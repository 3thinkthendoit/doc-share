package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"doc-share/internal/config"
	"doc-share/internal/model"

	"golang.org/x/oauth2"
)

func TestOAuthStateAcceptsGitee(t *testing.T) {
	a := oauthTestApp(t)
	state, err := a.mintOAuthState(model.OAuthProviderGitee)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := a.parseOAuthState(state); !ok || p != model.OAuthProviderGitee {
		t.Fatalf("parse gitee state = %q %v", p, ok)
	}
}

func TestOAuthGiteeEnabledAndConfig(t *testing.T) {
	a := oauthTestApp(t)
	a.Cfg.Auth.Gitee = config.OAuthProviderConfig{ClientID: "gcid", ClientSecret: "gsec"}
	if a.OAuthEnabled(model.OAuthProviderGitee) {
		t.Fatal("toggle off → disabled")
	}
	a.setCache = &SiteSettings{SiteName: "DocShare", RegMethod: "username", OAuthGiteeEnabled: true}
	if !a.OAuthEnabled(model.OAuthProviderGitee) {
		t.Fatal("want gitee enabled via config.yaml")
	}
	if f := a.oauthViewFlags(); f["oauthGitee"] != true {
		t.Fatalf("view flags = %v", f)
	}
	cfg, err := a.oauth2Config(nil, model.OAuthProviderGitee)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint.TokenURL != "https://gitee.com/oauth/token" || cfg.Endpoint.AuthStyle != oauth2.AuthStyleInParams {
		t.Fatalf("endpoint = %+v", cfg.Endpoint)
	}
	if u := cfg.AuthCodeURL("s"); !strings.Contains(u, "scope=user_info") || strings.Contains(u, "emails") {
		t.Fatalf("auth url = %s", u)
	}
}

func TestOAuthHTTPClientProxyRouting(t *testing.T) {
	a := oauthTestApp(t)
	base := SiteSettings{SiteName: "DocShare", RegMethod: "username"}

	// 未配置：全部直连
	s := base
	a.setCache = &s
	for _, p := range model.OAuthProviders {
		c, err := a.oauthHTTPClient(p)
		if err != nil || c.Transport != nil {
			t.Fatalf("%s: 未配置代理应直连, err=%v", p, err)
		}
	}

	// 设置页启用 http 代理：GitHub / Google 走代理，Gitee 直连
	s = base
	s.OAuthProxyEnabled = true
	s.OAuthProxyURL = "http://127.0.0.1:7890"
	a.setCache = &s
	for _, p := range []string{model.OAuthProviderGitHub, model.OAuthProviderGoogle} {
		c, err := a.oauthHTTPClient(p)
		if err != nil {
			t.Fatal(err)
		}
		tr, ok := c.Transport.(*http.Transport)
		if !ok || tr.Proxy == nil {
			t.Fatalf("%s 应走代理", p)
		}
		pu, _ := tr.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.com"}})
		if pu == nil || pu.Host != "127.0.0.1:7890" {
			t.Fatalf("%s proxy = %v", p, pu)
		}
	}
	if c, _ := a.oauthHTTPClient(model.OAuthProviderGitee); c.Transport != nil {
		t.Fatal("gitee 应直连")
	}

	// 地址已存但开关关闭：直连
	s.OAuthProxyEnabled = false
	a.setCache = &s
	if c, _ := a.oauthHTTPClient(model.OAuthProviderGitHub); c.Transport != nil {
		t.Fatal("开关关闭应直连")
	}

	// 设置页未填：用 config 兜底（视为启用）
	s = base
	a.setCache = &s
	a.Cfg.Auth.OAuthProxy = "socks5://127.0.0.1:1080"
	if a.oauthProxyURL() != "socks5://127.0.0.1:1080" {
		t.Fatalf("config 兜底 = %q", a.oauthProxyURL())
	}

	// 启用但地址非法：报错而非静默直连
	s.OAuthProxyEnabled = true
	s.OAuthProxyURL = "ftp://bad:21"
	a.setCache = &s
	if _, err := a.oauthHTTPClient(model.OAuthProviderGoogle); err == nil {
		t.Fatal("非法代理应报错")
	}
}

func TestFetchGiteeUser(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_ = r.ParseForm()
			gotForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok-1","token_type":"bearer","expires_in":86400}`))
		case "/api/v5/user":
			if r.URL.Query().Get("access_token") != "tok-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 4242, "login": "gitee-octo", "name": "码云用户",
				"email": "x@example.com", "avatar_url": "https://gitee.com/a.png",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	oldURL := giteeUserURL
	giteeUserURL = srv.URL + "/api/v5/user"
	defer func() { giteeUserURL = oldURL }()

	cfg := &oauth2.Config{
		ClientID: "gcid", ClientSecret: "gsec", RedirectURL: "http://localhost/auth/gitee/callback",
		Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/oauth/authorize", TokenURL: srv.URL + "/oauth/token", AuthStyle: oauth2.AuthStyleInParams},
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, srv.Client())
	p, err := defaultFetchGiteeUser(ctx, cfg, "code-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotForm.Get("client_secret") != "gsec" || gotForm.Get("code") != "code-1" || gotForm.Get("redirect_uri") == "" {
		t.Fatalf("token form = %v", gotForm)
	}
	if p.Subject != "4242" || p.Login != "gitee-octo" || p.Nickname != "码云用户" {
		t.Fatalf("profile = %+v", p)
	}
	if p.EmailVerified {
		t.Fatal("gitee 邮箱不应视为已验证")
	}

	// 未验证邮箱不应合并到已有同邮箱账号
	a := oauthTestApp(t)
	u, err := a.resolveOAuthUser(model.OAuthProviderGitee, p)
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "" || u.EmailVerified {
		t.Fatalf("gitee 新建账号不应写入未验证邮箱: %+v", u)
	}
}
