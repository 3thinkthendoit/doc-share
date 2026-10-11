package handler

import (
	"strings"
	"testing"

	"doc-share/internal/model"
)

// 回归：trimHistory 必须输出时间正序（最旧在前、最新在后）。
// 曾经的头插实现产出倒序上下文，导致模型收到"最新问题在最前、旧对话在后"
// 的乱序消息而答非所问。
func TestTrimHistoryKeepsChronologicalOrder(t *testing.T) {
	history := []model.ChatMessage{ // 按 id desc：最新在前
		{Role: "user", Content: "阅读ID=19的文档"},
		{Role: "assistant", Content: "你的文档如下"},
		{Role: "user", Content: "列一下我的文档"},
	}
	msgs := trimHistory(history)
	if len(msgs) != 3 {
		t.Fatalf("expect 3 msgs, got %d", len(msgs))
	}
	want := []string{"列一下我的文档", "你的文档如下", "阅读ID=19的文档"}
	for i, w := range want {
		if !strings.Contains(msgs[i].Content, w) {
			t.Fatalf("msg[%d] = %q, want contains %q（顺序必须为时间正序）", i, msgs[i].Content, w)
		}
	}
}

// 预算裁剪时优先保留最新消息：超预算的最旧消息被丢弃
func TestTrimHistoryDropsOldestOnBudget(t *testing.T) {
	history := []model.ChatMessage{ // 最新在前
		{Role: "user", Content: "最新问题"},
		{Role: "assistant", Content: strings.Repeat("旧", chatBudgetMaxChars)}, //  alone 超预算
	}
	msgs := trimHistory(history)
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "最新问题") {
		t.Fatalf("最新消息必须被保留在末尾，got %+v", msgs)
	}
}

// 回归：编辑短句兜底取历史文档 ID，必须能命中真实落库形态——
// 助手列表输出的"- id=19 ..."与用户提问"阅读ID=19"（注入消息不落库，不能依赖）
func TestLastHistoryDocIDMatchesRealShapes(t *testing.T) {
	cases := []struct {
		msgs []model.ChatMessage
		want int64
	}{
		{[]model.ChatMessage{ // 最新在前
			{Role: "user", Content: "三期都要改"},
			{Role: "assistant", Content: "- id=19 [markdown] 《执行互助平台 · 分三期开发计划与排期》{{doc:19}}"},
		}, 19},
		{[]model.ChatMessage{ // 最新在前：三期都要改 是最新提问，上轮读过 id=19
			{Role: "user", Content: "三期都要改"},
			{Role: "assistant", Content: "好的，我已读取文档 id=19 的正文，请告诉我要改哪里。"},
			{Role: "user", Content: "阅读ID=19的文档"},
		}, 19},
		{[]model.ChatMessage{{Role: "user", Content: "列一下我的文档"}}, 0},
	}
	for i, c := range cases {
		if got := lastHistoryDocID(c.msgs); got != c.want {
			t.Fatalf("case %d: lastHistoryDocID=%d, want %d", i, got, c.want)
		}
	}
}
