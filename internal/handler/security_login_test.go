package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/session"

	"github.com/gin-gonic/gin"
	"github.com/mojocn/base64Captcha"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

func securityLoginApp(t *testing.T) *App {
	t.Helper()
	a, _ := newAuthApp(t)
	a.Signer = session.NewSigner("test-secret-key-at-least-32-bytes!!")
	a.CaptchaMgr = base64Captcha.NewCaptcha(
		base64Captcha.NewDriverDigit(80, 240, 4, 0.7, 80),
		base64Captcha.DefaultMemStore,
	)
	return a
}

func putSetting(t *testing.T, a *App, key, value string) {
	t.Helper()
	if err := a.DB.Save(&model.SystemSetting{Key: key, Value: value}).Error; err != nil {
		t.Fatal(err)
	}
	a.setMu.Lock()
	a.setCache = nil
	a.setMu.Unlock()
}

func seedLoginUser(t *testing.T, a *App, username, password string) model.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{
		Username: username, Nickname: username, PasswordHash: string(hash),
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func captchaOK(t *testing.T, id, code string) {
	t.Helper()
	if err := base64Captcha.DefaultMemStore.Set(id, code); err != nil {
		t.Fatal(err)
	}
}

func TestLoginLockAndUnlock(t *testing.T) {
	a := securityLoginApp(t)
	putSetting(t, a, keyLoginMaxFails, "3")
	putSetting(t, a, keyLoginLockMinutes, "30")
	u := seedLoginUser(t, a, "lockme", "secret1")

	tryBad := func() map[string]any {
		captchaOK(t, "cid-lock", "1111")
		body := url.Values{
			"username": {"lockme"}, "password": {"wrong"},
			"captcha_id": {"cid-lock"}, "captcha": {"1111"},
		}.Encode()
		w, c := ajaxForm(http.MethodPost, "/login", body)
		a.Login(c)
		assertCode(t, w, http.StatusBadRequest)
		return decodeJSON(t, w)
	}

	if m := tryBad(); m["error"] != "用户名或密码错误" {
		t.Fatalf("1st = %v", m)
	}
	if m := tryBad(); m["error"] != "用户名或密码错误" {
		t.Fatalf("2nd = %v", m)
	}
	if m := tryBad(); m["error"] != msgLocked {
		t.Fatalf("3rd = %v", m)
	}

	var got model.User
	if err := a.DB.First(&got, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.Locked() || got.FailedLogins < 3 {
		t.Fatalf("want locked user, got fails=%d until=%v", got.FailedLogins, got.LockedUntil)
	}

	w, c := ajaxForm(http.MethodPost, "/console/api/users/"+itoa(int(u.ID))+"/unlock", "")
	c.Params = gin.Params{{Key: "id", Value: itoa(int(u.ID))}}
	a.UnlockUser(c)
	assertCode(t, w, http.StatusOK)

	var after model.User
	if err := a.DB.First(&after, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Locked() || after.FailedLogins != 0 {
		t.Fatalf("unlock failed: fails=%d until=%v", after.FailedLogins, after.LockedUntil)
	}

	captchaOK(t, "cid-ok", "2222")
	body := url.Values{
		"username": {"lockme"}, "password": {"secret1"},
		"captcha_id": {"cid-ok"}, "captcha": {"2222"},
	}.Encode()
	w2, c2 := ajaxForm(http.MethodPost, "/login", body)
	a.Login(c2)
	assertCode(t, w2, http.StatusOK)
}

func TestLoginLockAtomicUnderConcurrency(t *testing.T) {
	a := securityLoginApp(t)
	putSetting(t, a, keyLoginMaxFails, "5")
	putSetting(t, a, keyLoginLockMinutes, "30")
	u := seedLoginUser(t, a, "raceuser", "secret1")
	// sqlite 内存库多连接会各自空库；单连接下并行累加才能验证原子 UPDATE
	sqlDB, err := a.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var user model.User
			if err := a.DB.First(&user, u.ID).Error; err != nil {
				t.Error(err)
				return
			}
			a.recordLoginFailure(&user)
		}()
	}
	wg.Wait()

	var got model.User
	if err := a.DB.First(&got, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.Locked() || got.FailedLogins < 5 {
		t.Fatalf("want lock after concurrent fails, got fails=%d locked=%v", got.FailedLogins, got.Locked())
	}
}

func TestTOTPEnrollVerifyAndReset(t *testing.T) {
	a := securityLoginApp(t)
	putSetting(t, a, keyTOTPEnabled, "1")
	u := seedLoginUser(t, a, "totpuser", "secret1")

	captchaOK(t, "cid-totp", "3333")
	body := url.Values{
		"username": {"totpuser"}, "password": {"secret1"},
		"captcha_id": {"cid-totp"}, "captcha": {"3333"},
	}.Encode()
	w, c := ajaxForm(http.MethodPost, "/login", body)
	a.Login(c)
	assertCode(t, w, http.StatusOK)
	m := decodeJSON(t, w)
	if m["step"] != "totp_enroll" {
		t.Fatalf("want enroll step, got %v", m)
	}
	pending, _ := m["pending"].(string)
	secret, _ := m["secret"].(string)
	if pending == "" || secret == "" || m["qr"] == nil {
		t.Fatalf("enroll payload incomplete: %v", m)
	}

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w, c = ajaxForm(http.MethodPost, "/login/totp", url.Values{
		"pending": {pending}, "code": {code},
	}.Encode())
	a.LoginTOTP(c)
	assertCode(t, w, http.StatusOK)
	tok := cookieValue(w, middleware.CookieSession)
	if tok == "" {
		t.Fatalf("missing session cookie: %v", w.Result().Cookies())
	}
	claims, err := a.Signer.ParseUserToken(tok)
	if err != nil || !claims.TOTPVerified {
		t.Fatalf("session must be TOTP-verified: err=%v claims=%+v tok=%q", err, claims, tok)
	}

	var got model.User
	if err := a.DB.First(&got, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.TOTPBound() {
		t.Fatal("expected totp secret saved")
	}

	// 重放同一张 enroll pending 必须失败
	code, _ = totp.GenerateCode(secret, time.Now())
	w, c = ajaxForm(http.MethodPost, "/login/totp", url.Values{
		"pending": {pending}, "code": {code},
	}.Encode())
	a.LoginTOTP(c)
	assertCode(t, w, http.StatusBadRequest)

	// 第二次登录：仅校验
	captchaOK(t, "cid-totp2", "4444")
	w, c = ajaxForm(http.MethodPost, "/login", url.Values{
		"username": {"totpuser"}, "password": {"secret1"},
		"captcha_id": {"cid-totp2"}, "captcha": {"4444"},
	}.Encode())
	a.Login(c)
	assertCode(t, w, http.StatusOK)
	m = decodeJSON(t, w)
	if m["step"] != "totp" {
		t.Fatalf("want verify step, got %v", m)
	}
	pending, _ = m["pending"].(string)
	opened, err := a.openTOTPSecret(got.TOTPSecret)
	if err != nil {
		t.Fatal(err)
	}
	code, err = totp.GenerateCode(opened, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w, c = ajaxForm(http.MethodPost, "/login/totp", url.Values{
		"pending": {pending}, "code": {code},
	}.Encode())
	a.LoginTOTP(c)
	assertCode(t, w, http.StatusOK)

	w, c = ajaxForm(http.MethodPost, "/console/api/users/"+itoa(int(u.ID))+"/reset-totp", "")
	c.Params = gin.Params{{Key: "id", Value: itoa(int(u.ID))}}
	a.ResetUserTOTP(c)
	assertCode(t, w, http.StatusOK)
	if err := a.DB.First(&got, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.TOTPBound() {
		t.Fatal("expected totp cleared")
	}

	// 重置后旧 verify pending 不可用
	w, c = ajaxForm(http.MethodPost, "/login/totp", url.Values{
		"pending": {pending}, "code": {code},
	}.Encode())
	a.LoginTOTP(c)
	assertCode(t, w, http.StatusBadRequest)
}

func TestTOTPTryCapBlocksEvenCorrectCode(t *testing.T) {
	a := securityLoginApp(t)
	putSetting(t, a, keyTOTPEnabled, "1")
	// 锁定关闭：只靠 totpTryCap
	putSetting(t, a, keyLoginMaxFails, "0")
	u := seedLoginUser(t, a, "capuser", "secret1")

	captchaOK(t, "cid-cap", "5555")
	w, c := ajaxForm(http.MethodPost, "/login", url.Values{
		"username": {"capuser"}, "password": {"secret1"},
		"captcha_id": {"cid-cap"}, "captcha": {"5555"},
	}.Encode())
	a.Login(c)
	m := decodeJSON(t, w)
	pending, _ := m["pending"].(string)
	secret, _ := m["secret"].(string)

	for i := 0; i < totpTryCap; i++ {
		w, c = ajaxForm(http.MethodPost, "/login/totp", url.Values{
			"pending": {pending}, "code": {"000000"},
		}.Encode())
		a.LoginTOTP(c)
		assertCode(t, w, http.StatusBadRequest)
	}
	good, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w, c = ajaxForm(http.MethodPost, "/login/totp", url.Values{
		"pending": {pending}, "code": {good},
	}.Encode())
	a.LoginTOTP(c)
	assertCode(t, w, http.StatusBadRequest)
	if decodeJSON(t, w)["error"] != msgTOTPTooMany {
		t.Fatalf("want too-many, got %s", w.Body.String())
	}
	var got model.User
	a.DB.First(&got, u.ID)
	if got.TOTPBound() {
		t.Fatal("must not bind after try cap")
	}
}

func TestUpdateUserDoesNotClobberLockOrTOTP(t *testing.T) {
	a := securityLoginApp(t)
	u := seedLoginUser(t, a, "clobber", "secret1")
	until := time.Now().Add(time.Hour)
	sealed, err := a.sealTOTPSecret("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&u).Updates(map[string]any{
		"failed_logins": 3,
		"locked_until":  until,
		"totp_secret":   sealed,
		"totp_epoch":    2,
	}).Error; err != nil {
		t.Fatal(err)
	}

	w, c := ajaxForm(http.MethodPut, "/console/api/users/"+itoa(int(u.ID)), `{"nickname":"n2"}`)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: itoa(int(u.ID))}}
	a.UpdateUser(c)
	assertCode(t, w, http.StatusOK)

	var got model.User
	if err := a.DB.First(&got, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Nickname != "n2" {
		t.Fatalf("nickname=%q", got.Nickname)
	}
	if got.FailedLogins != 3 || got.LockedUntil == nil || !got.TOTPBound() || got.TOTPEpoch != 2 {
		t.Fatalf("security fields clobbered: %+v", got)
	}
}

func TestRegisterRequiresTOTPWhenEnabled(t *testing.T) {
	a := securityLoginApp(t)
	putSetting(t, a, keyTOTPEnabled, "1")
	captchaOK(t, "cid-reg", "6666")
	w, c := ajaxForm(http.MethodPost, "/register", url.Values{
		"username": {"newtotp"}, "password": {"secret1"},
		"captcha_id": {"cid-reg"}, "captcha": {"6666"},
	}.Encode())
	a.Register(c)
	assertCode(t, w, http.StatusOK)
	m := decodeJSON(t, w)
	if m["step"] != "totp_enroll" {
		t.Fatalf("register must enroll TOTP, got %v", m)
	}
	if strings.Contains(w.Header().Get("Set-Cookie"), middleware.CookieSession+"=") {
		t.Fatal("must not issue session before TOTP")
	}
}

func TestSessionWithoutTOTPFlagRejectedWhenEnabled(t *testing.T) {
	a := securityLoginApp(t)
	putSetting(t, a, keyTOTPEnabled, "1")
	u := seedLoginUser(t, a, "flaguser", "secret1")
	legacy := a.Signer.MakeUserToken(u.ID, time.Hour, false)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/console", nil)
	c.Request.AddCookie(&http.Cookie{Name: middleware.CookieSession, Value: legacy})
	h := middleware.RequireAuth(a.DB, a.Signer, func() bool { return a.Settings().TOTPEnabled })
	h(c)
	if !c.IsAborted() {
		t.Fatalf("expected abort, code=%d", w.Code)
	}
	if middleware.CurrentUser(c) != nil {
		t.Fatal("legacy session must not authenticate when TOTP on")
	}
}

func cookieValue(w *httptest.ResponseRecorder, name string) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			// ResponseRecorder 有时留下百分号编码；浏览器/Gin 读 cookie 会解码
			v, err := url.QueryUnescape(c.Value)
			if err != nil {
				return c.Value
			}
			return v
		}
	}
	return ""
}
