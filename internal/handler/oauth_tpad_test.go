package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"doc-share/internal/config"
	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

// fakeTPad 按 think-tpad oauth_server*.go 的行为模拟授权服务器
func fakeTPad(t *testing.T, clientID, clientSecret string) *httptest.Server {
	t.Helper()
	const code, access = "tpad_code_abc", "tpad-access-token"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case tpadTokenPath:
			// think-tpad 只从表单读凭据，不支持 HTTP Basic
			if _, _, ok := r.BasicAuth(); ok {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"缺少 client_id / client_secret / code。"}`))
				return
			}
			_ = r.ParseForm()
			if r.PostForm.Get("client_id") != clientID || r.PostForm.Get("client_secret") != clientSecret {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"client_secret 校验失败。"}`))
				return
			}
			if r.PostForm.Get("code") != code || !strings.HasSuffix(r.PostForm.Get("redirect_uri"), "/auth/tpad/callback") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"授权码无效或已使用。"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"` + access + `","token_type":"Bearer","expires_in":7200,"scope":"basic email"}`))
		case tpadUserinfoPath:
			if r.Header.Get("Authorization") != "Bearer "+access {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sub": "1024", "username": "zhangsan", "nickname": "张三",
				"picture": "/static/uploadfile/avatar.png", "email": "zs@example.com",
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func tpadSettings(base string) *SiteSettings {
	return &SiteSettings{SiteName: "DocShare", RegMethod: "username", OAuthTPadEnabled: true,
		OAuthTPadClientID: "tpad_oauth_1", OAuthTPadClientSecret: "tpad_secret_1", OAuthTPadBaseURL: base}
}

func TestTPadLoginEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := oauthTestApp(t)
	srv := fakeTPad(t, "tpad_oauth_1", "tpad_secret_1")
	defer srv.Close()
	a.setCache = tpadSettings(srv.URL + "/")
	if !a.OAuthEnabled(model.OAuthProviderTPad) || a.oauthViewFlags()["oauthTPad"] != true {
		t.Fatal("tpad 应启用")
	}

	// 1) 发起登录：跳到 Think-TPAD 前端授权页，参数与 think-tpad OAuthAuthorizeView 读取的一致
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "http://docs.local/auth/tpad", nil)
	c.Params = gin.Params{{Key: "provider", Value: model.OAuthProviderTPad}}
	a.OAuthStart(c)
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusFound {
		t.Fatalf("start = %d %s", w.Code, w.Header().Get("Location"))
	}
	q := loc.Query()
	if loc.Scheme+"://"+loc.Host+loc.Path != srv.URL+"/tpad/oauth/authorize" || q.Get("client_id") != "tpad_oauth_1" ||
		q.Get("scope") != "basic email" || q.Get("redirect_uri") != "http://docs.local/auth/tpad/callback" ||
		q.Get("state") == "" || q.Get("access_type") != "" {
		t.Fatalf("authorize url = %s", loc)
	}
	var stateCookie *http.Cookie
	for _, ck := range (&http.Response{Header: w.Header()}).Cookies() {
		if ck.Name == oauthStateCookie {
			stateCookie = ck
		}
	}
	if stateCookie == nil {
		t.Fatal("缺 state cookie")
	}

	// 2) 回调：换票 → 取资料 → 建号 → 签发会话
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	cb := url.Values{"code": {"tpad_code_abc"}, "state": {q.Get("state")}}
	c.Request = httptest.NewRequest(http.MethodGet, "http://docs.local/auth/tpad/callback?"+cb.Encode(), nil)
	c.Request.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: stateCookie.Value})
	c.Params = gin.Params{{Key: "provider", Value: model.OAuthProviderTPad}}
	a.OAuthCallback(c)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/console" {
		t.Fatalf("callback = %d %s body=%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if !strings.Contains(strings.Join(w.Header().Values("Set-Cookie"), ";"), middleware.CookieSession+"=") {
		t.Fatal("应签发登录会话")
	}
	var ident model.OAuthIdentity
	if err := a.DB.Where("provider = ? AND subject = ?", model.OAuthProviderTPad, "1024").First(&ident).Error; err != nil {
		t.Fatalf("应写入 tpad 身份绑定: %v", err)
	}
	var u model.User
	a.DB.First(&u, ident.UserID)
	if u.Username != "zhangsan" || u.Nickname != "张三" || u.Avatar != srv.URL+"/static/uploadfile/avatar.png" {
		t.Fatalf("user = %+v", u)
	}
	if u.Email != "" || u.EmailVerified {
		t.Fatal("Think-TPAD 邮箱未验证，不应写入")
	}
	// 用户列表能识别 tpad 绑定
	if ps := loadUserOAuthProviders(a.DB, []model.User{u}); len(ps[u.ID]) != 1 || ps[u.ID][0] != model.OAuthProviderTPad {
		t.Fatalf("providers = %v", ps)
	}
}

func TestTPadConfigAndRouting(t *testing.T) {
	a := oauthTestApp(t)
	// 缺实例地址：即使开关打开、凭据齐全也不启用
	s := tpadSettings("")
	a.setCache = s
	if a.OAuthEnabled(model.OAuthProviderTPad) {
		t.Fatal("没有 Think-TPAD 地址不应启用")
	}
	if _, err := a.oauth2Config(nil, model.OAuthProviderTPad); err == nil {
		t.Fatal("没有地址时 oauth2Config 应报错")
	}
	// config.yaml 兜底地址
	a.Cfg.Auth.TPad = config.OAuthProviderConfig{BaseURL: "https://tpad.example.com"}
	if !a.OAuthEnabled(model.OAuthProviderTPad) {
		t.Fatal("config 兜底地址应生效")
	}
	cfg, _ := a.oauth2Config(nil, model.OAuthProviderTPad)
	if cfg.Endpoint.TokenURL != "https://tpad.example.com/api/v1/tpad/oauth/token" || tpadBaseFromConfig(cfg) != "https://tpad.example.com" {
		t.Fatalf("endpoint = %+v", cfg.Endpoint)
	}
	// 即使开启了出站代理也直连（自建内网服务）
	s.OAuthProxyEnabled, s.OAuthProxyURL = true, "http://127.0.0.1:7890"
	if c, err := a.oauthHTTPClient(model.OAuthProviderTPad); err != nil || c.Transport != nil {
		t.Fatalf("tpad 应直连: %v", err)
	}
}

func TestTPadBadSecretAndBadToken(t *testing.T) {
	a := oauthTestApp(t)
	srv := fakeTPad(t, "tpad_oauth_1", "right")
	defer srv.Close()
	a.setCache = tpadSettings(srv.URL)
	cfg, _ := a.oauth2Config(nil, model.OAuthProviderTPad)
	cfg.RedirectURL = "http://docs.local/auth/tpad/callback"
	ctx := httptest.NewRequest(http.MethodGet, "/", nil).Context()
	if _, err := defaultFetchTPadUser(ctx, cfg, "tpad_code_abc"); err == nil || !strings.Contains(err.Error(), "tpad token exchange") {
		t.Fatalf("Secret 错误应在换票阶段失败: %v", err)
	}
}

func TestTPadSettingsValidation(t *testing.T) {
	a := oauthTestApp(t)
	a.DB.AutoMigrate(&model.SystemSetting{})
	save := func(extra string) *httptest.ResponseRecorder {
		body := `{"site_name":"DocShare","reg_method":"username","ai_max_creates_per_req":3,"ai_max_creates_per_day":50,"ai_stream_timeout_min":10` + extra + `}`
		return call(a.UpdateSettings, nil, http.MethodPut, "/console/api/settings", nil, body, nil, nil)
	}
	if w := save(`,"oauth_tpad_enabled":true`); w.Code != http.StatusBadRequest {
		t.Fatalf("启用但无地址应拒绝, got %d %s", w.Code, w.Body.String())
	}
	if w := save(`,"oauth_tpad_base_url":"tpad.example.com"`); w.Code != http.StatusBadRequest {
		t.Fatalf("地址缺协议应拒绝, got %d", w.Code)
	}
	w := save(`,"oauth_tpad_enabled":true,"oauth_tpad_base_url":"https://tpad.example.com/","oauth_tpad_client_id":"tpad_oauth_9","oauth_tpad_client_secret":"s9"`)
	assertCode(t, w, http.StatusOK)
	s := a.Settings()
	if !s.OAuthTPadEnabled || s.OAuthTPadBaseURL != "https://tpad.example.com" || s.OAuthTPadClientSecret != "s9" {
		t.Fatalf("settings = %+v", s)
	}
	// Secret 留空保留原值
	assertCode(t, save(`,"oauth_tpad_enabled":true,"oauth_tpad_base_url":"https://tpad.example.com","oauth_tpad_client_id":"tpad_oauth_9"`), http.StatusOK)
	if a.Settings().OAuthTPadClientSecret != "s9" {
		t.Fatal("Secret 留空应保留原值")
	}
}
