package geoip

import "testing"

func TestLookupPublicIP(t *testing.T) {
	// 已知公网 IP：应能反查出非空归属地（具体文案随 xdb 版本可能变化）
	got := Lookup("114.114.114.114")
	if got == "" {
		t.Fatal("expected non-empty region for 114.114.114.114")
	}
	t.Log(got)
}

func TestLookupPrivateIP(t *testing.T) {
	got := Lookup("192.168.1.1")
	// 内网可能返回「内网」或空（视库数据）；不应 panic
	_ = got
}

func TestFormatRegion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"中国|广东|深圳|电信|CN", "广东"},
		{"0|0|0|内网IP|0", "内网"},
		{"中国|0|0|联通|CN", "中国"},
		{"中国|北京|北京|联通|CN", "北京"},
		{"美国|加利福尼亚|洛杉矶|0|US", "美国 加利福尼亚 洛杉矶"},
		{"", ""},
	}
	for _, c := range cases {
		if got := formatRegion(c.in); got != c.want {
			t.Errorf("formatRegion(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestShortRegion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"中国 广东 深圳", "广东"},
		{"中国 北京", "北京"},
		{"中国", "中国"},
		{"广东", "广东"},
		{"内网", "内网"},
		{"美国 加利福尼亚", "美国 加利福尼亚"},
		{"", ""},
	}
	for _, c := range cases {
		if got := ShortRegion(c.in); got != c.want {
			t.Errorf("ShortRegion(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
