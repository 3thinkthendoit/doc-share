package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeMarkdown_stripsDangerousSchemes(t *testing.T) {
	in := `a ![x](file:///etc/passwd) b <img src="file:///etc/shadow"> c ![d](DATA:text/plain;base64,QQ==)`
	out := SanitizeMarkdown(in)
	if strings.Contains(strings.ToLower(out), "file:") {
		t.Fatalf("file: still present: %s", out)
	}
	if strings.Contains(strings.ToLower(out), "data:") {
		t.Fatalf("data: still present: %s", out)
	}
}

func TestPrepareMarkdown_BaseURL(t *testing.T) {
	md := `见 ![a](/uploads/2026/x.png) 与 [s](/static/logo.svg "Logo")`
	out := PrepareMarkdown(md, "https://ex.com", "")
	if !strings.Contains(out, "](https://ex.com/uploads/2026/x.png)") {
		t.Fatalf("uploads not absolutized: %s", out)
	}
	if !strings.Contains(out, "](https://ex.com/static/logo.svg \"Logo\")") {
		t.Fatalf("static titled link not absolutized: %s", out)
	}
}

func TestPrepareMarkdown_noFileURL(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Join("2026", "a.png")
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// PrepareMarkdown 不再产出 file://（避免绕过）
	out := PrepareMarkdown(`![a](/uploads/2026/a.png)`, "https://ex.com", dir)
	if strings.Contains(out, "file:") {
		t.Fatalf("must not emit file URL: %s", out)
	}
	if !strings.Contains(out, "https://ex.com/uploads/2026/a.png") {
		t.Fatalf("expected https absolutize: %s", out)
	}
}

func TestMaterializeMarkdown_copiesSafeUpload(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(t.TempDir(), "media")
	full := filepath.Join(root, "2026", "a.png")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := MaterializeMarkdown(`![a](/uploads/2026/a.png "图")`, "https://ex.com", root, media)
	if !strings.Contains(out, "](media/001.png \"图\")") {
		t.Fatalf("expected media copy link: %s", out)
	}
	if _, err := os.Stat(filepath.Join(media, "001.png")); err != nil {
		t.Fatalf("media file missing: %v", err)
	}
}

func TestResolveSafeUpload_blocksTraversalAndHTML(t *testing.T) {
	root := t.TempDir()
	// 穿越
	if _, ok := resolveSafeUpload(root, "/uploads/../../etc/passwd"); ok {
		t.Fatal("traversal should be rejected")
	}
	if _, ok := resolveSafeUpload(root, "/uploads/foo/../../../etc/passwd"); ok {
		t.Fatal("nested traversal should be rejected")
	}
	// html/ 旁路
	htmlFile := filepath.Join(root, "html", "9", "x", "index.html")
	if err := os.MkdirAll(filepath.Dir(htmlFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(htmlFile, []byte("<html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolveSafeUpload(root, "/uploads/html/9/x/index.html"); ok {
		t.Fatal("html/ prefix should be rejected")
	}
	// 合法文件
	okFile := filepath.Join(root, "2026", "ok.png")
	if err := os.MkdirAll(filepath.Dir(okFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(okFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if abs, ok := resolveSafeUpload(root, "/uploads/2026/ok.png"); !ok || abs == "" {
		t.Fatal("legitimate upload should resolve")
	}
}

func TestEnsureTitleHeading(t *testing.T) {
	if got := EnsureTitleHeading("body", "T"); got != "# T\n\nbody" {
		t.Fatalf("got %q", got)
	}
	if got := EnsureTitleHeading("# Already\n\nx", "T"); got != "# Already\n\nx" {
		t.Fatalf("should not duplicate: %q", got)
	}
}

func TestSafeFilename(t *testing.T) {
	if SafeFilename("") != "document" {
		t.Fatal("empty")
	}
	if SafeFilename(`a/b:c*.md`) != "a_b_c_.md" {
		t.Fatalf("got %q", SafeFilename(`a/b:c*.md`))
	}
}

func TestSanitizeMetaTitle(t *testing.T) {
	got := SanitizeMetaTitle("hello\nheader-includes: evil\r\nworld")
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Fatalf("newlines remain: %q", got)
	}
	if got != "hello header-includes: evil world" {
		t.Fatalf("got %q", got)
	}
}

func TestTryAcquireRelease(t *testing.T) {
	// 抽空可能被其它测试占用的槽
	for i := 0; i < maxConcurrent+2; i++ {
		Release()
	}
	if !TryAcquire() || !TryAcquire() {
		t.Fatal("should acquire up to maxConcurrent")
	}
	if TryAcquire() {
		t.Fatal("third acquire should fail")
	}
	Release()
	if !TryAcquire() {
		t.Fatal("after release should acquire again")
	}
	Release()
	Release()
}
