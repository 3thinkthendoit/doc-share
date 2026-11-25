package proxy

import (
	"encoding/json"
	"testing"
	"time"
)

// sampleVLESS 中 pbk 是格式合法（base64url/32 字节）的占位值，仅用于解析与配置形状测试。
const sampleVLESS = "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@203.0.113.10:8443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.example.com&fp=chrome&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=d7ceffe8b55790a0&type=tcp#HK-Reality"

func TestParseVLESSRealityVision(t *testing.T) {
	node, err := ParseVLESS(sampleVLESS)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if node.UUID != "51f41936-48e7-4bb5-b34c-55ea127b7afd" {
		t.Errorf("uuid = %q", node.UUID)
	}
	if node.Host != "203.0.113.10" {
		t.Errorf("host = %q", node.Host)
	}
	if node.Port != 8443 {
		t.Errorf("port = %d", node.Port)
	}
	if node.Security != "reality" || node.Flow != "xtls-rprx-vision" {
		t.Errorf("security=%q flow=%q", node.Security, node.Flow)
	}
	if node.SNI != "www.example.com" || node.PublicKey != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" || node.ShortID != "d7ceffe8b55790a0" {
		t.Errorf("sni=%q pbk=%q sid=%q", node.SNI, node.PublicKey, node.ShortID)
	}
	if got := node.ServerPreview(); got != "203.0.113.10:8443" {
		t.Errorf("preview = %q", got)
	}
}

func TestParseVLESSDefaults(t *testing.T) {
	node, err := ParseVLESS("vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@node.example.com:443?security=tls")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if node.SNI != "node.example.com" {
		t.Errorf("sni 应回落到服务器域名，got %q", node.SNI)
	}
	if node.Fingerprint != "chrome" {
		t.Errorf("fp 默认 chrome，got %q", node.Fingerprint)
	}
}

func TestParseVLESSRejections(t *testing.T) {
	cases := map[string]string{
		"空链接":         "",
		"协议非vless":    "trojan://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com:443",
		"缺UUID":       "vless://@a.com:443",
		"UUID非法":      "vless://not-a-uuid@a.com:443",
		"缺端口":         "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com",
		"端口非法":        "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com:99999",
		"ws传输":        "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com:443?type=ws",
		"未知安全层":       "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com:443?security=quic",
		"未知流控":        "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com:443?security=tls&flow=xtls-rprx-splice",
		"流控缺安全层":      "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com:443?flow=xtls-rprx-vision",
		"reality缺pbk": "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com:443?security=reality",
		"pbk格式非法":     "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com:443?security=reality&pbk=testpublickey",
		"sid非十六进制":    "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com:443?security=reality&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=zzzz",
		"sid过长":       "vless://51f41936-48e7-4bb5-b34c-55ea127b7afd@a.com:443?security=reality&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=0123456789abcdef00",
	}
	for name, raw := range cases {
		if _, err := ParseVLESS(raw); err == nil {
			t.Errorf("%s：应拒绝但未拒绝", name)
		}
	}
}

func TestBuildConfigShape(t *testing.T) {
	node, err := ParseVLESS(sampleVLESS)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	raw, err := buildConfig(node)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("配置不是合法 JSON: %v", err)
	}
	if _, hasInbound := cfg["inbounds"]; hasInbound {
		t.Error("内嵌拨号模式不应声明入站")
	}
	outbounds, ok := cfg["outbounds"].([]any)
	if !ok || len(outbounds) != 1 {
		t.Fatalf("outbounds 缺失或数量错误")
	}
	ob := outbounds[0].(map[string]any)
	if ob["protocol"] != "vless" {
		t.Errorf("protocol = %v", ob["protocol"])
	}
	stream := ob["streamSettings"].(map[string]any)
	if stream["security"] != "reality" || stream["network"] != "tcp" {
		t.Errorf("stream = %v", stream)
	}
	reality, ok := stream["realitySettings"].(map[string]any)
	if !ok || reality["publicKey"] != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("realitySettings = %v", stream["realitySettings"])
	}
}

func TestManagerDirectWhenEmpty(t *testing.T) {
	m := NewManager()
	client, err := m.HTTPClient("", 5*time.Second)
	if err != nil {
		t.Fatalf("direct: %v", err)
	}
	if client.Transport != nil {
		t.Error("空配置应返回默认直连 client（nil Transport）")
	}
	if _, err := m.HTTPClient("vless://bad", 5*time.Second); err == nil {
		t.Error("非法链接应返回错误")
	}
}

// TestManagerStartsInstanceForSample 验证 buildConfig 产出的 JSON 被 xray 真正接受
// （StartInstance 不发起任何网络拨号，可离线运行）。
func TestManagerStartsInstanceForSample(t *testing.T) {
	m := NewManager()
	defer m.Close()
	client, err := m.HTTPClient(sampleVLESS, 5*time.Second)
	if err != nil {
		t.Fatalf("启动内嵌实例失败: %v", err)
	}
	if client.Transport == nil {
		t.Fatal("代理模式应使用 xray transport")
	}
	// 幂等：相同配置指纹再次获取不重建、不报错。
	if _, err := m.HTTPClient(sampleVLESS, 5*time.Second); err != nil {
		t.Fatalf("重复获取失败: %v", err)
	}
}

func TestIsolatedClientLifecycle(t *testing.T) {
	m := NewManager()
	defer m.Close()
	client, closeInst, err := m.IsolatedClient(sampleVLESS, 5*time.Second)
	if err != nil {
		t.Fatalf("IsolatedClient: %v", err)
	}
	if client.Transport == nil {
		t.Fatal("IsolatedClient 应使用 xray transport")
	}
	closeInst()
	if _, _, err := m.IsolatedClient("vless://bad", time.Second); err == nil {
		t.Error("非法链接应返回错误")
	}
}
