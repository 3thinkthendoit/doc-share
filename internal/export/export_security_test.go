package export

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAvailable_pdfFailsWithoutFonts(t *testing.T) {
	invalidateFontCache()
	prev := fontSearchDirsFn
	fontSearchDirsFn = func() []string {
		return []string{filepath.Join(t.TempDir(), "empty")}
	}
	t.Cleanup(func() {
		fontSearchDirsFn = prev
		invalidateFontCache()
	})

	if err := Available(FormatPDF); err == nil {
		t.Fatal("expected PDF unavailable without fonts")
	}
}

func TestFontsInDir_rejectsOTFOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "NotoSansSC-Regular.otf"), []byte("not-a-font"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fontsInDir(dir); err == nil {
		t.Fatal("OTF-only dir should be rejected")
	}
}

func TestBlockRemoteImages_stripsHTTP(t *testing.T) {
	md := "a ![x](http://169.254.169.254/meta) b ![y](media/001.png) c"
	out := blockRemoteImagesKeepLocal(md)
	if strings.Contains(out, "169.254") || strings.Contains(strings.ToLower(out), "http") {
		t.Fatalf("remote url leaked: %s", out)
	}
	if !strings.Contains(out, "media/001.png") {
		t.Fatalf("local media should remain: %s", out)
	}
}

func TestResolveUnderRoot_blocksTraversal(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media")
	if err := os.MkdirAll(media, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "ok.png"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolveUnderRoot(root, "media/ok.png"); !ok {
		t.Fatal("legitimate media path should resolve")
	}
	if _, ok := resolveUnderRoot(root, "media/../ok.png"); ok {
		t.Fatal("dot-dot segment should fail")
	}
	if _, ok := resolveUnderRoot(root, "../ok.png"); ok {
		t.Fatal("parent escape should fail")
	}
	if _, ok := resolveUnderRoot(root, "/media/ok.png"); ok {
		t.Fatal("absolute path should fail")
	}
	if _, ok := resolveUnderRoot(root, "media/../../etc/passwd"); ok {
		t.Fatal("nested escape should fail")
	}
}

func TestNormalizeImage_gifToPNG(t *testing.T) {
	var gbuf bytes.Buffer
	pal := image.NewPaletted(image.Rect(0, 0, 4, 4), []color.Color{color.White, color.Black})
	if err := gif.Encode(&gbuf, pal, nil); err != nil {
		t.Fatal(err)
	}
	out, ext, err := normalizeImageBytes(gbuf.Bytes(), ".gif")
	if err != nil {
		t.Fatal(err)
	}
	if ext != ".png" {
		t.Fatalf("ext=%s", ext)
	}
	if _, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("not png: %v", err)
	}
}

func TestLocalizeRemoteImages_blocksLoopbackAndKeepsLocal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	}))
	t.Cleanup(srv.Close)

	work := t.TempDir()
	md := "![ok](" + srv.URL + "/a.png) ![bad](http://169.254.169.254/x) ![local](media/keep.png)"
	out, err := localizeRemoteImages(context.Background(), work, md, []string{"127.0.0.1", "localhost"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "169.254") {
		t.Fatalf("link-local should be stripped: %s", out)
	}
	if strings.Contains(out, srv.URL) {
		t.Fatalf("loopback fetch must not succeed / url must not remain: %s", out)
	}
	if !strings.Contains(out, "media/keep.png") {
		t.Fatalf("local media should remain: %s", out)
	}
}

func TestBlockedIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.169.254", "::1"} {
		if !isBlockedIP(net.ParseIP(s)) {
			t.Fatalf("expected blocked: %s", s)
		}
	}
	if isBlockedIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public IP should not be blocked")
	}
}

func TestExport_stripsSSRFEvenWithBaseURL(t *testing.T) {
	md := "见 ![x](http://169.254.169.254/latest/meta-data/)"
	data, _, err := Export(context.Background(), FormatDOCX, Options{
		Title:    "t",
		Markdown: md,
		BaseURL:  "https://ex.com",
		Timeout:  15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("169.254.169.254")) {
		t.Fatal("ssrf host leaked into docx zip")
	}
}

func TestEmbedLocalMedia_pathSafe(t *testing.T) {
	work := t.TempDir()
	media := filepath.Join(work, "media")
	if err := os.MkdirAll(media, 0o700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	if err := os.WriteFile(filepath.Join(media, "001.png"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out := embedLocalMediaAsDataURI(work, `![a](media/001.png) ![b](media/../001.png)`)
	if !strings.Contains(out, "data:image/png;base64,") {
		t.Fatalf("expected data uri: %s", out[:min(80, len(out))])
	}
	if !strings.Contains(out, "](media/../001.png)") {
		t.Fatalf("traversal image should stay unresolved: %s", out)
	}
}

func TestNeutralizeRemoteImageRefs(t *testing.T) {
	md := "![a][id]\n\n[id]: http://169.254.169.254/x\n[ok]: /uploads/a.png\n"
	out := neutralizeRemoteImageRefs(md)
	if strings.Contains(out, "169.254") {
		t.Fatalf("remote ref should be neutralized: %s", out)
	}
	if !strings.Contains(out, "[id]: #") {
		t.Fatalf("expected # placeholder: %s", out)
	}
	if !strings.Contains(out, "[ok]: /uploads/a.png") {
		t.Fatalf("local ref should remain: %s", out)
	}
}

func TestNormalizeHostSet_ipv6AndPort(t *testing.T) {
	set := normalizeHostSet([]string{
		"https://example.com:8443/path",
		"[2001:db8::1]",
		"http://[2001:db8::2]:443/x",
	})
	if !set["example.com"] {
		t.Fatalf("missing example.com: %#v", set)
	}
	if !set["2001:db8::1"] || !set["2001:db8::2"] {
		t.Fatalf("ipv6 hosts: %#v", set)
	}
}

func TestRunWithContext_waitsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runWithContext(ctx, func() error {
			close(started)
			time.Sleep(80 * time.Millisecond)
			return nil
		})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("want canceled after work finishes, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runWithContext should wait for work then return")
	}
}
