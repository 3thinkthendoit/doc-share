package handler

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"doc-share/internal/i18n"
)

// TestAuthSplitPageMarkup 锁定登录/注册分栏骨架：无顶栏、欢迎标题、邮箱弹层不在 auth-card 内（避免吃表单 captcha 壳样式）
func TestAuthSplitPageMarkup(t *testing.T) {
	bundle := newBundle(t)
	tmpl, err := ParseTemplates(os.DirFS("../../web/templates"))
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}

	render := func(name string, v View) string {
		t.Helper()
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return buf.String()
	}
	base := func() View {
		return View{
			"Lang": "zh-CN", "Langs": i18n.Supported, "Path": "/login", "Dict": "",
			"SiteName": "DocShare", "SiteLogo": "", viewBundleKey: bundle,
			"title": "欢迎登录 DocShare", "titleSolo": true,
		}
	}

	login := render("login.html", base())
	for _, want := range []string{
		`class="auth-body auth-split"`,
		`class="auth-brand-pane"`,
		`class="auth-form-pane"`,
		`欢迎登录 DocShare`,
	} {
		if !strings.Contains(login, want) {
			t.Fatalf("login.html missing %q", want)
		}
	}
	for _, bad := range []string{`has-site-nav`, `siteNav`, `l-nav`} {
		if strings.Contains(login, bad) {
			t.Fatalf("login.html should not include %q", bad)
		}
	}
	if strings.Contains(login, `· DocShare</title>`) {
		t.Fatal("login title should be titleSolo (no · SiteName suffix)")
	}
	if !strings.Contains(login, `<title>欢迎登录 DocShare</title>`) {
		t.Fatal("login <title> should match welcome heading")
	}

	reg := base()
	reg["Path"] = "/register"
	reg["title"] = "欢迎注册 DocShare"
	reg["method"] = "email"
	reg["username"] = ""
	reg["identifier"] = ""
	out := render("register.html", reg)
	if !strings.Contains(out, `id="mailCaptchaModal"`) {
		t.Fatal("email register should include mailCaptchaModal")
	}
	// 弹层须在 </div> 关闭 auth-card 之后、</main> 之前：卡内不应包住 modal
	cardEnd := strings.Index(out, `class="auth-alt"`)
	modalAt := strings.Index(out, `id="mailCaptchaModal"`)
	mainEnd := strings.Index(out, `</main>`)
	if cardEnd < 0 || modalAt < 0 || mainEnd < 0 {
		t.Fatal("register markup markers missing")
	}
	if !(cardEnd < modalAt && modalAt < mainEnd) {
		t.Fatalf("mailCaptchaModal must sit after auth-card content and inside main (cardEnd=%d modal=%d mainEnd=%d)", cardEnd, modalAt, mainEnd)
	}

	withOAuth := base()
	withOAuth["oauthGitHub"] = true
	withOAuth["oauthGoogle"] = true
	loginOAuth := render("login.html", withOAuth)
	for _, want := range []string{`class="auth-oauth"`, `/auth/github`, `/auth/google`} {
		if !strings.Contains(loginOAuth, want) {
			t.Fatalf("login oauth markup missing %q", want)
		}
	}
	totp := base()
	totp["totpStep"] = "totp"
	totp["oauthGitHub"] = true
	if strings.Contains(render("login.html", totp), `class="auth-oauth"`) {
		t.Fatal("OAuth buttons must be hidden on TOTP step")
	}
}
