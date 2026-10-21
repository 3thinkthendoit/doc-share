package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"doc-share/internal/llm"
)

func TestUpstreamRetryBackoff(t *testing.T) {
	if got := upstreamRetryBackoff(0); got != 0 {
		t.Fatalf("attempt 0: %s", got)
	}
	if got := upstreamRetryBackoff(1); got != 400*time.Millisecond {
		t.Fatalf("attempt 1: %s", got)
	}
	if got := upstreamRetryBackoff(2); got != 800*time.Millisecond {
		t.Fatalf("attempt 2: %s", got)
	}
}

func TestStreamChatWithTransientRetryFlakyHTTP(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			// 真实上游路径：首包 502（瞬时），次包正常 SSE
			http.Error(w, `{"error":{"message":"bad gateway"}}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		delta, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{"content": "完整回答"},
			}},
		})
		_, _ = w.Write([]byte("data: " + string(delta) + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	cfg := llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m", StreamTimeout: time.Minute}
	var got strings.Builder
	var resets int
	msg, err := streamChatWithTransientRetry(
		context.Background(),
		aiUpstreamTransientRetries,
		func(int) time.Duration { return 0 },
		func() bool { return got.Len() > 0 },
		func() { got.Reset() },
		func() { resets++ },
		func() (llm.Message, error) {
			return cfg.StreamChat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil,
				func(d string) { got.WriteString(d) })
		},
	)
	if err != nil {
		t.Fatalf("retry should succeed: %v (hits=%d)", err, hits.Load())
	}
	if hits.Load() != 2 {
		t.Fatalf("want 2 upstream calls, got %d", hits.Load())
	}
	if resets != 0 {
		t.Fatalf("no partial text before fail, resets=%d", resets)
	}
	if got.String() != "完整回答" || msg.Content != "完整回答" {
		t.Fatalf("content=%q msg=%q", got.String(), msg.Content)
	}
}

func TestStreamChatWithTransientRetryResetOnPartial(t *testing.T) {
	var calls int
	var got strings.Builder
	var resets int
	msg, err := streamChatWithTransientRetry(
		context.Background(),
		aiUpstreamTransientRetries,
		func(int) time.Duration { return 0 },
		func() bool { return got.Len() > 0 },
		func() { got.Reset() },
		func() { resets++ },
		func() (llm.Message, error) {
			calls++
			if calls == 1 {
				got.WriteString("半句")
				return llm.Message{}, errors.New("network error")
			}
			got.WriteString("完整回答")
			return llm.Message{Role: "assistant", Content: "完整回答"}, nil
		},
	)
	if err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if resets != 1 {
		t.Fatalf("want 1 reset after partial, got %d", resets)
	}
	if got.String() != "完整回答" || msg.Content != "完整回答" {
		t.Fatalf("content=%q msg=%q", got.String(), msg.Content)
	}
}

func TestStreamChatWithTransientRetryNoRetryOn400(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	cfg := llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m", StreamTimeout: time.Minute}
	_, err := streamChatWithTransientRetry(
		context.Background(),
		aiUpstreamTransientRetries,
		func(int) time.Duration { return 0 },
		func() bool { return false },
		func() {},
		func() { t.Fatal("onReset must not run") },
		func() (llm.Message, error) {
			return cfg.StreamChat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, nil)
		},
	)
	if err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != 1 {
		t.Fatalf("400 must not retry, hits=%d", hits.Load())
	}
	if llm.IsTransientUpstream(err) {
		t.Fatalf("400 should not be transient: %v", err)
	}
}

func TestStreamChatWithTransientRetryRespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	_, err := streamChatWithTransientRetry(
		ctx,
		2,
		func(int) time.Duration { return 0 },
		func() bool { return false },
		func() {},
		nil,
		func() (llm.Message, error) {
			calls++
			if calls == 1 {
				cancel()
				return llm.Message{}, errors.New("network error")
			}
			return llm.Message{}, fmt.Errorf("should not call again")
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v (calls=%d)", err, calls)
	}
	if calls != 1 {
		t.Fatalf("cancel before retry: calls=%d", calls)
	}
}
