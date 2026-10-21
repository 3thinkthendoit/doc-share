package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestFlashRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &App{}

	w1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(w1)
	c1.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	a.setFlash(c1, "获取用户信息失败")

	raw := cookieValue(w1, flashCookie)
	if raw == "" {
		t.Fatal("missing flash cookie")
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil || decoded != "获取用户信息失败" {
		t.Fatalf("cookie value=%q decoded=%q err=%v", raw, decoded, err)
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/login", nil)
	c2.Request.AddCookie(&http.Cookie{Name: flashCookie, Value: raw})
	got := a.takeFlash(c2)
	if got != "获取用户信息失败" {
		t.Fatalf("takeFlash=%q", got)
	}
	// 读后应下发清除 Cookie（MaxAge<0 或空值）
	cleared := false
	for _, c := range w2.Result().Cookies() {
		if c.Name == flashCookie && (c.MaxAge < 0 || c.Value == "") {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("flash not cleared: %v", w2.Header().Get("Set-Cookie"))
	}
	// 无 Cookie 的新请求再读应为空
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = httptest.NewRequest(http.MethodGet, "/login", nil)
	if a.takeFlash(c3) != "" {
		t.Fatal("empty request takeFlash must be empty")
	}
}

func TestConsumeQueryFlash(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &App{}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/login?error="+url.QueryEscape("第三方登录失败"), nil)
	if !a.consumeQueryFlash(c, "error", "/login", nil) {
		t.Fatal("expected redirect")
	}
	if w.Code != http.StatusFound {
		t.Fatalf("status=%d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Fatalf("Location=%q", loc)
	}
	raw := cookieValue(w, flashCookie)
	msg, _ := url.QueryUnescape(raw)
	if msg != "第三方登录失败" {
		t.Fatalf("flash=%q", msg)
	}

	// 无 query：不跳转
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/login", nil)
	if a.consumeQueryFlash(c2, "error", "/login", nil) {
		t.Fatal("clean URL must not redirect")
	}
}

func TestConsumeQueryFlashMapCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &App{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/s/tok?err=pwd", nil)
	if !a.consumeQueryFlash(c, "err", "/s/tok", mapShareErrCode) {
		t.Fatal("expected redirect")
	}
	raw := cookieValue(w, flashCookie)
	msg, _ := url.QueryUnescape(raw)
	if msg != "share.pwdWrong" {
		t.Fatalf("mapped flash=%q", msg)
	}
	if !strings.HasPrefix(w.Header().Get("Location"), "/s/tok") {
		t.Fatalf("Location=%q", w.Header().Get("Location"))
	}
}
