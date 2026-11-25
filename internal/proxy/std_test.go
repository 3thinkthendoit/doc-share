package proxy

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestValidateAndPreview(t *testing.T) {
	ok := map[string]string{
		"":                      "",
		"http://127.0.0.1:7890": "http://127.0.0.1:7890",
		"https://user:pass@proxy.example.com:8443": "https://proxy.example.com:8443",
		"socks5://127.0.0.1:1080":                  "socks5://127.0.0.1:1080",
		"SOCKS5H://127.0.0.1:1080":                 "socks5://127.0.0.1:1080",
		sampleVLESS:                                "vless://203.0.113.10:8443",
	}
	for raw, want := range ok {
		if err := Validate(raw); err != nil {
			t.Errorf("Validate(%q) = %v", raw, err)
		}
		if got := Preview(raw); got != want {
			t.Errorf("Preview(%q) = %q, want %q", raw, got, want)
		}
	}
	bad := []string{
		"ftp://127.0.0.1:21",
		"socks5://127.0.0.1",
		"http://:7890",
		"http://127.0.0.1:99999",
		"vless://not-a-uuid@a.com:443",
	}
	for _, raw := range bad {
		if err := Validate(raw); err == nil {
			t.Errorf("Validate(%q) 应拒绝", raw)
		}
	}
}

func TestPreviewHidesCredentials(t *testing.T) {
	if got := Preview("http://alice:secret@127.0.0.1:7890"); got != "http://127.0.0.1:7890" {
		t.Errorf("preview 泄露凭据: %q", got)
	}
}

func TestStdProxyClientUsesProxy(t *testing.T) {
	m := NewManager()
	c, err := m.HTTPClient("http://127.0.0.1:7890", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("transport 未设置代理")
	}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.com"}}
	pu, err := tr.Proxy(req)
	if err != nil || pu == nil || pu.Host != "127.0.0.1:7890" {
		t.Fatalf("proxy = %v, %v", pu, err)
	}
	direct, err := m.HTTPClient("", 5*time.Second)
	if err != nil || direct.Transport != nil {
		t.Fatalf("空地址应直连: %v", err)
	}
}

func TestManagerReusesAndRetiresTransport(t *testing.T) {
	m := NewManager()
	defer m.Close()
	c1, _ := m.HTTPClient("http://127.0.0.1:7890", time.Second)
	c2, _ := m.HTTPClient("http://127.0.0.1:7890", time.Second)
	if c1.Transport != c2.Transport {
		t.Fatal("相同配置应复用同一 Transport（连接池）")
	}
	c3, _ := m.HTTPClient("socks5://127.0.0.1:1080", time.Second)
	if c3.Transport == c1.Transport {
		t.Fatal("配置变更应换新 Transport")
	}
	// 关闭代理后回收资源
	if _, err := m.HTTPClient("", time.Second); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stdTr != nil || m.inst != nil {
		t.Fatal("关闭代理后应回收 Transport / xray 实例")
	}
}

func TestParseErrorsHideCredentials(t *testing.T) {
	raw := "http://user:p%zzass@127.0.0.1:7890" // 非法转义触发 url.Parse 失败
	err := Validate(raw)
	if err == nil {
		t.Fatal("应报错")
	}
	if strings.Contains(err.Error(), "user") || strings.Contains(err.Error(), "ass") {
		t.Fatalf("错误信息不应回显原始地址: %v", err)
	}
}
