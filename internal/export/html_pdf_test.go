package export

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExportHTMLBasic(t *testing.T) {
	if _, err := resolveFonts(); err != nil {
		t.Skip("no CJK font:", err)
	}
	html := `<!DOCTYPE html><html><head><meta charset="utf-8"><title>测</title>
<style>h1{font-size:18pt} .q{margin:8px 0}</style></head>
<body><h1>一年级测验</h1><div class="q">1. 1+1=?</div><script>alert(1)</script></body></html>`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pdf, err := ExportHTML(ctx, html, "一年级测验", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pdf) < 100 || !strings.HasPrefix(string(pdf[:4]), "%PDF") {
		t.Fatalf("not a pdf: len=%d prefix=%q", len(pdf), string(pdf[:min(8, len(pdf))]))
	}
}

func TestWriteTempFileAndExportWithBase(t *testing.T) {
	if _, err := resolveFonts(); err != nil {
		t.Skip("no CJK font:", err)
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	if _, err := resolveFonts(); err != nil {
		t.Skip("no CJK font:", err)
	}
	// 取消后应仍跑完转换（不泄漏槽语义由 runWithContext 保证）；此处只验证不 panic 且返回 ctx 错误或成功。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ExportHTML(ctx, `<html><body>x</body></html>`, "t", "")
	if err == nil {
		t.Fatal("expected error on cancelled ctx")
	}
}
