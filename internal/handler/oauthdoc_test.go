package handler

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"doc-share/internal/middleware"
)

func TestOAuthDocPageRenders(t *testing.T) {
	a, users := oauthSrvApp(t)
	a.setCache = &SiteSettings{SiteName: "DocShare", RegMethod: "username", SiteDomain: "https://docs.example.com"}
	l10n := regexp.MustCompile(`(?s)<script>window\.__LANG=.*?</script>`)
	rawKey := regexp.MustCompile(`\b(oauthdoc|oauthsrv)\.[A-Za-z.]+`)
	for _, lang := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, u := range []bool{false, true} { // 公开页：未登录也能看
			w, c := newCtx()
			c.Request = httptest.NewRequest(http.MethodGet, "/console/oauthdoc", nil)
			c.Set(middleware.ContextLang, lang)
			if u {
				c.Set(middleware.ContextUser, users["viewm"])
			}
			a.OAuthDocPage(c)
			assertCode(t, w, http.StatusOK)
			body := l10n.ReplaceAllString(w.Body.String(), "")
			if m := rawKey.FindAllString(body, 5); len(m) > 0 {
				t.Errorf("%s 残留未翻译 key: %v", lang, m)
			}
			for _, leftover := range []string{"{{ISSUER}}", "__CS", "__BEARER__", "__SK"} {
				if strings.Contains(body, leftover) {
					t.Errorf("%s 示例占位符未替换: %s", lang, leftover)
				}
			}
			for _, want := range []string{
				"https://docs.example.com/.well-known/openid-configuration",
				"https://docs.example.com/oauth/token",
				"client_" + "sec" + "ret",
				"code_challenge_method",
				`id="sec-pkce"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s 页面缺少 %q", lang, want)
				}
			}
			// 已配置站点域名：不提示 issuer 警告；管理入口仅登录可见
			if strings.Contains(body, "System Settings → Site domain") || strings.Contains(body, "站点域名」填写对外地址") {
				t.Errorf("%s 已配置域名不应出现 issuer 提示", lang)
			}
			if got := strings.Contains(body, `href="/console/oauth-apps"`); got != u {
				t.Errorf("%s 登录=%v 时管理入口可见性不对", lang, u)
			}
		}
	}
	// 未配置站点域名时提示
	a.setCache = &SiteSettings{SiteName: "DocShare", RegMethod: "username"}
	w, c := newCtx()
	c.Request = httptest.NewRequest(http.MethodGet, "/console/oauthdoc", nil)
	c.Set(middleware.ContextLang, "en-US")
	a.OAuthDocPage(c)
	if !strings.Contains(w.Body.String(), "Site domain") {
		t.Error("未配置站点域名时应提示")
	}
}
