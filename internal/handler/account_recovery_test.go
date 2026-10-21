package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/session"

	"github.com/gin-gonic/gin"
	"github.com/mojocn/base64Captcha"
	"golang.org/x/crypto/bcrypt"
)

func TestPasswordResetTokenRoundTrip(t *testing.T) {
	a := oauthTestApp(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("oldpass1"), bcrypt.MinCost)
	u := &model.User{ID: 42, PasswordHash: string(hash)}
	tok, err := a.mintPasswordResetToken(u)
	if err != nil {
		t.Fatal(err)
	}
	claims, ok := a.parsePasswordResetToken(tok)
	if !ok || claims.UID != 42 || claims.Stamp != pwdResetStamp(u.PasswordHash) {
		t.Fatalf("got %+v ok=%v", claims, ok)
	}
	if _, ok := a.parsePasswordResetToken(tok + "x"); ok {
		t.Fatal("tampered")
	}
	a2 := &App{Signer: session.NewSigner("other-secret-key-xxxxxxxxxxxxxxxx")}
	if _, ok := a2.parsePasswordResetToken(tok); ok {
		t.Fatal("foreign signer")
	}
}

func TestPasswordResetTokenInvalidAfterPasswordChange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := oauthTestApp(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("oldpass1"), bcrypt.MinCost)
	u := model.User{
		Username: "rec2", PasswordHash: string(hash), Email: "rec2@example.com", EmailVerified: true,
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	tok, err := a.mintPasswordResetToken(&u)
	if err != nil {
		t.Fatal(err)
	}
	newHash, _ := bcrypt.GenerateFromPassword([]byte("newpass1"), bcrypt.MinCost)
	if err := a.DB.Model(&u).UpdateColumn("password_hash", string(newHash)).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	form := url.Values{"token": {tok}, "password": {"newer12"}, "confirm": {"newer12"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/reset-password", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	a.ResetPassword(c)
	if w.Code == http.StatusOK {
		t.Fatalf("expected reject after password change, body=%s", w.Body.String())
	}
}

func TestForgotUsernameAntiEnumeration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := oauthTestApp(t)
	a.Limiter = middleware.NewShareLimiter(20, time.Minute)
	store := base64Captcha.DefaultMemStore
	a.CaptchaMgr = base64Captcha.NewCaptcha(base64Captcha.NewDriverDigit(80, 240, 4, 0.7, 80), store)
	id, _, ans, err := a.CaptchaMgr.Generate()
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	form := url.Values{"email": {"nobody@example.com"}, "captcha_id": {id}, "captcha": {ans}}
	c.Request = httptest.NewRequest(http.MethodPost, "/forgot-username", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	a.ForgotUsername(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestResetPasswordWithToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := oauthTestApp(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("oldpass1"), bcrypt.MinCost)
	u := model.User{
		Username: "rec1", PasswordHash: string(hash), Email: "rec1@example.com", EmailVerified: true,
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	tok, err := a.mintPasswordResetToken(&u)
	if err != nil {
		t.Fatal(err)
	}
	// 邮件链接应可经 QueryEscape 往返
	escaped := url.QueryEscape(tok)
	raw, err := url.QueryUnescape(escaped)
	if err != nil || raw != tok {
		t.Fatalf("query escape roundtrip failed: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	form := url.Values{"token": {tok}, "password": {"newpass1"}, "confirm": {"newpass1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/reset-password", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	a.ResetPassword(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got model.User
	if err := a.DB.First(&got, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte("newpass1")) != nil {
		t.Fatal("password not updated")
	}

	// 同一 token 不可复用
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	form2 := url.Values{"token": {tok}, "password": {"again12"}, "confirm": {"again12"}}
	c2.Request = httptest.NewRequest(http.MethodPost, "/reset-password", strings.NewReader(form2.Encode()))
	c2.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c2.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	a.ResetPassword(c2)
	if w2.Code == http.StatusOK {
		t.Fatal("token should be invalid after successful reset")
	}
}
