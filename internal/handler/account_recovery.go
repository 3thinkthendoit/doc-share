package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"doc-share/internal/model"
	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

const pwdResetTTL = 30 * time.Minute

// ForgotUsernamePage 忘记账号名
func (a *App) ForgotUsernamePage(c *gin.Context) {
	a.render(c, "forgot_username.html", gin.H{
		"title":     a.tr(c, "recover.forgotUserTitle"),
		"titleSolo": true,
	})
}

// ForgotUsername 提交邮箱：无论是否命中均返回统一成功文案（防枚举）；命中已验证邮箱则发信告知用户名。
func (a *App) ForgotUsername(c *gin.Context) {
	email := strings.ToLower(strings.TrimSpace(c.PostForm("email")))
	okMsg := a.tr(c, "recover.sentIfExists")
	fail := func(msg string) {
		if isAjax(c) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}
		a.render(c, "forgot_username.html", gin.H{
			"title": a.tr(c, "recover.forgotUserTitle"), "titleSolo": true, "error": msg, "email": email,
		})
	}
	if !util.ValidEmail(email) {
		fail(a.tr(c, "邮箱格式不正确"))
		return
	}
	if !a.CaptchaMgr.Verify(c.PostForm("captcha_id"), c.PostForm("captcha"), true) {
		fail(a.tr(c, "验证码错误或已过期"))
		return
	}
	if !a.Limiter.Allowed(c.ClientIP(), "recover:"+c.ClientIP()) {
		fail(a.tr(c, "发送过于频繁，请稍后再试"))
		return
	}

	var user model.User
	err := a.DB.Where("LOWER(email) = ? AND email_verified = ?", email, true).First(&user).Error
	if err == nil && user.Status == model.StatusEnabled {
		s := a.Settings()
		subject := s.SiteName + " " + a.tr(c, "recover.forgotUserMailSubject")
		body := fmt.Sprintf("<p>%s</p><p><b>%s</b></p><p>%s</p>",
			a.tr(c, "recover.forgotUserMailIntro"),
			user.Username,
			a.tr(c, "recover.mailIgnore"))
		if sendErr := util.SendMail(s.MailConfig(), email, subject, body); sendErr != nil {
			// 发信失败也走统一成功文案，避免与「邮箱不存在」形成差分枚举
			log.Printf("forgot-username mail to %s: %v", email, sendErr)
		}
	}
	// 无论是否命中均占限额，避免限流差分枚举
	a.Limiter.Fail(c.ClientIP(), "recover:"+c.ClientIP())

	if isAjax(c) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "message": okMsg})
		return
	}
	a.render(c, "forgot_username.html", gin.H{
		"title": a.tr(c, "recover.forgotUserTitle"), "titleSolo": true, "ok": okMsg, "email": email,
	})
}

// ForgotPasswordPage 找回密码（发重置链接）
func (a *App) ForgotPasswordPage(c *gin.Context) {
	a.render(c, "forgot_password.html", gin.H{
		"title":     a.tr(c, "recover.forgotPwdTitle"),
		"titleSolo": true,
	})
}

// ForgotPassword 提交邮箱发送重置链接
func (a *App) ForgotPassword(c *gin.Context) {
	email := strings.ToLower(strings.TrimSpace(c.PostForm("email")))
	okMsg := a.tr(c, "recover.sentIfExists")
	fail := func(msg string) {
		if isAjax(c) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}
		a.render(c, "forgot_password.html", gin.H{
			"title": a.tr(c, "recover.forgotPwdTitle"), "titleSolo": true, "error": msg, "email": email,
		})
	}
	if !util.ValidEmail(email) {
		fail(a.tr(c, "邮箱格式不正确"))
		return
	}
	if !a.CaptchaMgr.Verify(c.PostForm("captcha_id"), c.PostForm("captcha"), true) {
		fail(a.tr(c, "验证码错误或已过期"))
		return
	}
	if !a.Limiter.Allowed(c.ClientIP(), "recover:"+c.ClientIP()) {
		fail(a.tr(c, "发送过于频繁，请稍后再试"))
		return
	}

	var user model.User
	err := a.DB.Where("LOWER(email) = ? AND email_verified = ?", email, true).First(&user).Error
	if err == nil && user.Status == model.StatusEnabled {
		token, tokErr := a.mintPasswordResetToken(&user)
		if tokErr != nil {
			log.Printf("forgot-password mint token uid=%d: %v", user.ID, tokErr)
		} else {
			s := a.Settings()
			link := a.recoveryBaseURL(c) + "/reset-password?token=" + url.QueryEscape(token)
			subject := s.SiteName + " " + a.tr(c, "recover.forgotPwdMailSubject")
			body := fmt.Sprintf("<p>%s</p><p><a href=\"%s\">%s</a></p><p>%s</p><p>%s</p>",
				a.tr(c, "recover.forgotPwdMailIntro"),
				link, link,
				a.tr(c, "recover.forgotPwdMailTTL"),
				a.tr(c, "recover.mailIgnore"))
			if sendErr := util.SendMail(s.MailConfig(), email, subject, body); sendErr != nil {
				log.Printf("forgot-password mail to %s: %v", email, sendErr)
			}
		}
	}
	a.Limiter.Fail(c.ClientIP(), "recover:"+c.ClientIP())

	if isAjax(c) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "message": okMsg})
		return
	}
	a.render(c, "forgot_password.html", gin.H{
		"title": a.tr(c, "recover.forgotPwdTitle"), "titleSolo": true, "ok": okMsg, "email": email,
	})
}

// ResetPasswordPage 通过邮件链接设置新密码
func (a *App) ResetPasswordPage(c *gin.Context) {
	token := strings.TrimSpace(c.Query("token"))
	data := gin.H{
		"title":     a.tr(c, "recover.resetPwdTitle"),
		"titleSolo": true,
		"token":     token,
	}
	if token == "" {
		data["error"] = a.tr(c, "recover.tokenInvalid")
	} else if _, ok := a.parsePasswordResetToken(token); !ok {
		data["error"] = a.tr(c, "recover.tokenInvalid")
		data["token"] = ""
	}
	a.render(c, "reset_password.html", data)
}

// ResetPassword 提交新密码
func (a *App) ResetPassword(c *gin.Context) {
	token := strings.TrimSpace(c.PostForm("token"))
	pass := c.PostForm("password")
	confirm := c.PostForm("confirm")
	fail := func(msg string) {
		if isAjax(c) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}
		a.render(c, "reset_password.html", gin.H{
			"title": a.tr(c, "recover.resetPwdTitle"), "titleSolo": true, "error": msg, "token": token,
		})
	}
	claims, ok := a.parsePasswordResetToken(token)
	if !ok {
		fail(a.tr(c, "recover.tokenInvalid"))
		return
	}
	if len(pass) < 6 {
		fail(a.tr(c, "密码至少 6 位"))
		return
	}
	if pass != confirm {
		fail(a.tr(c, "两次输入的密码不一致"))
		return
	}
	var user model.User
	if err := a.DB.First(&user, claims.UID).Error; err != nil || user.Status != model.StatusEnabled {
		fail(a.tr(c, "recover.tokenInvalid"))
		return
	}
	// 密码已变更则旧链接失效（含刚用过的同一次 token）
	if pwdResetStamp(user.PasswordHash) != claims.Stamp {
		fail(a.tr(c, "recover.tokenInvalid"))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		fail(a.tr(c, "服务器错误"))
		return
	}
	if err := a.DB.Model(&user).UpdateColumn("password_hash", string(hash)).Error; err != nil {
		fail(a.tr(c, "修改失败"))
		return
	}
	a.expireLoginLock(&user)

	if isAjax(c) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "redirect": "/login"})
		return
	}
	a.render(c, "reset_password.html", gin.H{
		"title": a.tr(c, "recover.resetPwdTitle"), "titleSolo": true,
		"ok": a.tr(c, "recover.resetOk"), "token": "",
	})
}

type pwdResetClaims struct {
	UID   uint
	Stamp string
}

func pwdResetStamp(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:8])
}

func (a *App) mintPasswordResetToken(user *model.User) (string, error) {
	if a == nil || a.Signer == nil || user == nil || user.ID == 0 {
		return "", fmt.Errorf("signer or user missing")
	}
	nonce, err := randomHex(8)
	if err != nil {
		return "", err
	}
	exp := time.Now().Add(pwdResetTTL).Unix()
	payload := strings.Join([]string{
		"pwdreset",
		strconv.FormatUint(uint64(user.ID), 10),
		strconv.FormatInt(exp, 10),
		nonce,
		pwdResetStamp(user.PasswordHash),
	}, "|")
	return a.Signer.Sign(payload), nil
}

func (a *App) parsePasswordResetToken(token string) (pwdResetClaims, bool) {
	if a == nil || a.Signer == nil || token == "" {
		return pwdResetClaims{}, false
	}
	payload, valid := a.Signer.Verify(token)
	if !valid {
		return pwdResetClaims{}, false
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 5 || parts[0] != "pwdreset" {
		return pwdResetClaims{}, false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return pwdResetClaims{}, false
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || id == 0 {
		return pwdResetClaims{}, false
	}
	stamp := parts[4]
	if stamp == "" {
		return pwdResetClaims{}, false
	}
	return pwdResetClaims{UID: uint(id), Stamp: stamp}, true
}

// recoveryBaseURL 邮件内绝对链接前缀：优先站点域名，否则当前请求
func (a *App) recoveryBaseURL(c *gin.Context) string {
	if d := strings.TrimRight(strings.TrimSpace(a.Settings().SiteDomain), "/"); d != "" {
		return d
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}
