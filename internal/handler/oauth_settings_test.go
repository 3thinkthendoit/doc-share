package handler

import "testing"

func TestNormalizeGitLabBaseURL(t *testing.T) {
	ok := map[string]string{
		"":                            "",
		"https://git.example.com/":    "https://git.example.com",
		"http://10.0.0.5:8080/gitlab": "http://10.0.0.5:8080/gitlab",
		"  https://gitlab.com  ":      "https://gitlab.com",
	}
	for in, want := range ok {
		got, err := normalizeGitLabBaseURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeGitLabBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"git.example.com",                 // 缺协议
		"ftp://git.example.com",           // 非 http(s)
		"https://git.example.com/?a=1",    // 查询串会破坏端点拼接
		"https://git.example.com/#x",      // 锚点
		"https://user:pw@git.example.com", // 内嵌账号
	} {
		if _, err := normalizeGitLabBaseURL(in); err == nil {
			t.Errorf("normalizeGitLabBaseURL(%q) 应拒绝", in)
		}
	}
}
