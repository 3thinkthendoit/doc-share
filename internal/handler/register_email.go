package handler

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"doc-share/internal/model"
	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
)

// 邮箱注册验证码：内存存储（单实例部署），5 分钟有效、60 秒重发冷却
const (
	mailCodeTTL      = 5 * time.Minute
	mailCodeCooldown = 60 * time.Second
)

type mailCodeEntry struct {
	code     string
	exp      time.Time
	lastSent time.Time
	fails    int // 连续校验失败次数，达上限即销毁（防暴力枚举）
}

var mailCodeStore = struct {
	sync.Mutex
	m map[string]mailCodeEntry
}{m: map[string]mailCodeEntry{}}

// SendRegisterCode 发送邮箱注册验证码：POST /register/email-code {email, captcha_id, captcha}
// 前置：邮箱注册已开放、格式正确、邮箱未被注册、图形验证码通过、60 秒冷却、IP 限流
func (a *App) SendRegisterCode(c *gin.Context) {
	var req struct {
		Email     string `json:"email"`
		CaptchaID string `json:"captcha_id"`
		Captcha   string `json:"captcha"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	s := a.Settings()
	if s.RegMethod != "email" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前未开放邮箱注册"})
		return
	}
	if !util.ValidEmail(email) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "邮箱格式不正确"})
		return
	}
	var exists int64
	a.DB.Model(&model.User{}).Where("username = ? OR email = ?", email, email).Count(&exists)
	if exists > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该邮箱已被注册"})
		return
	}
	// 60 秒冷却与 IP 限流先于图形验证码校验：验证码一次性，避免被无关限制白白消耗
	mailCodeStore.Lock()
	now := time.Now()
	if e, ok := mailCodeStore.m[email]; ok && now.Sub(e.lastSent) < mailCodeCooldown {
		mailCodeStore.Unlock()
		c.JSON(http.StatusTooManyRequests, gin.H{"error": fmt.Sprintf("发送过于频繁，请 %d 秒后再试", int((mailCodeCooldown-time.Since(e.lastSent))/time.Second)+1)})
		return
	}
	mailCodeStore.Unlock()
	// IP 维度限流，防批量刷邮件（5 次/10 分钟）
	if !a.Limiter.Allowed(c.ClientIP(), "mail:"+c.ClientIP()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "发送过于频繁，请稍后再试"})
		return
	}
	// 图形验证码：防批量刷邮件（一次性，失败即消耗）
	if !a.CaptchaMgr.Verify(req.CaptchaID, req.Captcha, true) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码错误或已过期", "captchaFailed": true})
		return
	}

	mailCodeStore.Lock()
	code := genMailCode()
	mailCodeStore.m[email] = mailCodeEntry{code: code, exp: now.Add(mailCodeTTL), lastSent: now}
	mailCodeStore.Unlock()
	mailCodeStore.m[email] = mailCodeEntry{code: code, exp: now.Add(mailCodeTTL), lastSent: now}
	mailCodeStore.Unlock()

	cfg := s.MailConfig()
	subject := s.SiteName + " 注册验证码"
	body := fmt.Sprintf("<p>你的注册验证码为：<b style=\"font-size:20px\">%s</b></p><p>%d 分钟内有效。若非本人操作，请忽略本邮件。</p>",
		code, int(mailCodeTTL/time.Minute))
	if err := util.SendMail(cfg, email, subject, body); err != nil {
		// 发送失败回滚验证码，允许用户重试
		mailCodeStore.Lock()
		delete(mailCodeStore.m, email)
		mailCodeStore.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码邮件发送失败：" + err.Error()})
		return
	}
	a.Limiter.Fail(c.ClientIP(), "mail:"+c.ClientIP()) // 占用一次 IP 限额
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// verifyMailCode 校验并消耗验证码；连续失败 5 次即销毁，防暴力枚举
func (a *App) verifyMailCode(email, code string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	code = strings.TrimSpace(code)
	if email == "" || len(code) != 6 {
		return false
	}
	mailCodeStore.Lock()
	defer mailCodeStore.Unlock()
	e, ok := mailCodeStore.m[email]
	if !ok || time.Now().After(e.exp) {
		return false
	}
	if e.code != code {
		e.fails++
		if e.fails >= 5 {
			delete(mailCodeStore.m, email)
		} else {
			mailCodeStore.m[email] = e
		}
		return false
	}
	delete(mailCodeStore.m, email) // 一次性，验证即销毁
	return true
}

// genMailCode 6 位纯数字验证码
func genMailCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "000000" // 极端情况下熵源失败，仍保证流程可走（概率可忽略）
	}
	return fmt.Sprintf("%06d", n.Int64())
}
