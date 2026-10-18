package webfetch

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestHTMLTextSkipsScript(t *testing.T) {
	title, text := HTMLText([]byte(`<!doctype html><html><head><title>周报</title>
<script>secret()</script><style>.x{}</style></head>
<body><h1>标题</h1><p>第一段</p><p>第二段</p></body></html>`))
	if title != "周报" {
		t.Fatalf("title=%q", title)
	}
	if text == "" || strings.Contains(text, "secret") {
		t.Fatalf("text=%q", text)
	}
	if !strings.Contains(text, "第一段") || !strings.Contains(text, "第二段") {
		t.Fatalf("text=%q", text)
	}
}

func TestParsePublicURLRejectsPrivate(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1/a",
		"http://localhost/a",
		"http://10.1.2.3/a",
		"http://169.254.169.254/latest",
		"http://0.0.0.1/",
		"http://100.64.0.1/",
		"http://192.0.2.1/",
		"http://[::1]/",
		"file:///etc/passwd",
		"http://user:pass@example.com/",
	} {
		if _, err := parsePublicURL(raw); err == nil {
			t.Fatalf("expected reject %s", raw)
		}
	}
	if _, err := parsePublicURL("https://example.com/docs"); err != nil {
		t.Fatal(err)
	}
}

func TestBlockedIPExtraRanges(t *testing.T) {
	for _, raw := range []string{"0.0.0.1", "100.64.1.1", "169.254.169.254", "192.0.2.10", "::1", "fc00::1"} {
		if !blockedIP(net.ParseIP(raw)) {
			t.Fatalf("expected block %s", raw)
		}
	}
	if blockedIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("public IP must pass")
	}
}

func TestDisplayURLStripsQuery(t *testing.T) {
	u, err := url.Parse("https://example.com/a?token=secret#frag")
	if err != nil {
		t.Fatal(err)
	}
	if got := displayURL(u); got != "https://example.com/a" {
		t.Fatalf("display=%q", got)
	}
}
