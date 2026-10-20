package export

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireHTMLExportEnv：有 Chrome 直接测主路径；否则有字体时开 LEGACY；否则 Skip。
func requireHTMLExportEnv(t *testing.T) {
	t.Helper()
	if _, err := findChrome(); err == nil {
		return
	}
	if _, err := resolveFonts(); err != nil {
		t.Skip("need Chrome or CJK fonts:", err)
	}
	t.Setenv("DOC_SHARE_HTML_PDF_LEGACY", "1")
	t.Setenv("DOC_SHARE_CHROME_PATH", filepath.Join(t.TempDir(), "missing-chrome"))
}

func TestExportHTMLBasic(t *testing.T) {
	requireHTMLExportEnv(t)
	dir := t.TempDir()
	html := `<!DOCTYPE html><html><head><meta charset="utf-8"><title>测</title>
<style>h1{font-size:18pt} .q{margin:8px 0}</style></head>
<body><h1>一年级测验</h1><div class="q">1. 1+1=?</div><script>alert(1)</script></body></html>`
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pdf, err := ExportHTML(ctx, html, "一年级测验", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pdf) < 100 || !strings.HasPrefix(string(pdf[:4]), "%PDF") {
		t.Fatalf("not a pdf: len=%d prefix=%q", len(pdf), string(pdf[:min(8, len(pdf))]))
	}
}

func TestWriteTempFileAndExportWithBase(t *testing.T) {
	requireHTMLExportEnv(t)
	dir := t.TempDir()
	html := `<!DOCTYPE html><html><head><meta charset="utf-8"></head>
<body><p>带图</p><img src="pic.png" width="10" height="10"></body></html>`
	// 1x1 PNG
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
		0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xfe, 0xd4, 0xef, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	if err := WriteTempFile(dir, "index.html", []byte(html)); err != nil {
		t.Fatal(err)
	}
	if err := WriteTempFile(dir, "pic.png", png); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pdf, err := ExportHTML(ctx, html, "图", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(pdf[:4]), "%PDF") {
		t.Fatal("bad pdf")
	}
}

func TestEnsureHTMLFontFamily(t *testing.T) {
	in := `<html><head><title>x</title></head><body>hi</body></html>`
	out := ensureHTMLFontFamily(in, "NotoSansSC")
	if !strings.Contains(out, "font-family:NotoSansSC") || !strings.Contains(out, "</head>") {
		t.Fatalf("%s", out)
	}
}

func TestEnsureHTMLDocumentTitle(t *testing.T) {
	out := ensureHTMLDocumentTitle(`<html><head></head><body></body></html>`, `测<script>`)
	if !strings.Contains(out, `<title>测&lt;script&gt;</title>`) {
		t.Fatalf("%s", out)
	}
	out = ensureHTMLDocumentTitle(`<html><head><title>old</title></head><body></body></html>`, "new")
	if strings.Contains(out, "old") || !strings.Contains(out, "<title>new</title>") {
		t.Fatalf("%s", out)
	}
}

func TestStripNonBMPRunes(t *testing.T) {
	// U+1F600 😀 = 码点 128512 >= 65536，会触发 fpdf Cw 越界
	in := "你好😀世界"
	out := stripNonBMPRunes(in)
	if strings.ContainsRune(out, 0x1F600) {
		t.Fatal("emoji should be stripped")
	}
	if !strings.Contains(out, "你好") || !strings.Contains(out, "世界") {
		t.Fatalf("BMP text lost: %q", out)
	}
	if !strings.ContainsRune(out, '\uFFFD') {
		t.Fatal("want replacement char")
	}
}

func TestExportHTMLWithEmojiNoPanic(t *testing.T) {
	requireHTMLExportEnv(t)
	dir := t.TempDir()
	// 复现：正文含非 BMP 时旧版会 panic index out of range [65536]
	html := `<!DOCTYPE html><html><head><meta charset="utf-8"></head>
<body><p>测验 😀 完成</p><p>/console/raw/61/doc61:1.abc/index.html</p></body></html>`
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pdf, err := ExportHTML(ctx, html, "emoji", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pdf) < 100 || !strings.HasPrefix(string(pdf[:4]), "%PDF") {
		t.Fatalf("not a pdf: len=%d", len(pdf))
	}
}

func TestSafeSiteRelPath(t *testing.T) {
	ok := []string{"index.html", "css/a.css", "img/x.png"}
	bad := []string{"", ".", "../x", "a/../b", "/abs", `a\b`, "C:x", "a//b", "x\x00y"}
	for _, p := range ok {
		if !SafeSiteRelPath(p) {
			t.Fatalf("want ok: %q", p)
		}
	}
	for _, p := range bad {
		if SafeSiteRelPath(p) {
			t.Fatalf("want reject: %q", p)
		}
	}
}

func TestWriteTempFileRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := WriteTempFile(dir, "../escape.txt", []byte("x")); err == nil {
		t.Fatal("expected reject")
	}
	if err := WriteTempFile(dir, "ok/a.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeHTMLResources_blocksRemoteAndTraversal(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "ok.png"), []byte("x"), 0o644)
	in := `<html><head>
<link rel="stylesheet" href="https://evil.example/x.css">
<link rel="stylesheet" href="style.css">
<style>@import url("https://evil.example/i.css"); body{background:url('../../etc/passwd')}</style>
</head><body>
<img src="http://169.254.169.254/latest">
<img src="ok.png">
<img src="/etc/passwd">
<img src="../../../etc/passwd">
<img src="file:///etc/passwd">
<img srcset="ok.png 1x, https://evil/2.png 2x">
</body></html>`
	out := sanitizeHTMLResources(in, dir)
	if strings.Contains(out, "https://evil") || strings.Contains(out, "http://169.254") ||
		strings.Contains(out, "file:") || strings.Contains(out, "/etc/passwd") ||
		strings.Contains(out, "../") {
		t.Fatalf("dangerous ref survived:\n%s", out)
	}
	if !strings.Contains(out, `src="ok.png"`) {
		t.Fatalf("safe relative img lost:\n%s", out)
	}
	if !strings.Contains(out, `href="style.css"`) {
		t.Fatalf("safe relative css lost:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "@import url") {
		t.Fatalf("@import not blocked:\n%s", out)
	}
}

func TestLocalizeHTMLResources_blocksRemote(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "ok.png"), []byte("x"), 0o644)
	in := `<img src="https://evil.example/x.png"><img src="/console/raw/1/sig/ok.png"><img src="ok.png">`
	out := localizeHTMLResources(in, dir)
	if strings.Contains(out, "https://evil") {
		t.Fatalf("remote survived:\n%s", out)
	}
	if !strings.Contains(out, `src="ok.png"`) {
		t.Fatalf("localized/safe img lost:\n%s", out)
	}
}

func TestSanitizeAllowsDataImage(t *testing.T) {
	in := `<img src="data:image/png;base64,aaaa">`
	out := sanitizeHTMLResources(in, "")
	if !strings.Contains(out, "data:image/png;base64,aaaa") {
		t.Fatalf("%s", out)
	}
	out = sanitizeHTMLResources(`<img src="data:text/html,hi">`, "")
	if strings.Contains(out, "data:text/html") {
		t.Fatalf("non-image data URI must be blocked: %s", out)
	}
}

func TestExportHTML_cancelsStillWaits(t *testing.T) {
	// 取消后应直接返回 ctx 错误。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ExportHTML(ctx, `<html><body>x</body></html>`, "t", t.TempDir())
	if err == nil {
		t.Fatal("expected error on cancelled ctx")
	}
}

func TestLocalizeResourceRef_siteRawPaths(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.png"), []byte("x"), 0o644)
	cases := []struct {
		in, want string
	}{
		{"/console/raw/61/sig/a.png", "a.png"},
		{"/s/tok/raw/sig/a.png", "a.png"},
		{"/ps/p/d/1/raw/sig/a.png", "a.png"},
		{"https://evil.example/x.png", "#"},
		{"a.png", "a.png"},
		{"/other/abs.png", "#"},
	}
	for _, tc := range cases {
		if got := localizeResourceRef(tc.in, dir); got != tc.want {
			t.Fatalf("localizeResourceRef(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestChromeRequestAllowed(t *testing.T) {
	origin := "http://127.0.0.1:12345"
	ok := []string{
		origin + "/index.html",
		origin + "/a.png",
		"data:image/png;base64,aa",
		"blob:http://127.0.0.1:12345/x",
		"about:blank",
	}
	bad := []string{
		"http://127.0.0.1:9999/",
		"http://169.254.169.254/",
		"http://10.0.0.1/",
		"https://evil.example/",
		"file:///etc/passwd",
		"",
	}
	for _, u := range ok {
		if !chromeRequestAllowed(u, origin) {
			t.Fatalf("want allow %q", u)
		}
	}
	for _, u := range bad {
		if chromeRequestAllowed(u, origin) {
			t.Fatalf("want deny %q", u)
		}
	}
}

func TestExportHTMLRequiresBasePath(t *testing.T) {
	_, err := ExportHTML(context.Background(), `<html><body>x</body></html>`, "t", "")
	if err == nil || !strings.Contains(err.Error(), "工作目录") {
		t.Fatalf("want basePath error, got %v", err)
	}
}

func TestExportHTML_noChromeWithoutLegacyFails(t *testing.T) {
	t.Setenv("DOC_SHARE_CHROME_PATH", filepath.Join(t.TempDir(), "missing-chrome"))
	t.Setenv("DOC_SHARE_HTML_PDF_LEGACY", "0")
	_, err := ExportHTML(context.Background(), `<html><body>x</body></html>`, "t", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Chrome") {
		t.Fatalf("want chrome required error, got %v", err)
	}
}

func TestExportHTML_legacyOptIn(t *testing.T) {
	if _, err := resolveFonts(); err != nil {
		t.Skip("no CJK font:", err)
	}
	t.Setenv("DOC_SHARE_CHROME_PATH", filepath.Join(t.TempDir(), "missing-chrome"))
	t.Setenv("DOC_SHARE_HTML_PDF_LEGACY", "1")
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pdf, err := ExportHTML(ctx, `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body><p>legacy</p></body></html>`, "L", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(pdf[:4]), "%PDF") {
		t.Fatal("bad pdf")
	}
}
