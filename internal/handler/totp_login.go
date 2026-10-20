package handler

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	totpPendingTTL = 5 * time.Minute
	totpTryCap     = 5
	msgLocked      = "账号已锁定，请稍后再试或联系管理员解锁"
	msgTOTPBad     = "动态口令错误"
	msgTOTPExpired = "登录已过期，请重新输入密码"
	msgTOTPTooMany = "动态口令错误次数过多，请重新登录"
	msgTOTPOff     = "动态口令验证已关闭，请重新登录"
)

func (a *App) sealTOTPSecret(plain string) (string, error) {
	if a.Signer == nil || plain == "" {
		return "", fmt.Errorf("empty")
	}
	block, err := aes.NewCipher(a.Signer.DeriveKey("totp-secret"))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return "v1:" + base64.RawStdEncoding.EncodeToString(out), nil
}

func (a *App) openTOTPSecret(sealed string) (string, error) {
	raw, ok := strings.CutPrefix(sealed, "v1:")
	if !ok || a.Signer == nil {
		return "", fmt.Errorf("bad secret")
	}
	buf, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(a.Signer.DeriveKey("totp-secret"))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(buf) < gcm.NonceSize() {
		return "", fmt.Errorf("short")
	}
	plain, err := gcm.Open(nil, buf[:gcm.NonceSize()], buf[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (a *App) signTOTPPending(uid uint, mode, secret string, epoch int) string {
	exp := time.Now().Add(totpPendingTTL).Unix()
	// 用 | 分段。表单提交会把字段里的换行规范成 CRLF，换行一变 HMAC 就对不上，登录会被判成过期。
	payload := fmt.Sprintf("totp|%d|%d|%s|%s|%d", uid, exp, mode, secret, epoch)
	return a.Signer.Sign(payload)
}

func (a *App) parseTOTPPending(token string) (uid uint, mode, secret string, epoch int, ok bool) {
	if a.Signer == nil {
		return 0, "", "", 0, false
	}
	payload, valid := a.Signer.Verify(token)
	if !valid {
		return 0, "", "", 0, false
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 6 || parts[0] != "totp" {
		return 0, "", "", 0, false
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, "", "", 0, false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return 0, "", "", 0, false
	}
	if parts[3] != "enroll" && parts[3] != "verify" {
		return 0, "", "", 0, false
	}
	ep, err := strconv.Atoi(parts[5])
	if err != nil {
		return 0, "", "", 0, false
	}
	return uint(id), parts[3], parts[4], ep, true
}

func newTOTPKey(issuer, account string) (*otp.Key, string, error) {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		issuer = defaultSiteName
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
		SecretSize:  20,
	})
	if err != nil {
		return nil, "", err
	}
	img, err := key.Image(200, 200)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, "", err
	}
	qr := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	return key, qr, nil
}

func totpCodeOK(code, secret string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	return totp.Validate(code, secret)
}

// recordLoginFailure 原子累加错误次数；达到锁定阈值时写入解锁时间。
func (a *App) recordLoginFailure(user *model.User) bool {
	s := a.Settings()
	if err := a.DB.Exec(
		"UPDATE users SET failed_logins = failed_logins + 1 WHERE id = ?", user.ID,
	).Error; err != nil {
		return false
	}
	var fails int
	if err := a.DB.Raw("SELECT failed_logins FROM users WHERE id = ?", user.ID).Scan(&fails).Error; err != nil {
		return false
	}
	user.FailedLogins = fails
	if s.LoginMaxFails <= 0 || fails < s.LoginMaxFails {
		return false
	}
	mins := s.LoginLockMinutes
	if mins < 1 {
		mins = 30
	}
	until := time.Now().Add(time.Duration(mins) * time.Minute)
	if err := a.DB.Exec(
		"UPDATE users SET locked_until = ? WHERE id = ?", until, user.ID,
	).Error; err != nil {
		return false
	}
	user.LockedUntil = &until
	return true
}

func (a *App) respondTOTP(c *gin.Context, step, pending, qr, secret string) {
	if isAjax(c) {
		body := gin.H{"step": step, "pending": pending}
		if qr != "" {
			body["qr"] = qr
			body["secret"] = secret
		}
		c.JSON(http.StatusOK, body)
		return
	}
	a.render(c, "login.html", gin.H{
		"title":       "登录",
		"totpStep":    step,
		"totpPending": pending,
		"totpQR":      qr,
		"totpSecret":  secret,
	})
}

// clearCorruptTOTP 密钥无法解密（如轮换 session_secret）时清空，迫使重新绑定。
func (a *App) clearCorruptTOTP(user *model.User) {
	next := user.TOTPEpoch + 1
	_ = a.DB.Exec(
		"UPDATE users SET totp_secret = '', totp_epoch = ? WHERE id = ?", next, user.ID,
	).Error
	user.TOTPSecret = ""
	user.TOTPEpoch = next
}

func (a *App) beginTOTP(c *gin.Context, user *model.User) {
	if strings.TrimSpace(user.TOTPSecret) != "" {
		if _, err := a.openTOTPSecret(user.TOTPSecret); err != nil {
			a.clearCorruptTOTP(user)
		}
	}
	if strings.TrimSpace(user.TOTPSecret) == "" {
		key, qr, err := newTOTPKey(a.Settings().SiteName, user.Username)
		if err != nil {
			if isAjax(c) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "动态口令生成失败"})
				return
			}
			a.render(c, "login.html", gin.H{"title": "登录", "error": "动态口令生成失败"})
			return
		}
		a.respondTOTP(c, "totp_enroll", a.signTOTPPending(user.ID, "enroll", key.Secret(), user.TOTPEpoch), qr, key.Secret())
		return
	}
	a.respondTOTP(c, "totp", a.signTOTPPending(user.ID, "verify", "", user.TOTPEpoch), "", "")
}

// LoginTOTP 校验动态口令。enroll 在口令正确后才把密钥写入账号。
func (a *App) LoginTOTP(c *gin.Context) {
	fail := func(msg string) {
		if isAjax(c) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}
		a.render(c, "login.html", gin.H{"title": "登录", "error": msg})
	}
	if !a.Settings().TOTPEnabled {
		fail(msgTOTPOff)
		return
	}
	uid, mode, secret, epoch, ok := a.parseTOTPPending(c.PostForm("pending"))
	if !ok {
		fail(msgTOTPExpired)
		return
	}
	var user model.User
	if err := a.DB.First(&user, uid).Error; err != nil {
		fail(msgTOTPExpired)
		return
	}
	if user.Status != model.StatusEnabled {
		fail("账号已被禁用")
		return
	}
	if user.TOTPEpoch != epoch {
		fail(msgTOTPExpired)
		return
	}
	a.expireLoginLock(&user)
	if user.Locked() {
		fail(msgLocked)
		return
	}
	// 未开启账户锁定时，动态口令错误达到上限后直接拒绝（含猜对也不发会话）
	if a.Settings().LoginMaxFails == 0 && user.FailedLogins >= totpTryCap {
		fail(msgTOTPTooMany)
		return
	}
	if mode == "enroll" {
		if user.TOTPBound() {
			fail(msgTOTPExpired)
			return
		}
	} else {
		opened, err := a.openTOTPSecret(user.TOTPSecret)
		if err != nil || opened == "" {
			a.clearCorruptTOTP(&user)
			fail(msgTOTPExpired)
			return
		}
		secret = opened
	}
	if !totpCodeOK(c.PostForm("code"), secret) {
		if a.recordLoginFailure(&user) {
			fail(msgLocked)
			return
		}
		if a.Settings().LoginMaxFails == 0 && user.FailedLogins >= totpTryCap {
			fail(msgTOTPTooMany)
			return
		}
		fail(msgTOTPBad)
		return
	}
	// 成功：写入密钥（若绑定）并递增 epoch，使同一张 pending 失效；带 epoch 条件防重放
	updates := map[string]any{"totp_epoch": epoch + 1}
	if mode == "enroll" {
		sealed, err := a.sealTOTPSecret(secret)
		if err != nil {
			fail("动态口令绑定失败")
			return
		}
		updates["totp_secret"] = sealed
	}
	res := a.DB.Model(&model.User{}).Where("id = ? AND totp_epoch = ?", user.ID, epoch).Updates(updates)
	if res.Error != nil || res.RowsAffected != 1 {
		fail(msgTOTPExpired)
		return
	}
	user.TOTPEpoch = epoch + 1
	a.issueSession(c, &user)
}

func (a *App) clearLoginLock(user *model.User) error {
	err := a.DB.Exec(
		"UPDATE users SET failed_logins = 0, locked_until = NULL WHERE id = ?", user.ID,
	).Error
	if err == nil {
		user.FailedLogins = 0
		user.LockedUntil = nil
	}
	return err
}

// expireLoginLock 锁定时间已过则清零，避免错误次数残留导致下一次立刻再锁。
func (a *App) expireLoginLock(user *model.User) {
	if user.LockedUntil == nil || user.Locked() {
		return
	}
	_ = a.clearLoginLock(user)
}

func (a *App) issueSession(c *gin.Context, user *model.User) {
	_ = a.clearLoginLock(user)
	// 开启全站 TOTP 时，会话必须带验证标记；否则旧会话在中间件会被拒绝
	token := a.Signer.MakeUserToken(user.ID, sessionTTL, a.Settings().TOTPEnabled)
	a.setCookie(c, middleware.CookieSession, token, int(sessionTTL.Seconds()))
	if isAjax(c) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "redirect": "/console"})
		return
	}
	c.Redirect(http.StatusFound, "/console")
}
