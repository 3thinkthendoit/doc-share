package handler

import (
	"net/http"
	"strings"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

const sessionTTL = 24 * time.Hour

// LoginPage 渲染登录页
func (a *App) LoginPage(c *gin.Context) {
	a.render(c, "login.html", gin.H{"title": "登录"})
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

// Login 处理登录表单
func (a *App) Login(c *gin.Context) {
	username := c.PostForm("username")
	password := c.PostForm("password")

	// 先验验证码（一次性，失败即消耗），再进入密码校验
	if !a.CaptchaMgr.Verify(c.PostForm("captcha_id"), c.PostForm("captcha"), true) {
		a.render(c, "login.html", gin.H{"title": "登录", "error": "验证码错误或已过期"})
		return
	}

	var user model.User
	if err := a.DB.Where("username = ?", username).First(&user).Error; err != nil {
		a.render(c, "login.html", gin.H{"title": "登录", "error": "用户名或密码错误"})
		return
	}
	if user.Status != model.StatusEnabled {
		a.render(c, "login.html", gin.H{"title": "登录", "error": "账号已被禁用"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		a.render(c, "login.html", gin.H{"title": "登录", "error": "用户名或密码错误"})
		return
	}

	token := a.Signer.MakeUserToken(user.ID, sessionTTL)
	setCookie(c, middleware.CookieSession, token, int(sessionTTL.Seconds()))
	c.Redirect(http.StatusFound, "/admin")
}

// Logout 登出
func (a *App) Logout(c *gin.Context) {
	setCookie(c, middleware.CookieSession, "", -1)
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
	a.render(c, "register.html", gin.H{"title": "注册"})
}

// Register 用户注册：默认普通用户（viewer），成功后自动登录
func (a *App) Register(c *gin.Context) {
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")

	renderErr := func(msg string) {
		a.render(c, "register.html", gin.H{"title": "注册", "error": msg, "username": username})
	}

	// 与登录同样先验验证码，防批量机器注册（一次性，失败即消耗）
	if !a.CaptchaMgr.Verify(c.PostForm("captcha_id"), c.PostForm("captcha"), true) {
		renderErr("验证码错误或已过期")
		return
	}
	if username == "" || len(username) > 50 {
		renderErr("用户名不能为空且不超过 50 字符")
		return
	}
	if len(password) < 6 {
		renderErr("密码至少 6 位")
		return
	}

	var exists int64
	a.DB.Model(&model.User{}).Where("username = ?", username).Count(&exists)
	if exists > 0 {
		renderErr("用户名已存在")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		renderErr("服务器错误")
		return
	}
	user := model.User{
		Username:     username,
		PasswordHash: string(hash),
		Nickname:     username,
		Role:         model.RoleViewer, // 注册默认普通用户
		Status:       model.StatusEnabled,
	}
	if err := a.DB.Create(&user).Error; err != nil {
		// 并发注册时依赖 username 唯一索引兜底
		renderErr("注册失败，用户名可能已被占用")
		return
	}

	token := a.Signer.MakeUserToken(user.ID, sessionTTL)
	setCookie(c, middleware.CookieSession, token, int(sessionTTL.Seconds()))
	c.Redirect(http.StatusFound, "/admin")
}
