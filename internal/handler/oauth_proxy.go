package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"doc-share/internal/model"
	"doc-share/internal/proxy"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

// oauthHTTPTimeout 单次第三方登录服务端出站（换票 + 拉资料）总超时
const oauthHTTPTimeout = 15 * time.Second

// oauthProviderNeedsProxy GitHub / Google / gitlab.com 在国内服务器上常无法直连，经代理出站；
// Gitee、QQ 与自建 GitLab（通常在内网或国内）始终直连。
func (a *App) oauthProviderNeedsProxy(provider string) bool {
	switch provider {
	case model.OAuthProviderGitHub, model.OAuthProviderGoogle:
		return true
	case model.OAuthProviderGitLab:
		pcfg, _ := a.oauthProviderCfg(provider)
		return gitlabIsOfficial(pcfg.BaseURL)
	default:
		return false
	}
}

// proxies 惰性构造代理管理器（测试/手动构造 App 时也不会 nil）
func (a *App) proxies() *proxy.Manager {
	a.proxyOnce.Do(func() {
		if a.proxyMgr == nil {
			a.proxyMgr = proxy.NewManager()
		}
	})
	return a.proxyMgr
}

// oauthProxyURL 当前生效的出站代理地址，空 = 直连。
// 系统设置填写了地址时以其开关为准；未填写时用 config.yaml / DOC_SHARE_OAUTH_PROXY（视为启用）。
func (a *App) oauthProxyURL() string {
	s := a.Settings()
	if u := strings.TrimSpace(s.OAuthProxyURL); u != "" {
		if s.OAuthProxyEnabled {
			return u
		}
		return ""
	}
	if a.Cfg != nil {
		return strings.TrimSpace(a.Cfg.Auth.OAuthProxy)
	}
	return ""
}

// oauthHTTPClient 返回该提供方服务端出站用的 http.Client。
// 代理已启用但配置非法/实例启动失败时返回 error（调用方应拒绝登录，不静默直连）。
func (a *App) oauthHTTPClient(provider string) (*http.Client, error) {
	if !a.oauthProviderNeedsProxy(provider) {
		return &http.Client{Timeout: oauthHTTPTimeout}, nil
	}
	return a.proxies().HTTPClient(a.oauthProxyURL(), oauthHTTPTimeout)
}

// oauthCtxHTTPClient 取 ctx 中注入的 oauth2.HTTPClient（含代理），缺省 http.DefaultClient
func oauthCtxHTTPClient(ctx context.Context) *http.Client {
	if ctx != nil {
		if c, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && c != nil {
			return c
		}
	}
	return http.DefaultClient
}

// redactURLError 去掉 *url.Error 中 URL 的查询串。
// QQ / Gitee 把 client_secret、access_token 放在查询参数里，原始网络错误会把完整 URL 带进日志。
func redactURLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	clean := "<redacted>"
	if p, perr := url.Parse(ue.URL); perr == nil {
		p.RawQuery, p.Fragment, p.User = "", "", nil
		clean = p.String()
	}
	return &url.Error{Op: ue.Op, URL: clean, Err: ue.Err}
}

// oauthProxyPreview 设置页回显用：只显示协议与 host:port，不回传凭据
func (a *App) oauthProxyPreview() string {
	return proxy.Preview(a.Settings().OAuthProxyURL)
}

// TestOAuthProxy POST /console/api/settings/test-oauth-proxy（仅 admin）：
// 用表单里的地址（留空则用已保存/配置文件的地址）实际拨测 GitHub / Google / gitlab.com API。
// gitlab.com 未登录访问 /api/v4/version 返回 401，仍视为连通（<500）。
func (a *App) TestOAuthProxy(c *gin.Context) {
	var req struct {
		URL string `json:"oauth_proxy_url"`
	}
	_ = c.ShouldBind(&req)
	raw := strings.TrimSpace(req.URL)
	if raw == "" {
		raw = strings.TrimSpace(a.Settings().OAuthProxyURL)
	}
	if raw == "" && a.Cfg != nil {
		raw = strings.TrimSpace(a.Cfg.Auth.OAuthProxy)
	}
	if raw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "set.oauthProxyEmpty")})
		return
	}
	if err := proxy.Validate(raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "set.oauthProxyInvalid") + "：" + err.Error()})
		return
	}
	client, closeFn, err := a.proxies().IsolatedClient(raw, oauthHTTPTimeout)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "set.oauthProxyInvalid") + "：" + err.Error()})
		return
	}
	defer closeFn()

	type result struct {
		Name      string `json:"name"`
		OK        bool   `json:"ok"`
		LatencyMs int64  `json:"latency_ms"`
		Error     string `json:"error,omitempty"`
	}
	targets := []struct{ name, url string }{
		{"GitHub", "https://api.github.com/"},
		{"Google", "https://www.googleapis.com/oauth2/v3/certs"},
		{"GitLab", "https://gitlab.com/api/v4/version"},
	}
	results := make([]result, 0, len(targets))
	allOK := true
	for _, t := range targets {
		r := result{Name: t.name}
		ctx, cancel := context.WithTimeout(c.Request.Context(), oauthHTTPTimeout)
		start := time.Now()
		hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, t.url, nil)
		hreq.Header.Set("User-Agent", "DocShare-OAuth")
		res, err := client.Do(hreq)
		r.LatencyMs = time.Since(start).Milliseconds()
		switch {
		case err != nil:
			if errors.Is(err, context.DeadlineExceeded) {
				r.Error = "timeout"
			} else {
				r.Error = err.Error()
			}
		case res.StatusCode >= 500:
			r.Error = res.Status
		default:
			r.OK = true
		}
		if res != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
			res.Body.Close()
		}
		cancel()
		if !r.OK {
			allOK = false
		}
		results = append(results, r)
	}
	c.JSON(http.StatusOK, gin.H{"ok": allOK, "proxy": proxy.Preview(raw), "results": results})
}
