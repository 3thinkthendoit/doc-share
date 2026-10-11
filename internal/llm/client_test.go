package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cfgAt(url string) Config { return Config{BaseURL: url, APIKey: "k", Model: "m"} }

// sseServer 返回按行发送 SSE 的测试上游
func sseServer(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
}

// 上游每个 chunk 全量重发 name/arguments（部分代理的行为），聚合结果不得双写
func TestStreamChatToolCallsFullResend(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"list_projects","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"list_projects","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"read_document","arguments":"{\"id\":3}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"read_document","arguments":"{\"id\":3}"}}]}}]}`,
	})
	defer srv.Close()

	msg, err := cfgAt(srv.URL).StreamChat(context.Background(), []Message{{Role: "user", Content: "hi"}}, []Tool{}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("期望 2 个 tool_calls，得到 %d: %+v", len(msg.ToolCalls), msg.ToolCalls)
	}
	if msg.ToolCalls[0].Function.Name != "list_projects" || msg.ToolCalls[0].Function.Arguments != "{}" {
		t.Fatalf("聚合错误: %q / %q", msg.ToolCalls[0].Function.Name, msg.ToolCalls[0].Function.Arguments)
	}
	if msg.ToolCalls[1].Function.Name != "read_document" || msg.ToolCalls[1].Function.Arguments != `{"id":3}` {
		t.Fatalf("聚合错误: %q / %q", msg.ToolCalls[1].Function.Name, msg.ToolCalls[1].Function.Arguments)
	}
}

// 标准 OpenAI 增量分片：name 与 arguments 分多个 chunk 到达
func TestStreamChatToolCallsIncremental(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"search_doc"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"uments","arguments":"{\"qu"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ery\":\"x\"}"}}]}}]}`,
	})
	defer srv.Close()

	msg, err := cfgAt(srv.URL).StreamChat(context.Background(), []Message{{Role: "user", Content: "hi"}}, []Tool{}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("期望 1 个 tool_calls，得到 %d", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.Function.Name != "search_documents" || tc.Function.Arguments != `{"query":"x"}` {
		t.Fatalf("聚合错误: %q / %q", tc.Function.Name, tc.Function.Arguments)
	}
	if tc.ID != "c1" || tc.Type != "function" {
		t.Fatalf("id/type 错误: %q %q", tc.ID, tc.Type)
	}
}

// 消息体序列化不携带流式专用的 index 字段
func TestMessageToolCallsMarshalNoIndex(t *testing.T) {
	m := Message{Role: "assistant", ToolCalls: []ToolCallMsg{{ID: "c1", Type: "function"}}}
	m.ToolCalls[0].Function.Name = "f"
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "index") {
		t.Fatalf("消息体不应包含 index 字段: %s", data)
	}
}
