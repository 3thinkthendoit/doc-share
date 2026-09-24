package handler

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"doc-share/internal/i18n"
)

// TestDumpHomePreview 把官网首页渲染成仓库根的 home-preview.html（静态资源改为相对路径），
// 用于在没有数据库的情况下用浏览器做视觉校验。默认跳过，仅 DUMP_PREVIEW=1 时执行。
func TestDumpHomePreview(t *testing.T) {
	if os.Getenv("DUMP_PREVIEW") == "" {
		t.Skip("set DUMP_PREVIEW=1 to dump the landing page")
	}
	bundle := newBundle(t)
	tmpl, err := ParseTemplates(os.DirFS("../../web/templates"))
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}
	lang := os.Getenv("DUMP_LANG")
	if lang == "" {
		lang = i18n.Default
	}
	a := &App{I18N: bundle, Tmpl: tmpl}
	data := View{
		"user": nil, "Lang": lang, "Langs": i18n.Supported, "Path": "/",
		"Dict": template.JS(a.dictJSON(lang)), viewBundleKey: bundle,
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "home.html", data); err != nil {
		t.Fatalf("render home.html: %v", err)
	}
	html := strings.ReplaceAll(buf.String(), `href="/static/`, `href="web/static/`)
	html = strings.ReplaceAll(html, `src="/static/`, `src="web/static/`)
	out := filepath.Join("..", "..", "home-preview.html")
	if err := os.WriteFile(out, []byte(html), 0o600); err != nil {
		t.Fatalf("write preview: %v", err)
	}
	t.Logf("preview written: %s (%d bytes, lang=%s)", out, len(html), lang)
}
