package handler

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"doc-share/internal/llm"
	"doc-share/internal/model"
	"doc-share/internal/storage"
)

// 聊天贴图：落库 Markdown，调 LLM 时拆成 vision parts。
// 格式：首行 ![chat-image](url)，其后可选正文。
const (
	chatImageAlt      = "chat-image"
	chatImageMaxBytes = 5 << 20 // 单张 5MB
	chatImageHistKeep = 3       // 历史里最多保留最近 N 张图给模型
	chatImageBudgetEq = 1200    // 预算估算：一张图约等于这么多字符
)

var reChatImageLine = regexp.MustCompile(`(?m)^!\[[^\]]*\]\(([^)\s]+)\)\s*$`)

// parseChatImageContent 从用户消息中取出首张图 URL 与剩余正文。
func parseChatImageContent(content string) (imgURL, text string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", ""
	}
	loc := reChatImageLine.FindStringSubmatchIndex(content)
	if loc == nil {
		return "", content
	}
	imgURL = strings.TrimSpace(content[loc[2]:loc[3]])
	before := strings.TrimSpace(content[:loc[0]])
	after := strings.TrimSpace(content[loc[1]:])
	switch {
	case before != "" && after != "":
		text = before + "\n\n" + after
	case before != "":
		text = before
	default:
		text = after
	}
	return imgURL, text
}

// formatChatImageContent 组装落库内容（图在前，便于历史回看）。
func formatChatImageContent(imgURL, text string) string {
	imgURL = strings.TrimSpace(imgURL)
	text = strings.TrimSpace(text)
	if imgURL == "" {
		return text
	}
	line := "![" + chatImageAlt + "](" + imgURL + ")"
	if text == "" {
		return line
	}
	return line + "\n\n" + text
}

// chatUserContentOK 允许纯文字、纯图、或图+文。
func chatUserContentOK(content string) bool {
	img, text := parseChatImageContent(content)
	return strings.TrimSpace(text) != "" || img != ""
}

// resolveChatImageURL 把站内上传图转为 data URL，便于上游拉取；失败则原样返回。
func (a *App) resolveChatImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "data:image/") {
		return raw
	}
	key := storage.ObjectKeyFromURL(raw, a.commentRustFSConfig(), a.commentCOSConfig())
	if key == "" {
		return raw
	}
	backend, err := a.fileStorage()
	if err != nil {
		return raw
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rc, _, err := backend.Get(ctx, key)
	if err != nil {
		return raw
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, chatImageMaxBytes+1))
	if err != nil || len(data) == 0 || int64(len(data)) > chatImageMaxBytes {
		return raw
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		return raw
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// llmUserMessage 将落库的用户消息转为 LLM 消息；withImage=false 时图改为文字占位。
func (a *App) llmUserMessage(content string, withImage bool) llm.Message {
	imgURL, text := parseChatImageContent(content)
	if imgURL == "" {
		return llm.Message{Role: "user", Content: content}
	}
	if !withImage {
		placeholder := "[图片]"
		if text != "" {
			return llm.Message{Role: "user", Content: placeholder + "\n" + text}
		}
		return llm.Message{Role: "user", Content: placeholder}
	}
	resolved := a.resolveChatImageURL(imgURL)
	parts := make([]llm.ContentPart, 0, 2)
	if text != "" {
		parts = append(parts, llm.ContentPart{Type: "text", Text: text})
	} else {
		parts = append(parts, llm.ContentPart{Type: "text", Text: "请查看这张图片。"})
	}
	parts = append(parts, llm.ContentPart{Type: "image_url", ImageURL: &llm.ImageURL{URL: resolved}})
	return llm.Message{Role: "user", Parts: parts}
}

// trimHistoryWithImages 同 trimHistory，但对用户贴图展开为 vision（仅最近 chatImageHistKeep 张）。
// 第二返回值为实际进入上下文的最旧消息 id（0=无一保留），供滚动摘要 foldBefore 使用：
// 条数窗口外与字符预算裁掉的更旧消息都应 id < foldBefore。
func (a *App) trimHistoryWithImages(history []model.ChatMessage) ([]llm.Message, uint) {
	type item struct {
		role    string
		content string
		img     string
	}
	raw := make([]item, 0, len(history))
	var oldestKeptID uint
	remaining := chatBudgetMaxChars
	for i := 0; i < len(history); i++ {
		content := history[i].Content
		img, textOnly := parseChatImageContent(content)
		budgetContent := content
		if img != "" {
			budgetContent = textOnly
			if budgetContent == "" {
				budgetContent = "[图片]"
			}
		}
		r := []rune(budgetContent)
		maxChars := chatMsgMaxChars
		if strings.HasPrefix(content, "[工具查询结果]") {
			maxChars = chatToolMsgMaxChars
		}
		if len(r) > maxChars {
			budgetContent = string(r[:maxChars]) + "…（超长已截断）"
			if img == "" {
				content = budgetContent
			} else if textOnly != "" {
				// 截断正文，保留图链
				content = formatChatImageContent(img, budgetContent)
			}
		}
		cost := len([]rune(budgetContent))
		if img != "" {
			cost += chatImageBudgetEq
		}
		remaining -= cost
		if remaining < 0 {
			break
		}
		raw = append(raw, item{role: history[i].Role, content: content, img: img})
		oldestKeptID = history[i].ID // 从新往旧走，最后一次成功保留的即最旧保留
	}
	// 从新到旧：标记最近 N 张可带图
	keep := 0
	withImg := make([]bool, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i].img != "" && keep < chatImageHistKeep {
			withImg[i] = true
			keep++
		}
	}
	out := make([]llm.Message, 0, len(raw))
	for i := len(raw) - 1; i >= 0; i-- {
		it := raw[i]
		if it.role == "user" && it.img != "" {
			out = append(out, a.llmUserMessage(it.content, withImg[i]))
		} else {
			out = append(out, llm.Message{Role: it.role, Content: it.content})
		}
	}
	return out, oldestKeptID
}
