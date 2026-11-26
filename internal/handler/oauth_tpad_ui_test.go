package handler

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"doc-share/internal/middleware"
)

func TestTPadPagesRender(t *testing.T) {
	a, users := oauthSrvApp(t)
	a.setCache = tpadSettings("https://tpad.example.com")
	l10n := regexp.MustCompile(`(?s)<script>window\.__LANG=.*?</script>`)
	rawKey := regexp.MustCompile(`\b(set|login|user)\.(oauthTPad\w*|authTPad)\b`)
	for _, lang := range []string{"zh-CN", "en-US"} {
		// 登录页：出现 Think-TPAD 按钮
		w, c := newCtx()
		c.Request = httptest.NewRequest(http.MethodGet, "/login", nil)
		c.Set(middleware.ContextLang, lang)
		a.LoginPage(c)
		assertCode(t, w, http.StatusOK)
		body := l10n.ReplaceAllString(w.Body.String(), "")
		if !strings.Contains(body, `href="/auth/tpad"`) || !strings.Contains(body, "Think-TPAD") {
			t.Errorf("%s 登录页缺 Think-TPAD 入口", lang)
		}
		if m := rawKey.FindAllString(body, 3); len(m) > 0 {
			t.Errorf("%s 登录页残留 key: %v", lang, m)
		}
		// 系统设置页：Think-TPAD 配置块与实际回调
		w, c = newCtx()
		c.Request = httptest.NewRequest(http.MethodGet, "http://docs.local/console/settings", nil)
		c.Set(middleware.ContextLang, lang)
		c.Set(middleware.ContextUser, users["boss"])
		a.SettingsPage(c)
		assertCode(t, w, http.StatusOK)
		body = l10n.ReplaceAllString(w.Body.String(), "")
		for _, want := range []string{`name="oauth_tpad_base_url"`, `value="https://tpad.example.com"`, "http://docs.local/auth/tpad/callback"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s 设置页缺 %q", lang, want)
			}
		}
		if m := rawKey.FindAllString(body, 3); len(m) > 0 {
			t.Errorf("%s 设置页残留 key: %v", lang, m)
		}
	}
}
