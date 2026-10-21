package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
)

// IsTransientUpstream 判断上游错误是否适合静默重试（断连、超时、网关抖动等）。
// 用户取消（context.Canceled）与鉴权/参数类错误不重试。
func IsTransientUpstream(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	// 单轮 StreamTimeout 用尽也会是 DeadlineExceeded；再叠加重试会把用户拖到数十分钟，故不重试。
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}

	s := err.Error()
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "network error"),
		strings.Contains(lower, "timeout"),
		strings.Contains(lower, "timed out"),
		strings.Contains(lower, "i/o timeout"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "broken pipe"),
		strings.Contains(lower, "unexpected eof"),
		strings.Contains(lower, "server closed"),
		strings.Contains(lower, "http2:"),
		strings.Contains(s, "无法连接"):
		return true
	case strings.Contains(s, "服务返回 429"),
		strings.Contains(s, "服务返回 502"),
		strings.Contains(s, "服务返回 503"),
		strings.Contains(s, "服务返回 504"):
		return true
	}
	// 裸 EOF 等短错误（部分 net/http 包装）
	if lower == "eof" || strings.HasSuffix(lower, ": eof") {
		return true
	}
	return false
}
