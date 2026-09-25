package storage

import "testing"

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
