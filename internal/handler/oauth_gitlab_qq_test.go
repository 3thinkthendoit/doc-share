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

func TestOAuthGitLabSelfHostedConfig(t *testing.T) {
	a := oauthTestApp(t)
	a.Cfg.Auth.GitLab = config.OAuthProviderConfig{ClientID: "lcid", ClientSecret: "lsec"}
	a.setCache = &SiteSettings{SiteName: "DocShare", RegMethod: "username", OAuthGitLabEnabled: true, OAuthQQEnabled: true}
	if !a.OAuthEnabled(model.OAuthProviderGitLab) {
		t.Fatal("gitlab 应启用")
	}
	if a.OAuthEnabled(model.OAuthProviderQQ) {
		t.Fatal("qq 未配置凭据不应启用")
	}
	cfg, err := a.oauth2Config(nil, model.OAuthProviderGitLab)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint.AuthURL != "https://gitlab.com/oauth/authorize" || gitlabUserURLFromConfig(cfg) != "https://gitlab.com/api/v4/user" {
		t.Fatalf("默认端点 = %+v", cfg.Endpoint)
	}
	// 系统设置的自建地址优先
	a.setCache.OAuthGitLabBaseURL = "https://git.example.com/"
	cfg, _ = a.oauth2Config(nil, model.OAuthProviderGitLab)
	if cfg.Endpoint.TokenURL != "https://git.example.com/oauth/token" || gitlabUserURLFromConfig(cfg) != "https://git.example.com/api/v4/user" {
		t.Fatalf("自建端点 = %+v", cfg.Endpoint)
	}
	if !strings.Contains(cfg.AuthCodeURL("s"), "scope=read_user") {
		t.Fatal("scope 应为 read_user")
	}
}

func TestOAuthGitLabQQDirectEvenWithProxy(t *testing.T) {
	a := oauthTestApp(t)
	a.setCache = &SiteSettings{SiteName: "DocShare", RegMethod: "username", OAuthProxyEnabled: true, OAuthProxyURL: "http://127.0.0.1:7890"}
	// gitlab.com（默认）走代理
	if c, err := a.oauthHTTPClient(model.OAuthProviderGitLab); err != nil || c.Transport == nil {
		t.Fatalf("gitlab.com 应走代理, err=%v", err)
	}
	// 自建 GitLab 与 QQ 直连
	a.setCache.OAuthGitLabBaseURL = "https://git.example.com"
	for _, p := range []string{model.OAuthProviderGitLab, model.OAuthProviderQQ} {
		if c, err := a.oauthHTTPClient(p); err != nil || c.Transport != nil {
			t.Fatalf("%s 应直连, err=%v", p, err)
		}
	}
	for base, want := range map[string]bool{"": true, "https://gitlab.com/": true, "https://www.gitlab.com": true, "https://gitlab.com.evil.io": false, "http://10.0.0.5:8080": false} {
		if got := gitlabIsOfficial(base); got != want {
			t.Errorf("gitlabIsOfficial(%q) = %v", base, got)
		}
	}
	state, _ := a.mintOAuthState(model.OAuthProviderQQ)
	if p, ok := a.parseOAuthState(state); !ok || p != model.OAuthProviderQQ {
		t.Fatal("qq state 应可解析")
	}
	// config.yaml 漏写协议：自建直连、gitlab.com 仍走代理
	a.setCache.OAuthGitLabBaseURL = ""
	a.Cfg.Auth.GitLab.BaseURL = "git.example.com"
	if c, _ := a.oauthHTTPClient(model.OAuthProviderGitLab); c.Transport != nil {
		t.Fatal("config 自建 GitLab（无协议）应直连")
	}
	a.Cfg.Auth.GitLab.BaseURL = "gitlab.com"
	if c, _ := a.oauthHTTPClient(model.OAuthProviderGitLab); c.Transport == nil {
		t.Fatal("config 写 gitlab.com（无协议）应识别为官方并走代理")
	}
}

func TestGitLabBaseNormalize(t *testing.T) {
	cases := map[string]string{
		"":                         "https://gitlab.com",
		"git.example.com/":         "https://git.example.com",
		"http://10.0.0.5:8080/gl/": "http://10.0.0.5:8080/gl",
	}
	for in, want := range cases {
		if got := gitlabBase(in); got != want {
			t.Errorf("gitlabBase(%q) = %q, want %q", in, got, want)
		}
	}
	if got := gitlabUserURLFromConfig(&oauth2.Config{Endpoint: gitlabEndpoint("git.example.com/gl")}); got != "https://git.example.com/gl/api/v4/user" {
		t.Errorf("子路径自建实例的 user 接口 = %q", got)
	}
}

func TestRedactURLError(t *testing.T) {
	// 连接必然失败的端口，拿到真实的 *url.Error
	q := url.Values{}
	q.Set("client_secret", "SUPERSECRET")
	q.Set("access_token", "tok-xyz")
	_, err := (&http.Client{}).Get("http://127.0.0.1:1/oauth2.0/token?" + q.Encode())
	if err == nil {
		t.Skip("端口 1 意外可连")
	}
	if !strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("前提：原始错误应含查询串, got %v", err)
	}
	red := redactURLError(err).Error()
	if strings.Contains(red, "SUPERSECRET") || strings.Contains(red, "tok-xyz") {
		t.Fatalf("脱敏失败: %s", red)
	}
	if !strings.Contains(red, "127.0.0.1:1/oauth2.0/token") {
		t.Fatalf("应保留主机与路径便于排查: %s", red)
	}
}

func TestFetchGitLabUserBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"gl-tok","token_type":"Bearer"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":9,"username":"x","state":"blocked"}`))
	}))
	defer srv.Close()
	cfg := &oauth2.Config{ClientID: "c", Endpoint: gitlabEndpoint(srv.URL)}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, srv.Client())
	if _, err := defaultFetchGitLabUser(ctx, cfg, "c"); err == nil || !strings.Contains(err.Error(), "gitlab account inactive") {
		t.Fatalf("封禁账号应拒绝, err=%v", err)
	}
}

func TestFetchGitLabUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"gl-tok","token_type":"Bearer"}`))
		case "/api/v4/user":
			if r.Header.Get("Authorization") != "Bearer gl-tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 77, "username": "tanuki", "name": "Tanuki", "email": "t@example.com",
				"avatar_url": "https://git.example.com/a.png", "state": "active",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := &oauth2.Config{ClientID: "lcid", ClientSecret: "lsec", RedirectURL: "http://localhost/auth/gitlab/callback", Endpoint: gitlabEndpoint(srv.URL)}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, srv.Client())
	p, err := defaultFetchGitLabUser(ctx, cfg, "c")
	if err != nil {
		t.Fatal(err)
	}
	if p.Subject != "77" || p.Login != "tanuki" || p.Nickname != "Tanuki" || p.EmailVerified {
		t.Fatalf("profile = %+v", p)
	}
}

func TestFetchQQUser(t *testing.T) {
	var gotTokenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2.0/token":
			gotTokenQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"access_token":"qq-tok","expires_in":"7776000","refresh_token":"r"}`))
		case "/oauth2.0/me":
			// 模拟未生效 fmt=json 的 JSONP 返回
			_, _ = w.Write([]byte(`callback( {"client_id":"101","openid":"ABCDEF1234567890ABCDEF1234567890"} );`))
		case "/user/get_user_info":
			if r.URL.Query().Get("oauth_consumer_key") != "101" {
				_, _ = w.Write([]byte(`{"ret":-1,"msg":"bad key"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ret":0,"msg":"","nickname":"企鹅","figureurl_qq_2":"http://thirdqq.qlogo.cn/x/100"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldMe, oldInfo := qqOpenIDURL, qqUserInfoURL
	qqOpenIDURL, qqUserInfoURL = srv.URL+"/oauth2.0/me", srv.URL+"/user/get_user_info"
	defer func() { qqOpenIDURL, qqUserInfoURL = oldMe, oldInfo }()

	cfg := &oauth2.Config{
		ClientID: "101", ClientSecret: "qkey", RedirectURL: "http://localhost/auth/qq/callback",
		Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/oauth2.0/authorize", TokenURL: srv.URL + "/oauth2.0/token"},
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, srv.Client())
	p, err := defaultFetchQQUser(ctx, cfg, "qcode")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotTokenQuery, "fmt=json") || !strings.Contains(gotTokenQuery, "client_secret=qkey") {
		t.Fatalf("token query = %s", gotTokenQuery)
	}
	if p.Subject != "ABCDEF1234567890ABCDEF1234567890" || p.Nickname != "企鹅" || p.Login != "qq_ABCDEF12" {
		t.Fatalf("profile = %+v", p)
	}
	if p.Avatar != "https://thirdqq.qlogo.cn/x/100" {
		t.Fatalf("头像应升级为 https: %q", p.Avatar)
	}

	// 新建账号：中文昵称保留、用户名取 openid 前缀
	a := oauthTestApp(t)
	u, err := a.resolveOAuthUser(model.OAuthProviderQQ, p)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "qq_ABCDEF12" || u.Nickname != "企鹅" || u.Email != "" {
		t.Fatalf("user = %+v", u)
	}

	// openid 所属 APP 与配置不符：拒绝（令牌替换）
	cfg.ClientID = "202"
	if _, err := defaultFetchQQUser(ctx, cfg, "qcode"); err == nil || !strings.Contains(err.Error(), "client_id mismatch") {
		t.Fatalf("应拒绝他 APP 令牌, err=%v", err)
	}
}

func TestFetchQQUserTokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":100019,"error_description":"code to access token error"}`))
	}))
	defer srv.Close()
	cfg := &oauth2.Config{ClientID: "101", ClientSecret: "k", Endpoint: oauth2.Endpoint{TokenURL: srv.URL + "/oauth2.0/token"}}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, srv.Client())
	_, err := defaultFetchQQUser(ctx, cfg, "bad")
	if err == nil || !strings.Contains(err.Error(), "qq token exchange") || !strings.Contains(err.Error(), "100019") {
		t.Fatalf("err = %v", err)
	}
}
