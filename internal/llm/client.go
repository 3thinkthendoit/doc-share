// Package llm 极简 OpenAI 兼容对话客户端：兼容 OpenAI / DeepSeek / Qwen / Ollama 等
// 暴露 /chat/completions 接口的服务，支持 SSE 流式增量返回。
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Config LLM 接入配置（管理员在系统设置中维护）
type Config struct {
	BaseURL string `json:"base_url"` // 如 https://api.deepseek.com（不含 /chat/completions）
	APIKey  string `json:"api_key"`
	Model   string `json:"model"` // 如 deepseek-chat、qwen-max
	// Temperature 采样温度：越低越保守。指针类型，显式设置 0 也会下发；
	// nil（未设置）才表示不传该字段、使用上游默认。
	Temperature *float64 `json:"temperature,omitempty"`
}

// Valid 配置是否完整可用
func (c Config) Valid() bool {
	return strings.TrimSpace(c.BaseURL) != "" && strings.TrimSpace(c.Model) != ""
}

// Message 一条对话消息（OpenAI 格式）。
// Content 在为空且携带 ToolCalls 时整体省略（部分上游拒绝 content:"" + tool_calls 组合）。
type Message struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []ToolCallMsg `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	// 单轮对话结束后由上游返回的 token 用量（仅最终轮有效，不外发到上游）
	Usage *Usage `json:"usage,omitempty"`
}

// Usage 单次调用的 token 用量（OpenAI 兼容协议字段命名）
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ToolCallMsg 消息体里的函数调用（请求/响应序列化用，不含流式专用的 index 字段）
type ToolCallMsg struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ToolCall 流式 delta 里的函数调用增量（含 index，仅解析用，不回传上游）
type ToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool 注册给模型的工具定义（OpenAI function 格式）
type Tool struct {
	Type     string   `json:"type"` // 固定 "function"
	Function ToolFunc `json:"function"`
}

// ToolFunc 工具的名称、描述与 JSON Schema 参数
type ToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// completionsRequest /chat/completions 请求体（仅覆盖用到的字段）
type completionsRequest struct {
	Model       string        `json:"model"`
	Messages    []Message     `json:"messages"`
	Stream      bool          `json:"stream"`
	Tools       []Tool        `json:"tools,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	// 要求上游在最终 chunk 返回本轮 usage；多数 OpenAI 兼容网关支持
	StreamOptions *StreamOpts  `json:"stream_options,omitempty"`
}

type StreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

// completionsResponse 非流式响应（连通性测试用）
type completionsResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// streamChunk SSE 流式响应片段
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage    `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c Config) endpoint() string {
	return strings.TrimRight(strings.TrimSpace(c.BaseURL), "/") + "/chat/completions"
}

func (c Config) newRequest(ctx context.Context, body any) (*http.Request, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(c.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
	}
	return req, nil
}

// pingTimeout 连通性测试整体超时
const pingTimeout = 20 * time.Second

// Ping 连通性测试：发一条极短的非流式请求，能拿到 200 即视为配置正确
func (c Config) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, completionsRequest{
		Model:       c.Model,
		Messages:    []Message{{Role: "user", Content: "hi"}},
		Temperature: c.Temperature,
	})
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("服务返回 %d: %s", resp.StatusCode, msg)
	}
	var cr completionsResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return fmt.Errorf("响应解析失败: %w", err)
	}
	if cr.Error != nil && cr.Error.Message != "" {
		return fmt.Errorf("%s", cr.Error.Message)
	}
	return nil
}

// streamTimeout 单次对话整体超时（长回答给足时间）
const streamTimeout = 5 * time.Minute

// StreamChat 流式对话：增量文本通过 onDelta 回调返回（保证按序、在返回前回调）。
// tools 非空时启用 function calling：模型请求工具时不返回错误，而是把累计出的
// assistant 消息（含 ToolCalls）返回给调用方执行后再次进入本方法。
// 上游对带 tools 的请求返回 400/422 时（模型不支持 function calling 或格式分歧），
// 自动去工具降级重试一次，保证对话不中断；完整错误体记入服务日志便于排查。
func (c Config) StreamChat(ctx context.Context, messages []Message, tools []Tool, onDelta func(string)) (Message, error) {
	ctx, cancel := context.WithTimeout(ctx, streamTimeout)
	defer cancel()
	log.Printf("[llm] 请求 model=%s msgs=%d tools=%d url=%s", c.Model, len(messages), len(tools), c.endpoint())
	// 消息序列结构化日志：role + 长度 + 是否含 tool_calls + 开头摘要，
	// 用于排查"工具结果是否真的发到了上游""哪条消息被截断"
	var seq strings.Builder
	for i, m := range messages {
		head := m.Content
		if r := []rune(head); len(r) > 30 {
			head = string(r[:30])
		}
		tc := ""
		if len(m.ToolCalls) > 0 {
			tc = fmt.Sprintf(" tool_calls=%d", len(m.ToolCalls))
		}
		seq.WriteString(fmt.Sprintf("\n  [%d] %s len=%d%s %q", i, m.Role, len(m.Content), tc, head))
	}
	log.Printf("[llm] 消息序列:%s", seq.String())
	msg, err := c.streamOnce(ctx, messages, tools, onDelta)
	if err == nil {
		log.Printf("[llm] 完成 model=%s tools=%d content_len=%d tool_calls=%d", c.Model, len(tools), len(msg.Content), len(msg.ToolCalls))
		return msg, nil
	}
	if tools != nil && (strings.Contains(err.Error(), "服务返回 400") || strings.Contains(err.Error(), "服务返回 422")) {
		// 降级是丢上下文的重灾区：原错误体必须完整留档，便于定位上游为何拒绝
		log.Printf("[llm] 带 tools 请求被上游拒绝（%v），自动降级为纯对话重试（模型将看不到工具定义）", err)
		return c.streamOnce(ctx, messages, nil, onDelta)
	}
	return msg, err
}

// streamOnce 执行一次流式请求（不降级）
func (c Config) streamOnce(ctx context.Context, messages []Message, tools []Tool, onDelta func(string)) (Message, error) {
	req, err := c.newRequest(ctx, completionsRequest{
		Model:       c.Model,
		Messages:    messages,
		Stream:      true,
		Tools:       tools,
		Temperature: c.Temperature,
		StreamOptions: &StreamOpts{IncludeUsage: true},
	})
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[llm] 连接失败 url=%s err=%v", c.endpoint(), err)
		return Message{}, fmt.Errorf("无法连接: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		msg := strings.TrimSpace(string(data))
		if parsed := parseUpstreamError(data); parsed != "" {
			msg = parsed
		}
		if len(msg) > 800 {
			msg = msg[:800]
		}
		log.Printf("[llm] 上游错误 status=%d tools=%d body=%s", resp.StatusCode, len(tools), msg)
		return Message{}, fmt.Errorf("服务返回 %d: %s", resp.StatusCode, msg)
	}

	var content strings.Builder
	var lastUsage *Usage
	toolAcc := map[int]*ToolCall{} // 按 index 聚合 tool_calls 增量
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // 跳过无法解析的心跳/注释行
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return Message{}, fmt.Errorf("%s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			if chunk.Usage != nil { lastUsage = chunk.Usage }
			continue
		}
		if chunk.Usage != nil { lastUsage = chunk.Usage }
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			content.WriteString(delta.Content)
			if onDelta != nil {
				onDelta(delta.Content)
			}
		}
		for i := range delta.ToolCalls {
			tc := delta.ToolCalls[i]
			acc, ok := toolAcc[tc.Index]
			if !ok {
				cp := tc
				acc = &cp
				toolAcc[tc.Index] = acc
			}
			// 兼容两种上游：标准实现按增量分片；部分代理每个 chunk 全量重发。
			// 判别：全量重发的片段必然以已累计内容为前缀 → 覆盖；否则按增量追加。
			// 注意不能用 Contains：增量碎片恰好包含已累计子串时（长参数流式传输下并不罕见），
			// 会被误判为全量重发而覆盖，拼出残缺 JSON。
			merge := func(accStr *string, frag string) {
				if frag == "" {
					return
				}
				cur := *accStr
				if cur != "" && len(frag) >= len(cur) && strings.HasPrefix(frag, cur) {
					*accStr = frag
				} else {
					*accStr = cur + frag
				}
			}
			merge(&acc.ID, tc.ID)
			merge(&acc.Function.Name, tc.Function.Name)
			merge(&acc.Function.Arguments, tc.Function.Arguments)
			acc.Type = "function"
		}
	}
	if err := scanner.Err(); err != nil {
		return Message{}, err
	}

	assistant := Message{Role: "assistant", Content: content.String()}
	if lastUsage != nil {
		assistant.Usage = lastUsage
	}
	if len(toolAcc) > 0 {
		idxs := make([]int, 0, len(toolAcc))
		for i := range toolAcc {
			idxs = append(idxs, i)
		}
		sort.Ints(idxs)
		for _, i := range idxs {
			tc := *toolAcc[i]
			if tc.ID == "" {
				tc.ID = fmt.Sprintf("call_%d", i)
			}
			assistant.ToolCalls = append(assistant.ToolCalls, ToolCallMsg{
				ID:   tc.ID,
				Type: "function",
			})
			assistant.ToolCalls[len(assistant.ToolCalls)-1].Function = tc.Function
		}
	}
	return assistant, nil
}

// parseUpstreamError 从错误响应体里提取 message（兼容 OpenAI 错误格式与纯文本）
func parseUpstreamError(data []byte) string {
	var e struct {
		Error any `json:"error"`
	}
	if err := json.Unmarshal(data, &e); err == nil {
		switch v := e.Error.(type) {
		case string:
			return v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				return m
			}
		}
	}
	return ""
}
