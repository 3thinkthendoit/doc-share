package session

import (
	"testing"
	"time"
)

// TestShareTokenRoundTrip 验证签名往返：token 段不含冒号时必须可验证，
// 含冒号的旧形式（adminRawPayload 曾用 "doc:<id>"）会因 SplitN 取首段而失效——这是回归护栏。
func TestShareTokenRoundTrip(t *testing.T) {
	s := NewSigner("0123456789abcdef0123456789abcdef")

	// 分享侧：token 不含冒号，正常往返
	sig := s.MakeShareToken("0123abcd0123abcd0123abcd0123abcd", time.Hour)
	if !s.VerifyShareToken(sig, "0123abcd0123abcd0123abcd0123abcd") {
		t.Fatal("share token round trip failed")
	}

	// 管理预览侧：payload "doc9"（不含冒号）必须可验证
	sig2 := s.MakeShareToken("doc9", time.Hour)
	if !s.VerifyShareToken(sig2, "doc9") {
		t.Fatal(`admin raw payload "doc9" round trip failed`)
	}

	// 含冒号的 payload（历史 bug 形式）不得通过：SplitN 首段为 "doc"
	sig3 := s.MakeShareToken("doc:9", time.Hour)
	if s.VerifyShareToken(sig3, "doc:9") {
		t.Fatal(`payload with colon must NOT verify as "doc:9"`)
	}
	if s.VerifyShareToken(sig2, "doc10") {
		t.Fatal("sig must not verify against another payload")
	}
}
