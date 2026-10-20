package handler

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"doc-share/internal/llm"
	"doc-share/internal/middleware"
	"doc-share/internal/model"
)

const (
	commentAgentMaxReplyRunes = 900
	commentAgentTimeout       = 3 * time.Minute
	// 每用户窗口内最多触发次数（与评论限流独立，专门保护 LLM 费用）
	commentAgentMaxPerWindow = 3
	commentAgentLimitWindow  = 10 * time.Minute
)

// commentAgentLimiter @DocShare 派工专用限流（按用户）
var commentAgentLimiter = middleware.NewShareLimiter(commentAgentMaxPerWindow, commentAgentLimitWindow)

// maybeEnqueueDocShareAgent 评论含 @DocShare 时异步派工；仅登录用户触发。
func (a *App) maybeEnqueueDocShareAgent(doc *model.Document, cm *model.Comment, actor *model.User) {
	if doc == nil || cm == nil || actor == nil || actor.ID == 0 {
		return
	}
	if cm.UserID == 0 || !contentMentionsDocShare(cm.Content) {
		return
	}
	if isDocShareBotComment(cm) {
		return
	}
	docID, commentID, userID := doc.ID, cm.ID, actor.ID
	task := strings.TrimSpace(stripDocShareMentions(cm.Content))
	if task == "" {
		task = "请基于当前文档回答或协助处理评论中的请求。"
	}

	limitID := strconv.FormatUint(uint64(userID), 10)
	if !commentAgentLimiter.Allowed(limitID, "docshare") {
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("[comment-agent] rate-limit reply panic: %v", rec)
				}
			}()
			var d model.Document
			var p model.Comment
			if a.DB.First(&d, docID).Error != nil || a.DB.First(&p, commentID).Error != nil {
				return
			}
			a.postDocShareReply(&d, &p, "智能体请求过于频繁，请稍后再试（每用户约 10 分钟内最多 3 次）。也可使用「打开对话」。")
		}()
		return
	}
	commentAgentLimiter.Fail(limitID, "docshare")

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[comment-agent] panic doc=%d comment=%d: %v", docID, commentID, rec)
			}
		}()
		a.runDocShareCommentAgent(docID, commentID, userID, task)
	}()
}

func isDocShareBotComment(cm *model.Comment) bool {
	if cm == nil {
		return false
	}
	if cm.IsBot {
		return true
	}
	// 兼容迁移前仅靠 GuestName 标记的存量回复
	return cm.UserID == 0 && strings.EqualFold(strings.TrimSpace(cm.GuestName), model.DocShareBotName)
}

func stripDocShareMentions(content string) string {
	return mentionTokenRe.ReplaceAllStringFunc(content, func(m string) string {
		tok := strings.TrimPrefix(m, "@")
		if isDocShareMention(tok) {
			return ""
		}
		return m
	})
}

func (a *App) runDocShareCommentAgent(docID, commentID, actorID uint, task string) {
	var doc model.Document
	if err := a.DB.First(&doc, docID).Error; err != nil {
		log.Printf("[comment-agent] 文档不存在 doc=%d: %v", docID, err)
		return
	}
	var parent model.Comment
	if err := a.DB.Where("id = ? AND document_id = ?", commentID, docID).First(&parent).Error; err != nil {
		log.Printf("[comment-agent] 评论不存在 comment=%d: %v", commentID, err)
		return
	}

	cfg := a.DefaultLLM()
	cfg.StreamTimeout = commentAgentTimeout
	if !cfg.Valid() {
		a.postDocShareReply(&doc, &parent, "智能体暂未配置语言模型，请联系管理员在系统设置中配置后再试。")
		return
	}

	var actor model.User
	if a.DB.First(&actor, actorID).Error != nil {
		actor = model.User{ID: actorID}
	}
	_, _, injectRead := a.chatTools(&actor, "")
	docText, err := readDocInject(injectRead, int64(doc.ID), aiPrereadMaxChars, task)
	if err != nil {
		docText = buildDocInjectBody(doc.Title, doc.ContentVersion, doc.Content, task, aiPrereadMaxChars)
	}

	sys := "你是 DocShare 文档平台的智能体，正在回复文档评论中 @DocShare 的派工。" +
		"只用与用户相同的语言作答；回答简洁、可直接作为评论回复；不要输出工具调用或占位符链接；" +
		"只基于下方提供的文档正文与用户任务作答，不要声称已修改文档（评论派工为只读）。"
	msgs := []llm.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: "[当前文档正文]\n\n" + docText},
		{Role: "user", Content: "[评论派工]\n" + task},
	}

	ctx, cancel := context.WithTimeout(context.Background(), commentAgentTimeout)
	defer cancel()
	reply, err := cfg.StreamChat(ctx, msgs, nil, nil)
	if err != nil {
		log.Printf("[comment-agent] LLM 失败 doc=%d comment=%d: %v", docID, commentID, err)
		a.postDocShareReply(&doc, &parent, "智能体处理失败，请稍后重试或改用「打开对话」。")
		return
	}
	text := strings.TrimSpace(reply.Content)
	if text == "" {
		a.postDocShareReply(&doc, &parent, "智能体没有生成有效回复，请换一种说法再试。")
		return
	}
	if utf8.RuneCountInString(text) > commentAgentMaxReplyRunes {
		text = string([]rune(text)[:commentAgentMaxReplyRunes-1]) + "…"
	}
	a.postDocShareReply(&doc, &parent, text)
}

func (a *App) postDocShareReply(doc *model.Document, parent *model.Comment, content string) {
	content = strings.TrimSpace(content)
	if doc == nil || parent == nil || content == "" {
		return
	}
	parentID := parent.ID
	if parent.ParentID != 0 {
		parentID = parent.ParentID
	}
	cm := model.Comment{
		DocumentID: doc.ID,
		ParentID:   parentID,
		UserID:     0,
		GuestName:  model.DocShareBotName,
		IsBot:      true,
		Content:    content,
	}
	if err := a.DB.Create(&cm).Error; err != nil {
		log.Printf("[comment-agent] 写回复失败 doc=%d parent=%d: %v", doc.ID, parentID, err)
		return
	}
	if parent.UserID > 0 {
		a.notify(notifyPayload{
			UserID:  parent.UserID,
			Kind:    model.MsgCommentReply,
			Title:   "DocShare 已回复你的评论",
			Body:    fmt.Sprintf("智能体在「%s」回复了你：%s", clipRunes(doc.Title, 60), clipRunes(content, 80)),
			Link:    a.docNotifyLink(doc),
			RefType: "comment",
			RefID:   cm.ID,
		})
	}
}
