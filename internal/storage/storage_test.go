package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestObjectURL(t *testing.T) {
	got := ObjectURL("http://localhost:9000/", "docs", "/202609/a.png")
	want := "http://localhost:9000/docs/202609/a.png"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRustFSConfigValidate(t *testing.T) {
	_, err := (RustFSConfig{}).Validate()
	if err == nil {
		t.Fatal("expected error for empty config")
	}
	cfg, err := (RustFSConfig{
		Endpoint:  "http://localhost:9000/",
		AccessKey: "ak",
		SecretKey: "sk",
		Bucket:    "docs",
	}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "http://localhost:9000" {
		t.Fatalf("endpoint not trimmed: %q", cfg.Endpoint)
	}
	if cfg.Region != "us-east-1" {
		t.Fatalf("default region: %q", cfg.Region)
	}
}

func TestValidUploadKey(t *testing.T) {
	ok := []string{
		"202609/abcdefghijklmnop.png",
		"202601/Ab2Cd3Ef4Gh5Ij6K.jpg",
		"202612/x.webp",
		"embed/202609/abcdefghijklmnop.png",
		"embed/202601/Ab2Cd3Ef4Gh5Ij6K.jpg",
	}
	for _, k := range ok {
		if !ValidUploadKey(k) {
			t.Fatalf("want valid: %q", k)
		}
	}
	bad := []string{
		"",
		"../etc/passwd",
		"202609/../x.png",
		"202609//x.png",
		"2026/abc.png",
		"202609/abc.svg",
		"uploads/202609/abc.png",
		"embed/../202609/x.png",
		"embed/2026/x.png",
		"embed//202609/x.png",
	}
	for _, k := range bad {
		if ValidUploadKey(k) {
			t.Fatalf("want invalid: %q", k)
		}
	}
}

func TestValidEmbedUploadKey(t *testing.T) {
	if !ValidEmbedUploadKey("embed/202609/abcdefghijklmnop.png") {
		t.Fatal("embed key should be valid")
	}
	if ValidEmbedUploadKey("202609/abcdefghijklmnop.png") {
		t.Fatal("regular upload key must not be deletable as embed")
	}
	if ValidEmbedUploadKey("embed/../202609/x.png") {
		t.Fatal("traversal must fail")
	}
}

func TestObjectKeyFromURL(t *testing.T) {
	if got := ObjectKeyFromURL("/uploads/202609/abcdefghijklmnop.png", nil); got != "202609/abcdefghijklmnop.png" {
		t.Fatalf("local relative: got %q", got)
	}
	if got := ObjectKeyFromURL("/uploads/embed/202609/abcdefghijklmnop.png", nil); got != "embed/202609/abcdefghijklmnop.png" {
		t.Fatalf("embed relative: got %q", got)
	}
	if got := ObjectKeyFromURL("/uploads/202609/abcdefghijklmnop.png?v=1", nil); got != "202609/abcdefghijklmnop.png" {
		t.Fatalf("local query: got %q", got)
	}
	if got := ObjectKeyFromURL("https://example.com/uploads/202609/abcdefghijklmnop.png", nil); got != "202609/abcdefghijklmnop.png" {
		t.Fatalf("local absolute: got %q", got)
	}
	if got := ObjectKeyFromURL("/uploads/../../../etc/passwd", nil); got != "" {
		t.Fatalf("traversal should fail: %q", got)
	}
	if got := ObjectKeyFromURL("javascript:alert(1)", nil); got != "" {
		t.Fatalf("bad scheme: %q", got)
	}

	rust := &RustFSConfig{
		Endpoint:  "http://localhost:9000",
		AccessKey: "ak",
		SecretKey: "sk",
		Bucket:    "docs",
	}
	u := ObjectURL(rust.Endpoint, rust.Bucket, "202609/abcdefghijklmnop.png")
	if got := ObjectKeyFromURL(u, rust); got != "202609/abcdefghijklmnop.png" {
		t.Fatalf("rustfs url: got %q from %q", got, u)
	}
	eu := ObjectURL(rust.Endpoint, rust.Bucket, "embed/202609/abcdefghijklmnop.png")
	if got := ObjectKeyFromURL(eu, rust); got != "embed/202609/abcdefghijklmnop.png" {
		t.Fatalf("rustfs embed url: got %q from %q", got, eu)
	}
	if got := ObjectKeyFromURL(u, nil); got != "" {
		t.Fatalf("rustfs without cfg should fail: %q", got)
	}
}

func TestLocalDelete(t *testing.T) {
	dir := t.TempDir()
	l := &Local{Dir: dir}
	ctx := context.Background()
	key := "202609/testdeleteslug01.png"
	_, err := l.Put(ctx, key, bytes.NewReader([]byte("\x89PNG\r\n\x1a\nfake")), 12, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filepath.FromSlash(key))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file missing after put: %v", err)
	}
	if err := l.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists after delete")
	}
	if err := l.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := l.Delete(ctx, "../outside.png"); err == nil {
		t.Fatal("expected error for traversal key")
	}
}
