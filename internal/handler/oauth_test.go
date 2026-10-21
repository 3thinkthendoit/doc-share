package handler

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"doc-share/internal/config"
	"doc-share/internal/model"
	"doc-share/internal/session"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func oauthTestApp(t *testing.T) *App {
	t.Helper()
	a, _ := newAuthApp(t)
	a.Cfg = &config.Config{}
	a.Signer = session.NewSigner("test-secret-key-at-least-32-bytes!!")
	if err := a.DB.AutoMigrate(&model.OAuthIdentity{}); err != nil {
		t.Fatalf("migrate oauth: %v", err)
	}
	return a
}

func TestResolveOAuthUserCreateAndMerge(t *testing.T) {
	a := oauthTestApp(t)

	// 新建：无邮箱合并目标
	u1, err := a.resolveOAuthUser(model.OAuthProviderGitHub, oauthProfile{
		Subject: "1001", Email: "new@example.com", EmailVerified: true,
		Login: "octo", Nickname: "Octo", Avatar: "https://example.com/a.png",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u1.Username == "" || u1.Email != "new@example.com" {
		t.Fatalf("unexpected user: %+v", u1)
	}
	var n int64
	if err := a.DB.Model(&model.OAuthIdentity{}).Where("provider = ? AND subject = ?", model.OAuthProviderGitHub, "1001").Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("identity count=%d err=%v", n, err)
	}

	// 再次同一 subject：复用
	u1b, err := a.resolveOAuthUser(model.OAuthProviderGitHub, oauthProfile{
		Subject: "1001", Email: "new@example.com", EmailVerified: true, Login: "octo",
	})
	if err != nil || u1b.ID != u1.ID {
		t.Fatalf("reuse: err=%v id=%d want=%d", err, u1b.ID, u1.ID)
	}

	// 按邮箱合并到已有本地账号（须本地邮箱已验证）
	hash, _ := bcrypt.GenerateFromPassword([]byte("password1"), bcrypt.MinCost)
	local := model.User{
		Username: "localuser", PasswordHash: string(hash), Nickname: "Local",
		Email: "merge@example.com", EmailVerified: true,
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&local).Error; err != nil {
		t.Fatalf("seed local: %v", err)
	}
	u2, err := a.resolveOAuthUser(model.OAuthProviderGitHub, oauthProfile{
		Subject: "2002", Email: "Merge@Example.com", EmailVerified: true, Login: "other",
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if u2.ID != local.ID {
		t.Fatalf("expected merge into local id=%d got=%d", local.ID, u2.ID)
	}
	var linked model.OAuthIdentity
	if err := a.DB.Where("provider = ? AND subject = ?", model.OAuthProviderGitHub, "2002").First(&linked).Error; err != nil {
		t.Fatalf("link: %v", err)
	}
	if linked.UserID != local.ID {
		t.Fatalf("linked user_id=%d", linked.UserID)
	}
}

func TestResolveOAuthUserUnverifiedEmailNoMerge(t *testing.T) {
	a := oauthTestApp(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("password1"), bcrypt.MinCost)
	local := model.User{
		Username: "local2", PasswordHash: string(hash), Email: "only@example.com", EmailVerified: true,
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&local).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	u, err := a.resolveOAuthUser(model.OAuthProviderGoogle, oauthProfile{
		Subject: "g-sub", Email: "only@example.com", EmailVerified: false, Login: "only",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == local.ID {
		t.Fatal("unverified provider email must not merge")
	}
	if u.Email != "" {
		t.Fatalf("unverified email should not be stored on new user, got %q", u.Email)
	}
}

func TestResolveOAuthUserLocalUnverifiedRefuse(t *testing.T) {
	a := oauthTestApp(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("password1"), bcrypt.MinCost)
	local := model.User{
		Username: "local3", PasswordHash: string(hash), Email: "grab@example.com", EmailVerified: false,
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&local).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := a.resolveOAuthUser(model.OAuthProviderGitHub, oauthProfile{
		Subject: "g3", Email: "grab@example.com", EmailVerified: true, Login: "grabber",
	})
	if err == nil {
		t.Fatal("expected refuse when local email unverified")
	}
}

func TestResolveOAuthUserDisabledNoLink(t *testing.T) {
	a := oauthTestApp(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("password1"), bcrypt.MinCost)
	local := model.User{
		Username: "dis1", PasswordHash: string(hash), Email: "dis@example.com", EmailVerified: true,
		Role: model.RoleViewer, Status: model.StatusEnabled,
	}
	if err := a.DB.Create(&local).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Status=0 是零值，Create 可能被 GORM 跳过；显式 Update 禁用
	if err := a.DB.Model(&local).Update("status", model.StatusDisabled).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}
	_, err := a.resolveOAuthUser(model.OAuthProviderGitHub, oauthProfile{
		Subject: "dis-sub", Email: "dis@example.com", EmailVerified: true, Login: "dis",
	})
	if err == nil {
		t.Fatal("expected refuse for disabled account")
	}
	var n int64
	a.DB.Model(&model.OAuthIdentity{}).Where("subject = ?", "dis-sub").Count(&n)
	if n != 0 {
		t.Fatal("must not link disabled account")
	}
}

func TestResolveOAuthUserBoundNoUnverifiedEmailBackfill(t *testing.T) {
	a := oauthTestApp(t)
	u, err := a.resolveOAuthUser(model.OAuthProviderGitHub, oauthProfile{
		Subject: "bf1", Email: "", EmailVerified: false, Login: "nofill",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	u2, err := a.resolveOAuthUser(model.OAuthProviderGitHub, oauthProfile{
		Subject: "bf1", Email: "sneaky@example.com", EmailVerified: false, Login: "nofill",
	})
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if u2.ID != u.ID {
		t.Fatalf("id mismatch")
	}
	if u2.Email != "" {
		t.Fatalf("unverified backfill must not set email, got %q", u2.Email)
	}
	// 已验证可回填
	u3, err := a.resolveOAuthUser(model.OAuthProviderGitHub, oauthProfile{
		Subject: "bf1", Email: "Ok@Example.com", EmailVerified: true, Login: "nofill",
	})
	if err != nil || u3.Email != "ok@example.com" {
		t.Fatalf("verified backfill: err=%v email=%q", err, u3.Email)
	}
}

func TestOAuthEnabledRequiresConfigAndToggle(t *testing.T) {
	a := oauthTestApp(t)
	if a.OAuthEnabled(model.OAuthProviderGitHub) {
		t.Fatal("empty config should be disabled")
	}
	a.Cfg.Auth.GitHub.ClientID = "cid"
	a.Cfg.Auth.GitHub.ClientSecret = "sec"
	if a.OAuthEnabled(model.OAuthProviderGitHub) {
		t.Fatal("configured but toggle off → disabled")
	}
	a.setCache = &SiteSettings{OAuthGitHubEnabled: true, SiteName: "DocShare", RegMethod: "username"}
	if !a.OAuthEnabled(model.OAuthProviderGitHub) {
		t.Fatal("want enabled via config.yaml")
	}
	// 系统设置凭据优先于空 config
	a.Cfg = &config.Config{}
	a.setCache = &SiteSettings{
		SiteName: "DocShare", RegMethod: "username",
		OAuthGitHubEnabled: true,
		OAuthGitHubClientID: "ui-cid", OAuthGitHubClientSecret: "ui-sec",
	}
	if !a.OAuthConfigured(model.OAuthProviderGitHub) || !a.OAuthEnabled(model.OAuthProviderGitHub) {
		t.Fatal("want enabled via system settings")
	}
	pcfg, ok := a.oauthProviderCfg(model.OAuthProviderGitHub)
	if !ok || pcfg.ClientID != "ui-cid" || pcfg.ClientSecret != "ui-sec" {
		t.Fatalf("settings should win: %+v", pcfg)
	}
}

func TestSanitizeOAuthUsername(t *testing.T) {
	if got := sanitizeOAuthUsername("Octo-Cat!"); got != "Octo-Cat" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeOAuthUsername("@@@"); got != "user" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeOAuthRedirectURL(t *testing.T) {
	if got, err := normalizeOAuthRedirectURL(""); err != nil || got != "" {
		t.Fatalf("empty: %q %v", got, err)
	}
	got, err := normalizeOAuthRedirectURL("https://docs.example.com/auth/github/callback/")
	if err != nil || got != "https://docs.example.com/auth/github/callback" {
		t.Fatalf("ok: %q %v", got, err)
	}
	if _, err := normalizeOAuthRedirectURL("javascript:alert(1)"); err == nil {
		t.Fatal("want reject javascript")
	}
	if _, err := normalizeOAuthRedirectURL("/relative"); err == nil {
		t.Fatal("want reject relative")
	}
}

func TestOAuthRedirectURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := oauthTestApp(t)
	ctx := func(rawURL string, fwdProto string) *gin.Context {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		req := httptest.NewRequest(http.MethodGet, rawURL, nil)
		if fwdProto != "" {
			req.Header.Set("X-Forwarded-Proto", fwdProto)
		}
		c.Request = req
		return c
	}

	// 无显式回调：跟当前请求
	c := ctx("http://localhost:9000/login", "")
	got := a.oauthRedirectURL(c, model.OAuthProviderGitHub, config.OAuthProviderConfig{})
	want := "http://localhost:9000/auth/github/callback"
	if got != want {
		t.Fatalf("from request: got %q want %q", got, want)
	}

	// 同 Host、协议不一致：回退到请求 Origin（修 https 配置 + http 访问）
	c = ctx("http://localhost:9000/login", "")
	got = a.oauthRedirectURL(c, model.OAuthProviderGitHub, config.OAuthProviderConfig{
		RedirectURL: "https://localhost:9000/auth/github/callback",
	})
	if got != want {
		t.Fatalf("scheme mismatch: got %q want %q", got, want)
	}

	// 不同 Host：保留显式（反代对外域名）
	c = ctx("http://127.0.0.1:9000/login", "")
	pub := "https://docs.example.com/auth/github/callback"
	got = a.oauthRedirectURL(c, model.OAuthProviderGitHub, config.OAuthProviderConfig{RedirectURL: pub})
	if got != pub {
		t.Fatalf("different host keep explicit: got %q", got)
	}

	// X-Forwarded-Proto=https
	c = ctx("http://docs.example.com/login", "https")
	got = a.oauthRedirectURL(c, model.OAuthProviderGoogle, config.OAuthProviderConfig{})
	if got != "https://docs.example.com/auth/google/callback" {
		t.Fatalf("forwarded proto: got %q", got)
	}
}

func TestOAuthSecretKeepOnEmpty(t *testing.T) {
	a := oauthTestApp(t)
	if err := a.DB.Create(&model.SystemSetting{Key: keyOAuthGitHubClientSecret, Value: "keep-me"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	a.setCache = nil
	s := a.Settings()
	if s.OAuthGitHubClientSecret != "keep-me" {
		t.Fatalf("got %q", s.OAuthGitHubClientSecret)
	}
}

func TestParseGoogleIDToken(t *testing.T) {
	clientID := "123.apps.googleusercontent.com"
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	prev := lookupGoogleJWK
	lookupGoogleJWK = func(ctx context.Context, kid string) (*rsa.PublicKey, error) {
		if kid != "test-kid" {
			return nil, errors.New("unknown kid")
		}
		return &priv.PublicKey, nil
	}
	t.Cleanup(func() { lookupGoogleJWK = prev })

	mkToken := func(claims map[string]any) string {
		t.Helper()
		hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-kid"})
		body, _ := json.Marshal(claims)
		hSeg := base64.RawURLEncoding.EncodeToString(hdr)
		pSeg := base64.RawURLEncoding.EncodeToString(body)
		sum := sha256.Sum256([]byte(hSeg + "." + pSeg))
		sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return hSeg + "." + pSeg + "." + base64.RawURLEncoding.EncodeToString(sig)
	}

	token := mkToken(map[string]any{
		"iss": "https://accounts.google.com",
		"aud": clientID,
		"sub": "google-sub-9",
		"email": "user@gmail.com",
		"email_verified": "true",
		"name": "User",
		"picture": "https://example.com/p.png",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	p, err := parseGoogleIDToken(context.Background(), token, clientID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Subject != "google-sub-9" || p.Email != "user@gmail.com" || !p.EmailVerified || p.Login != "user" {
		t.Fatalf("profile=%+v", p)
	}
	if _, err := parseGoogleIDToken(context.Background(), token, "other-client"); err == nil {
		t.Fatal("aud mismatch must fail")
	}

	noExp := mkToken(map[string]any{
		"iss": "https://accounts.google.com", "aud": clientID, "sub": "x",
	})
	if _, err := parseGoogleIDToken(context.Background(), noExp, clientID); err == nil {
		t.Fatal("missing exp must fail")
	}

	badSig := token[:len(token)-4] + "dead"
	if _, err := parseGoogleIDToken(context.Background(), badSig, clientID); err == nil {
		t.Fatal("bad signature must fail")
	}
}

func TestOAuthFlexBool(t *testing.T) {
	var b oauthFlexBool
	for _, raw := range []string{`true`, `"true"`, `1`} {
		if err := json.Unmarshal([]byte(raw), &b); err != nil || !bool(b) {
			t.Fatalf("true case %s: err=%v v=%v", raw, err, b)
		}
	}
	for _, raw := range []string{`false`, `"false"`, `0`, `null`} {
		if err := json.Unmarshal([]byte(raw), &b); err != nil || bool(b) {
			t.Fatalf("false case %s: err=%v v=%v", raw, err, b)
		}
	}
}

func TestOAuthStateRoundTrip(t *testing.T) {
	a := oauthTestApp(t)
	state, err := a.mintOAuthState(model.OAuthProviderGitHub)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	p, ok := a.parseOAuthState(state)
	if !ok || p != model.OAuthProviderGitHub {
		t.Fatalf("parse: ok=%v provider=%q", ok, p)
	}
	if _, ok := a.parseOAuthState("tampered." + state); ok {
		t.Fatal("tampered state must fail")
	}
	if _, ok := a.parseOAuthState(""); ok {
		t.Fatal("empty state")
	}
	// 另一 Signer 无法校验
	a2 := &App{Signer: session.NewSigner("other-secret-key-at-least-32-bytes!")}
	if _, ok := a2.parseOAuthState(state); ok {
		t.Fatal("foreign signer must reject")
	}
}
