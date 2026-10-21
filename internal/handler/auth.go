package handler

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

const sessionTTL = 24 * time.Hour

// LoginPage 渲染登录页
func (a *App) LoginPage(c *gin.Context) {
	// 旧链接 /login?error=xxx → flash 后清地址栏
	if a.consumeQueryFlash(c, "error", "/login", nil) {
		return
	}
	data := gin.H{"title": "登录"}
	if flash := a.takeFlash(c); flash != "" {
		data["error"] = flash
	}
	for k, v := range a.oauthViewFlags() {
		data[k] = v
	}
	a.render(c, "login.html", data)
}

// Captcha 生成图形验证码：返回 {id, b64}，b64 为可直接赋给 img.src 的 data URI
func (a *App) Captcha(c *gin.Context) {
	id, b64s, _, err := a.CaptchaMgr.Generate()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "验证码生成失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "b64": b64s})
}

// findUserByUsername 账号登录：仅用户名精确匹配
func (a *App) findUserByUsername(username string) (model.User, bool) {
	var user model.User
	name := strings.TrimSpace(username)
	if name == "" {
		return user, false
	}
	if err := a.DB.Where("username = ?", name).First(&user).Error; err != nil {
		return user, false
	}
	return user, true
}

// findUserByVerifiedEmail 邮箱登录：仅已验证邮箱
func (a *App) findUserByVerifiedEmail(email string) (model.User, bool) {
	var user model.User
	em := strings.ToLower(strings.TrimSpace(email))
	if !util.ValidEmail(em) {
		return user, false
	}
	if err := a.DB.Where("LOWER(email) = ? AND email_verified = ?", em, true).First(&user).Error; err != nil {
		return user, false
	}
	return user, true
}

// Login 处理登录。mode=account：账号+密码+图形验证码；mode=email：邮箱+邮件验证码。
func (a *App) Login(c *gin.Context) {
	mode := strings.TrimSpace(c.PostForm("mode"))
	if mode == "" {
		mode = "account"
	}

	fail := func(msg string) {
		if isAjax(c) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}
		data := gin.H{"title": "登录", "error": msg}
		for k, v := range a.oauthViewFlags() {
			data[k] = v
		}
		a.render(c, "login.html", data)
	}

	// 协议勾选服务端校验（前端可绕过）
	if c.PostForm("agree") == "" {
		fail(a.tr(c, "login.needAgree"))
		return
	}

	var user model.User
	var ok bool

	switch mode {
	case "email":
		email := strings.ToLower(strings.TrimSpace(c.PostForm("email")))
		code := c.PostForm("code")
		if !util.ValidEmail(email) {
			fail(a.tr(c, "邮箱格式不正确"))
			return
		}
		if !a.verifyMailCode("login", email, code) {
			fail(a.tr(c, "邮箱验证码错误或已过期"))
			return
		}
		user, ok = a.findUserByVerifiedEmail(email)
		if !ok {
			// 验证码已消耗；统一文案，避免枚举
			fail(a.tr(c, "邮箱验证码错误或已过期"))
			return
		}
	case "account":
		if !a.CaptchaMgr.Verify(c.PostForm("captcha_id"), c.PostForm("captcha"), true) {
			fail("验证码错误或已过期")
			return
		}
		user, ok = a.findUserByUsername(c.PostForm("username"))
		if !ok {
			fail("用户名或密码错误")
			return
		}
	default:
		fail(a.tr(c, "登录失败"))
		return
	}

	if user.Status != model.StatusEnabled {
		fail("账号已被禁用")
		return
	}
	a.expireLoginLock(&user)
	if user.Locked() {
		fail(msgLocked)
		return
	}

	if mode == "account" {
		password := c.PostForm("password")
		if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
			if a.Settings().LoginMaxFails > 0 && a.recordLoginFailure(&user) {
				fail(msgLocked)
				return
			}
			fail("用户名或密码错误")
			return
		}
	}

	if a.Settings().TOTPEnabled {
		a.beginTOTP(c, &user)
		return
	}
	a.issueSession(c, &user)
}

// SendLoginCode 发送邮箱登录验证码：POST /login/email-code
// 无论邮箱是否存在均返回统一成功（防枚举）；仅已验证启用账号真正发信。
func (a *App) SendLoginCode(c *gin.Context) {
	var req struct {
		Email     string `json:"email" form:"email"`
		CaptchaID string `json:"captcha_id" form:"captcha_id"`
		Captcha   string `json:"captcha" form:"captcha"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	okMsg := a.tr(c, "login.codeSent")
	if !util.ValidEmail(email) {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "邮箱格式不正确")})
		return
	}
	key := mailCodeKey("login", email)
	mailCodeStore.Lock()
	now := time.Now()
	if e, ok := mailCodeStore.m[key]; ok && now.Sub(e.lastSent) < mailCodeCooldown {
		mailCodeStore.Unlock()
		c.JSON(http.StatusTooManyRequests, gin.H{"error": fmt.Sprintf("发送过于频繁，请 %d 秒后再试", int((mailCodeCooldown-time.Since(e.lastSent))/time.Second)+1)})
		return
	}
	mailCodeStore.Unlock()
	if !a.Limiter.Allowed(c.ClientIP(), "mail:"+c.ClientIP()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": a.tr(c, "发送过于频繁，请稍后再试")})
		return
	}
	if !a.CaptchaMgr.Verify(req.CaptchaID, req.Captcha, true) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码错误或已过期", "captchaFailed": true})
		return
	}

	// 无论是否命中用户都记冷却与 IP 限额，避免「有邮箱才进入 60s/限流」的差分枚举
	user, found := a.findUserByVerifiedEmail(email)
	sendOK := found && user.Status == model.StatusEnabled
	var code string
	mailCodeStore.Lock()
	entry := mailCodeEntry{lastSent: now, exp: now.Add(mailCodeTTL)}
	if sendOK {
		code = genMailCode()
		entry.code = code
	}
	mailCodeStore.m[key] = entry
	mailCodeStore.Unlock()
	a.Limiter.Fail(c.ClientIP(), "mail:"+c.ClientIP())

	if sendOK {
		s := a.Settings()
		subject := s.SiteName + " " + a.tr(c, "login.codeMailSubject")
		body := fmt.Sprintf("<p>%s</p><p><b style=\"font-size:20px\">%s</b></p><p>%s</p>",
			a.tr(c, "login.codeMailIntro"), code, a.tr(c, "recover.mailIgnore"))
		if err := util.SendMail(s.MailConfig(), email, subject, body); err != nil {
			// 清掉可用验证码但保留 lastSent，冷却行为与未注册邮箱一致
			mailCodeStore.Lock()
			if e, ok := mailCodeStore.m[key]; ok {
				e.code = ""
				mailCodeStore.m[key] = e
			}
			mailCodeStore.Unlock()
			log.Printf("login email-code to %s: %v", email, err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": okMsg})
}

// Logout 登出
func (a *App) Logout(c *gin.Context) {
	a.setCookie(c, middleware.CookieSession, "", -1)
	if isAjax(c) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "redirect": "/login"})
		return
	}
	c.Redirect(http.StatusFound, "/login")
}

// ChangePassword 登录用户修改自己的密码（需验证原密码）
func (a *App) ChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.OldPassword == "" || req.NewPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "原密码和新密码均不能为空"})
		return
	}
	if len(req.NewPassword) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码至少 6 位"})
		return
	}
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "原密码错误"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器错误"})
		return
	}
	if err := a.DB.Model(user).UpdateColumn("password_hash", string(hash)).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "修改失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RegisterPage 注册页（公开）
func (a *App) RegisterPage(c *gin.Context) {
	s := a.Settings()
	data := gin.H{
		"title": "注册", "settings": s, "method": s.RegMethod, "identifier": "", "username": "",
	}
	for k, v := range a.oauthViewFlags() {
		data[k] = v
	}
	a.render(c, "register.html", data)
}

// Register 用户注册：默认普通用户（viewer），成功后自动登录。
// 注册方式由系统设置唯一决定（username / email / phone），前端不传方式：
//   - 用户名注册：账号 + 密码（图形验证码防批量注册）
//   - 邮箱注册：账号 + 邮箱 + SMTP 验证码（免图形验证码）
//   - 手机注册：账号 + 手机号（短信网关未接入，暂以图形验证码替代）
func (a *App) Register(c *gin.Context) {
	s := a.Settings()
	method := s.RegMethod
	password := c.PostForm("password")
	username := strings.TrimSpace(c.PostForm("username"))
	identifier := strings.TrimSpace(c.PostForm("identifier"))

	var email, phone string
	renderErr := func(msg string) {
		if isAjax(c) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}
		data := gin.H{
			"title": "注册", "error": msg, "settings": s,
			"username": username, "identifier": identifier, "method": method,
		}
		for k, v := range a.oauthViewFlags() {
			data[k] = v
		}
		a.render(c, "register.html", data)
	}

	if username == "" || len(username) > 50 {
		renderErr("账号不能为空且不超过 50 字符")
		return
	}
	if len(password) < 6 {
		renderErr("密码至少 6 位")
		return
	}
	if c.PostForm("agree") == "" {
		renderErr(a.tr(c, "login.needAgree"))
		return
	}

	// 图形验证码：邮箱注册免（邮箱验证码即所有权证明）；用户名/手机保留防批量注册
	if method != "email" {
		if !a.CaptchaMgr.Verify(c.PostForm("captcha_id"), c.PostForm("captcha"), true) {
			renderErr("验证码错误或已过期")
			return
		}
	}

	var exists int64
	a.DB.Model(&model.User{}).Where("username = ?", username).Count(&exists)
	if exists > 0 {
		renderErr("该账号已被注册")
		return
	}

	switch method {
	case "email":
		if !util.ValidEmail(identifier) {
			renderErr("邮箱格式不正确")
			return
		}
		emailNorm := strings.ToLower(strings.TrimSpace(identifier))
		a.DB.Model(&model.User{}).Where("LOWER(email) = ?", emailNorm).Count(&exists)
		if exists > 0 {
			renderErr("该邮箱已被注册")
			return
		}
		// 验证码放在所有前置校验之后：一次性资源，避免其它错误白白消耗
		if !a.verifyMailCode("reg", identifier, c.PostForm("code")) {
			renderErr("邮箱验证码错误或已过期")
			return
		}
		email = emailNorm
	case "phone":
		if !util.ValidPhone(identifier) {
			renderErr("手机号格式不正确")
			return
		}
		a.DB.Model(&model.User{}).Where("phone = ?", identifier).Count(&exists)
		if exists > 0 {
			renderErr("该手机号已被注册")
			return
		}
		phone = identifier
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		renderErr("服务器错误")
		return
	}
	user := model.User{
		Username:      username,
		PasswordHash:  string(hash),
		Nickname:      username,
		Email:         email,
		EmailVerified: method == "email" && email != "",
		Phone:         phone,
		Role:          model.RoleViewer, // 注册默认普通用户
		Status:        model.StatusEnabled,
	}
	if err := a.DB.Create(&user).Error; err != nil {
		// 并发注册时依赖 username 唯一索引兜底
		renderErr("注册失败，账号可能已被占用")
		return
	}

	if a.Settings().TOTPEnabled {
		a.beginTOTP(c, &user)
		return
	}
	a.issueSession(c, &user)
}
