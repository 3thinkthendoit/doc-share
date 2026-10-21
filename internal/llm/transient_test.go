package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestIsTransientUpstream(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{io.EOF, true},
		{io.ErrUnexpectedEOF, true},
		{fmt.Errorf("无法连接: dial tcp"), true},
		{errors.New("network error"), true},
		{errors.New("Network Error"), true},
		{errors.New("read tcp: connection reset by peer"), true},
		{errors.New("服务返回 502: bad gateway"), true},
		{errors.New("服务返回 429: rate limit"), true},
		{errors.New("服务返回 400: bad request"), false},
		{errors.New("服务返回 401: unauthorized"), false},
		{errors.New("html 须为完整页面"), false},
		{fmt.Errorf("wrap: %w", context.Canceled), false},
		{fmt.Errorf("wrap: %w", context.DeadlineExceeded), false},
		{errors.New("i/o timeout"), true},
	}
	for _, tc := range cases {
		got := IsTransientUpstream(tc.err)
		if got != tc.want {
			t.Fatalf("IsTransientUpstream(%v)=%v want %v", tc.err, got, tc.want)
		}
	}
}
