package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"golang.org/x/crypto/bcrypt"
)

func TestFindUserByUsernameAndEmail(t *testing.T) {
	a := securityLoginApp(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("secret1"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{
		Username: "alice", Nickname: "alice", PasswordHash: string(hash),
		Email: "alice@example.com", EmailVerified: true,
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	u2 := model.User{
		Username: "bob", Nickname: "bob", PasswordHash: string(hash),
		Email: "bob@example.com", EmailVerified: false,
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&u2).Error; err != nil {
		t.Fatal(err)
	}

	got, ok := a.findUserByUsername("alice")
	if !ok || got.ID != u.ID {
		t.Fatalf("username: %+v ok=%v", got, ok)
	}
	if _, ok := a.findUserByUsername("alice@example.com"); ok {
		t.Fatal("account mode must not match email as username unless exact username")
	}
	got, ok = a.findUserByVerifiedEmail("Alice@Example.com")
	if !ok || got.ID != u.ID {
		t.Fatalf("verified email: %+v ok=%v", got, ok)
	}
	if _, ok := a.findUserByVerifiedEmail("bob@example.com"); ok {
		t.Fatal("unverified email")
	}
}

func TestLoginAccountMode(t *testing.T) {
	a := securityLoginApp(t)
	seedLoginUser(t, a, "acct1", "secret1")
	captchaOK(t, "cid-acct", "1234")
	body := url.Values{
		"mode": {"account"}, "username": {"acct1"}, "password": {"secret1"},
		"captcha_id": {"cid-acct"}, "captcha": {"1234"}, "agree": {"on"},
	}.Encode()
	w, c := ajaxForm(http.MethodPost, "/login", body)
	a.Login(c)
	assertCode(t, w, http.StatusOK)
}

func TestLoginEmailModeWithCode(t *testing.T) {
	a := securityLoginApp(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret1"), bcrypt.MinCost)
	u := model.User{
		Username: "mailogin", Nickname: "mailogin", PasswordHash: string(hash),
		Email: "mailogin@example.com", EmailVerified: true,
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	key := mailCodeKey("login", "mailogin@example.com")
	mailCodeStore.Lock()
	mailCodeStore.m[key] = mailCodeEntry{code: "654321", exp: time.Now().Add(time.Minute), lastSent: time.Now()}
	mailCodeStore.Unlock()

	body := url.Values{
		"mode": {"email"}, "email": {"mailogin@example.com"}, "code": {"654321"}, "agree": {"on"},
	}.Encode()
	w, c := ajaxForm(http.MethodPost, "/login", body)
	a.Login(c)
	assertCode(t, w, http.StatusOK)

	// 验证码一次性
	mailCodeStore.Lock()
	mailCodeStore.m[key] = mailCodeEntry{code: "654321", exp: time.Now().Add(time.Minute), lastSent: time.Now()}
	mailCodeStore.Unlock()
	w2, c2 := ajaxForm(http.MethodPost, "/login", url.Values{
		"mode": {"email"}, "email": {"nobody@example.com"}, "code": {"000000"}, "agree": {"on"},
	}.Encode())
	a.Login(c2)
	assertCode(t, w2, http.StatusBadRequest)
}

func TestSendLoginCodeAntiEnumeration(t *testing.T) {
	a := securityLoginApp(t)
	a.Limiter = middleware.NewShareLimiter(20, time.Minute)
	captchaOK(t, "cid-send-login", "1111")
	body, _ := json.Marshal(map[string]string{
		"email": "nosuch@example.com", "captcha_id": "cid-send-login", "captcha": "1111",
	})
	w, c := ajaxForm(http.MethodPost, "/login/email-code", string(body))
	c.Request.Header.Set("Content-Type", "application/json")
	a.SendLoginCode(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("body=%s", w.Body.String())
	}
	// 未注册邮箱也应进入 60s 冷却，避免与已注册邮箱形成差分
	captchaOK(t, "cid-send-login2", "2222")
	body2, _ := json.Marshal(map[string]string{
		"email": "nosuch@example.com", "captcha_id": "cid-send-login2", "captcha": "2222",
	})
	w2, c2 := ajaxForm(http.MethodPost, "/login/email-code", string(body2))
	c2.Request.Header.Set("Content-Type", "application/json")
	a.SendLoginCode(c2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("want cooldown for missing email, status=%d body=%s", w2.Code, w2.Body.String())
	}
}
