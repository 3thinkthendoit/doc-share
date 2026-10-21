package handler

import (
	"encoding/json"
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

func ajaxForm(method, path, body string) (*httptest.ResponseRecorder, *gin.Context) {
	w, c := newCtx()
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	c.Request.Header.Set("Accept", "application/json")
	return w, c
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("json: %v body=%s", err, w.Body.String())
	}
	return m
}

func TestLoginAndRegisterAjax(t *testing.T) {
	a, _ := newAuthApp(t)
	a.Signer = session.NewSigner("test-secret-key-at-least-32-bytes!!")
	a.CaptchaMgr = base64Captcha.NewCaptcha(
		base64Captcha.NewDriverDigit(80, 240, 4, 0.7, 80),
		base64Captcha.DefaultMemStore,
	)
	hash, err := bcrypt.GenerateFromPassword([]byte("secret1"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user := model.User{
		Username: "ajaxuser", Nickname: "ajax", PasswordHash: string(hash),
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	t.Run("login_bad_captcha", func(t *testing.T) {
		if err := base64Captcha.DefaultMemStore.Set("cid-bad", "9999"); err != nil {
			t.Fatal(err)
		}
		body := url.Values{
			"mode": {"account"}, "username": {"ajaxuser"}, "password": {"secret1"},
			"captcha_id": {"cid-bad"}, "captcha": {"0000"}, "agree": {"on"},
		}.Encode()
		w, c := ajaxForm(http.MethodPost, "/login", body)
		a.Login(c)
		assertCode(t, w, http.StatusBadRequest)
		if decodeJSON(t, w)["error"] != "验证码错误或已过期" {
			t.Fatalf("body = %s", w.Body.String())
		}
	})

	t.Run("login_ok", func(t *testing.T) {
		if err := base64Captcha.DefaultMemStore.Set("cid-ok", "1234"); err != nil {
			t.Fatal(err)
		}
		body := url.Values{
			"mode": {"account"}, "username": {"ajaxuser"}, "password": {"secret1"},
			"captcha_id": {"cid-ok"}, "captcha": {"1234"}, "agree": {"on"},
		}.Encode()
		w, c := ajaxForm(http.MethodPost, "/login", body)
		a.Login(c)
		assertCode(t, w, http.StatusOK)
		m := decodeJSON(t, w)
		if m["redirect"] != "/console" || m["ok"] != true {
			t.Fatalf("body = %s", w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Set-Cookie"), middleware.CookieSession+"=") {
			t.Fatalf("missing session cookie: %s", w.Header().Get("Set-Cookie"))
		}
	})

	t.Run("register_ok", func(t *testing.T) {
		if err := base64Captcha.DefaultMemStore.Set("cid-reg", "4321"); err != nil {
			t.Fatal(err)
		}
		body := url.Values{
			"username": {"newajax"}, "password": {"secret1"},
			"captcha_id": {"cid-reg"}, "captcha": {"4321"}, "agree": {"on"},
		}.Encode()
		w, c := ajaxForm(http.MethodPost, "/register", body)
		a.Register(c)
		assertCode(t, w, http.StatusOK)
		if decodeJSON(t, w)["redirect"] != "/console" {
			t.Fatalf("body = %s", w.Body.String())
		}
	})
}

func TestLogoutAjax(t *testing.T) {
	a := &App{}
	w, c := ajaxForm(http.MethodPost, "/logout", "")
	a.Logout(c)
	assertCode(t, w, http.StatusOK)
	m := decodeJSON(t, w)
	if m["redirect"] != "/login" {
		t.Fatalf("body = %s", w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), middleware.CookieSession+"=") {
		t.Fatalf("missing cleared cookie: %s", w.Header().Get("Set-Cookie"))
	}
}

func TestShareSubmitAjax(t *testing.T) {
	a, doc := newAuthApp(t)
	a.Signer = session.NewSigner("test-secret-key-at-least-32-bytes!!")
	a.Limiter = middleware.NewShareLimiter(5, time.Minute)
	hash, err := bcrypt.GenerateFromPassword([]byte("gatepw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	share := model.Share{DocumentID: doc.ID, ShareToken: "gatetok1", Password: string(hash)}
	if err := a.DB.Create(&share).Error; err != nil {
		t.Fatal(err)
	}

	t.Run("wrong_password", func(t *testing.T) {
		w, c := ajaxForm(http.MethodPost, "/s/gatetok1", url.Values{"password": {"nope"}}.Encode())
		c.Params = gin.Params{{Key: "token", Value: "gatetok1"}}
		a.ShareSubmit(c)
		assertCode(t, w, http.StatusBadRequest)
		if decodeJSON(t, w)["error"] != "share.pwdWrong" {
			t.Fatalf("body = %s", w.Body.String())
		}
	})

	t.Run("right_password", func(t *testing.T) {
		w, c := ajaxForm(http.MethodPost, "/s/gatetok1", url.Values{"password": {"gatepw"}}.Encode())
		c.Params = gin.Params{{Key: "token", Value: "gatetok1"}}
		a.ShareSubmit(c)
		assertCode(t, w, http.StatusOK)
		m := decodeJSON(t, w)
		if m["ok"] != true || m["redirect"] != "/s/gatetok1" {
			t.Fatalf("body = %s", w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Set-Cookie"), CookieShare+"=") {
			t.Fatalf("missing share cookie: %s", w.Header().Get("Set-Cookie"))
		}
		if strings.Contains(w.Body.String(), "<html") {
			t.Fatal("ajax success must not return html")
		}
	})

	t.Run("no_password_returns_json", func(t *testing.T) {
		if err := a.DB.Model(&model.Share{}).Where("share_token = ?", "gatetok1").Update("password", "").Error; err != nil {
			t.Fatal(err)
		}
		w, c := ajaxForm(http.MethodPost, "/s/gatetok1", "")
		c.Params = gin.Params{{Key: "token", Value: "gatetok1"}}
		a.ShareSubmit(c)
		assertCode(t, w, http.StatusOK)
		if decodeJSON(t, w)["redirect"] != "/s/gatetok1" {
			t.Fatalf("body = %s", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "<html") {
			t.Fatal("ajax bypass must not return html")
		}
	})
}

func TestProjectShareSubmitAjax(t *testing.T) {
	a, doc := newAuthApp(t)
	if err := a.DB.AutoMigrate(&model.ProjectShare{}); err != nil {
		t.Fatal(err)
	}
	a.Signer = session.NewSigner("test-secret-key-at-least-32-bytes!!")
	a.Limiter = middleware.NewShareLimiter(5, time.Minute)
	hash, err := bcrypt.GenerateFromPassword([]byte("gatepw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ps := model.ProjectShare{ProjectID: doc.ProjectID, ShareToken: "psgate01", Password: string(hash)}
	if err := a.DB.Create(&ps).Error; err != nil {
		t.Fatal(err)
	}

	w, c := ajaxForm(http.MethodPost, "/ps/psgate01", url.Values{"password": {"nope"}}.Encode())
	c.Params = gin.Params{{Key: "token", Value: "psgate01"}}
	a.ProjectShareSubmit(c)
	assertCode(t, w, http.StatusBadRequest)
	if decodeJSON(t, w)["error"] != "share.pwdWrong" {
		t.Fatalf("body = %s", w.Body.String())
	}

	if err := a.DB.Model(&model.ProjectShare{}).Where("share_token = ?", "psgate01").Update("password", "").Error; err != nil {
		t.Fatal(err)
	}
	w, c = ajaxForm(http.MethodPost, "/ps/psgate01", "")
	c.Params = gin.Params{{Key: "token", Value: "psgate01"}}
	a.ProjectShareSubmit(c)
	assertCode(t, w, http.StatusOK)
	if decodeJSON(t, w)["redirect"] != "/ps/psgate01" {
		t.Fatalf("body = %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "<html") {
		t.Fatal("ajax bypass must not return html")
	}
}
