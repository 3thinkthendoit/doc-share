package handler

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"doc-share/internal/llm"
	"doc-share/internal/model"
)

var errEmptyRollingSummary = errors.New("empty rolling summary")

const (
	// 单次纳入摘要的旧消息条数上限（按 id 升序取最早一批）
	chatMemoryFoldMaxMsgs = 30
	// 摘要正文上限（超出则截断落库）
	chatMemorySummaryMaxRunes = 1200
	// 折入摘要时单条消息保留字符
	chatMemoryFoldMsgMaxRunes = 500
	// 摘要 LLM 调用超时（与主对话流式超时独立）
	chatMemoryTimeout = 45 * time.Second
)

// rollingSummaryMessages 有摘要时插入一条 user 消息供模型读取（与预读/技能目录注入风格一致）。
func rollingSummaryMessages(summary string) []llm.Message {
	sum := strings.TrimSpace(summary)
	if sum == "" {
		return nil
	}
	return []llm.Message{{
		Role:    "user",
		Content: "[会话记忆]\n以下为更早轮次的压缩摘要（非原文）。回答时优先采信；与最近消息冲突时以最近消息为准。\n\n" + sum,
	}}
}

// injectRollingSummaryAfterSystem 把会话记忆插到 system 之后（若 msgs 以 system 开头）；可重复调用以替换。
func injectRollingSummaryAfterSystem(msgs []llm.Message, summary string) []llm.Message {
	inj := rollingSummaryMessages(summary)
	// 去掉已有的会话记忆注入，避免刷新后重复
	out := make([]llm.Message, 0, len(msgs)+1)
	for i, m := range msgs {
		if i > 0 && m.Role == "user" && strings.HasPrefix(m.Content, "[会话记忆]") {
			continue
		}
		out = append(out, m)
	}
	if len(inj) == 0 {
		return out
	}
	if len(out) == 0 {
		return inj
	}
	res := make([]llm.Message, 0, len(out)+len(inj))
	res = append(res, out[0])
	res = append(res, inj...)
	res = append(res, out[1:]...)
	return res
}

// loadMemoryFold 取出尚未写入摘要、且严格早于 foldBeforeID 的消息（最早一批）。
// foldBeforeID 为实际进入模型上下文的最旧消息 id：覆盖「条数窗口外」与「字符预算裁掉」两类。
func (a *App) loadMemoryFold(sessionID, summaryUptoID, foldBeforeID uint) []model.ChatMessage {
	if foldBeforeID == 0 || foldBeforeID <= summaryUptoID {
		return nil
	}
	var fold []model.ChatMessage
	err := a.DB.Where("session_id = ? AND id > ? AND id < ?", sessionID, summaryUptoID, foldBeforeID).
		Order("id asc").Limit(chatMemoryFoldMaxMsgs).Find(&fold).Error
	if err != nil {
		log.Printf("[chat-memory] 加载待摘要消息失败 session=%d: %v", sessionID, err)
		return nil
	}
	return fold
}

func foldLineForSummary(m model.ChatMessage) string {
	content := m.Content
	if strings.HasPrefix(content, "[工具查询结果]") {
		content = "[工具查询结果]（已省略）"
	} else if img, text := parseChatImageContent(content); img != "" {
		content = strings.TrimSpace(text)
		if content == "" {
			content = "[图片]"
		}
	}
	if utf8.RuneCountInString(content) > chatMemoryFoldMsgMaxRunes {
		content = string([]rune(content)[:chatMemoryFoldMsgMaxRunes]) + "…"
	}
	role := m.Role
	if role == "" {
		role = "?"
	}
	return role + ": " + content
}

func buildRollingSummaryPrompt(oldSummary string, fold []model.ChatMessage) []llm.Message {
	var b strings.Builder
	b.WriteString("将「已有会话记忆」与「未进入本轮上下文的旧消息」合并为更新后的会话记忆。\n")
	b.WriteString("要求：保留用户目标、约定、未完成事项、关键指代（文档 id/标题）；删除寒暄、重复与工具原始大段输出；")
	b.WriteString("不超过 800 字；只用与用户相同的语言；只输出摘要正文，不要标题或解释。\n\n")
	old := strings.TrimSpace(oldSummary)
	if old == "" {
		old = "（无）"
	}
	b.WriteString("【已有会话记忆】\n")
	b.WriteString(old)
	b.WriteString("\n\n【未进入本轮上下文的旧消息】\n")
	for _, m := range fold {
		b.WriteString(foldLineForSummary(m))
		b.WriteByte('\n')
	}
	return []llm.Message{
		{Role: "system", Content: "你是会话记忆压缩器，只输出更新后的摘要正文。"},
		{Role: "user", Content: b.String()},
	}
}

func clipRollingSummary(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= chatMemorySummaryMaxRunes {
		return s
	}
	return string([]rune(s)[:chatMemorySummaryMaxRunes-1]) + "…"
}

// compressRollingSummary 调用上游生成新摘要；失败返回 error，调用方不得清空旧摘要。
func (a *App) compressRollingSummary(ctx context.Context, cfg llm.Config, oldSummary string, fold []model.ChatMessage) (string, error) {
	var buf strings.Builder
	msg, err := cfg.StreamChat(ctx, buildRollingSummaryPrompt(oldSummary, fold), nil, func(delta string) {
		buf.WriteString(delta)
	})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(msg.Content)
	if out == "" {
		out = strings.TrimSpace(buf.String())
	}
	out = clipRollingSummary(out)
	if out == "" {
		return "", errEmptyRollingSummary
	}
	return out, nil
}

// maybeRefreshRollingSummary 若有未进入上下文且未摘要的消息，则压缩并落库；失败只打日志，不阻断主对话。
// foldBeforeID：本轮实际保留的最旧消息 id（见 trimHistoryWithImages）。
func (a *App) maybeRefreshRollingSummary(ctx context.Context, cfg llm.Config, s *model.ChatSession, foldBeforeID uint) {
	if s == nil || !cfg.Valid() || foldBeforeID == 0 {
		return
	}
	fold := a.loadMemoryFold(s.ID, s.SummaryUptoID, foldBeforeID)
	if len(fold) == 0 {
		return
	}
	sumCtx, cancel := context.WithTimeout(ctx, chatMemoryTimeout)
	defer cancel()
	newSum, err := a.compressRollingSummary(sumCtx, cfg, s.RollingSummary, fold)
	if err != nil {
		log.Printf("[chat-memory] 摘要失败 session=%d fold=%d: %v", s.ID, len(fold), err)
		return
	}
	upto := fold[len(fold)-1].ID
	if err := a.DB.Model(&model.ChatSession{}).Where("id = ?", s.ID).
		Updates(map[string]any{
			"rolling_summary":  newSum,
			"summary_upto_id":  upto,
		}).Error; err != nil {
		log.Printf("[chat-memory] 摘要落库失败 session=%d: %v", s.ID, err)
		return
	}
	s.RollingSummary = newSum
	s.SummaryUptoID = upto
	log.Printf("[chat-memory] 已更新摘要 session=%d upto=%d len=%d", s.ID, upto, utf8.RuneCountInString(newSum))
}
