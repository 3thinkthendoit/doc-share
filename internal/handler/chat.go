package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"doc-share/internal/llm"
	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
)

// 对话上下文窗口：携带的最近历史消息条数上限（含当前提问）
const chatHistoryLimit = 20

// aiToolRounds function calling 最大轮数（防止模型无限连环调用）
const aiToolRounds = 8

// chatApprovalTTL 敏感操作等待用户确认的时限
const chatApprovalTTL = 3 * time.Minute

// aiExecTool 执行一次工具调用并整理结果（错误转文案回传模型）
func (a *App) aiExecTool(exec func(name, argsJSON string) (string, error), tc llm.ToolCallMsg) string {
	result, err := exec(tc.Function.Name, tc.Function.Arguments)
	if err != nil {
		result = "工具执行失败：" + err.Error()
	}
	return result
}

// ReviewChatApproval 会话内 AI 敏感操作的用户裁决入口：approve=true 才放行执行
func (a *App) ReviewChatApproval(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id := c.Param("id")
	v, ok := a.approvals.Load(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "approval not found or expired"})
		return
	}
	pa := v.(chatApproval)
	if pa.UserID != user.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	if time.Now().After(pa.Expire) {
		a.approvals.Delete(id)
		c.JSON(http.StatusGone, gin.H{"error": "approval expired"})
		return
	}
	var body struct {
		Approve bool `json:"approve"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	select {
	case pa.Ch <- body.Approve:
	default:
	}
	a.approvals.Delete(id) // 一次裁决即失效
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// maskArgs 工具参数脱敏：密码类字段不进日志（粗粒度：检测到 password 字段即隐藏参数串）
func maskArgs(argsJSON string) string {
	if i := strings.Index(strings.ToLower(argsJSON), `"password`); i >= 0 {
		// 粗粒度兜底：检测到密码字段时隐藏整个参数串，避免正则遗漏变体
		return argsJSON[:i] + `{"password":"***"}`
	}
	return argsJSON
}

// chatSystemPrompt 默认系统提示词（未启用智能体时使用）
const chatSystemPrompt = "你是 DocShare 文档平台的 AI 助手，用与用户相同的语言回答，回答使用 Markdown 格式。" +
	"平台已提供文档检索工具（search_documents / read_document / list_projects）。" +
	"当用户询问平台内容、文档细节时请主动调用工具查询后再回答，引用时注明文档标题。" +
	"注意：search_documents 只返回文档元数据（标题/ID/链接），需要正文内容时必须再调用 read_document 获取。" +
	"工具返回的结果是真实可信的，请直接基于结果作答，不要声称\"没有找到\"除非结果确实为空。" +
	"工具结果中的 Markdown 链接是可点击的预览/分享地址，修改或创建文档后请把链接贴给用户方便实时查看效果。" +
	"【链接必须逐字引用工具返回的 Markdown 链接，严禁自己构造、改写或猜测任何 URL（包括把 /doc/<slug> 改写成 /docs/<id> 之类）】。" +
	"本站链接只有两种正确格式：文档预览 https://<站点域名>/doc/<slug>，分享 https://<站点域名>/s/<token>；出现任何其他形式（如 /docs/<数字ID>）即为错误。" +
	"没有工具结果支持的信息（如分享链接、密码、过期时间）一律不要输出，先调用工具获取。"

// ChatPage 在线体验页面（登录用户）
func (a *App) ChatPage(c *gin.Context) {
	a.render(c, "chat.html", gin.H{
		"title": a.tr(c, "chat.title"),
	})
}

// ListSessions 当前用户的会话列表（按最近更新排序）
func (a *App) ListChatSessions(c *gin.Context) {
	user := middleware.CurrentUser(c)
	var sessions []model.ChatSession
	if err := a.DB.Where("user_id = ?", user.ID).Order("updated_at desc").Limit(100).Find(&sessions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "加载会话失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// CreateSession 新建会话
func (a *App) CreateChatSession(c *gin.Context) {
	user := middleware.CurrentUser(c)
	var req struct {
		Title   string `json:"title"`
		AgentID uint   `json:"agent_id"`
	}
	_ = c.ShouldBind(&req) // 空请求体合法
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = model.ChatDefaultTitle
	}
	if utf8.RuneCountInString(title) > 100 {
		title = string([]rune(title)[:100])
	}
	s := model.ChatSession{UserID: user.ID, Title: title, AgentID: req.AgentID}
	if err := a.DB.Create(&s).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建会话失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"session": s})
}

// DeleteSession 删除会话（级联删除消息，仅本人）
func (a *App) DeleteChatSession(c *gin.Context) {
	user := middleware.CurrentUser(c)
	id := c.Param("id")
	res := a.DB.Where("id = ? AND user_id = ?", id, user.ID).Delete(&model.ChatSession{})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	a.DB.Where("session_id = ?", id).Delete(&model.ChatMessage{})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListMessages 会话消息列表（仅本人）
func (a *App) ListChatMessages(c *gin.Context) {
	user := middleware.CurrentUser(c)
	id := c.Param("id")
	s, ok := a.ownSession(c, user.ID, id)
	if !ok {
		return
	}
	var msgs []model.ChatMessage
	if err := a.DB.Where("session_id = ?", s.ID).Order("id asc").Find(&msgs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "加载消息失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": msgs})
}

// SessionCompletions 流式对话（SSE）：body {content}。
// 事件格式（text/event-stream）：
//
//	data: {"delta":"..."}   增量文本
//	data: {"done":true,"message_id":N}  结束
//	data: {"error":"..."}   失败
func (a *App) ChatCompletions(c *gin.Context) {
	user := middleware.CurrentUser(c)
	s, ok := a.ownSession(c, user.ID, c.Param("id"))
	if !ok {
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBind(&req); err != nil || strings.TrimSpace(req.Content) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入内容"})
		return
	}
	content := strings.TrimSpace(req.Content)
	if utf8.RuneCountInString(content) > 8000 {
		content = string([]rune(content)[:8000])
	}

	cfg := a.Settings().LLM
	if !cfg.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "chat.llmNotConfigured")})
		return
	}

	// 先落库用户消息；标题为默认值时改为内容摘要
	userMsg := model.ChatMessage{SessionID: s.ID, Role: "user", Content: content}
	if err := a.DB.Create(&userMsg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存消息失败"})
		return
	}
	if s.Title == model.ChatDefaultTitle {
		title := strings.ReplaceAll(content, "\n", " ")
		if utf8.RuneCountInString(title) > 30 {
			title = string([]rune(title)[:30])
		}
		a.DB.Model(&model.ChatSession{}).Where("id = ?", s.ID).Updates(map[string]any{"title": title})
	}

	// 组装上下文：系统提示 + 最近历史（最近 20 条，含刚插入的当前提问）
	var history []model.ChatMessage
	a.DB.Where("session_id = ?", s.ID).Order("id desc").Limit(chatHistoryLimit).Find(&history)
	msgs := make([]llm.Message, 0, len(history)+1)
	msgs = append(msgs, llm.Message{Role: "system", Content: chatSystemPrompt})
	for i := len(history) - 1; i >= 0; i-- {
		msgs = append(msgs, llm.Message{Role: history[i].Role, Content: history[i].Content})
	}

	// 数据工具：按当前用户权限检索/读取站内文档
	tools, execTool := a.chatTools(c, user)

	// SSE 响应头
	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	flusher, canFlush := c.Writer.(http.Flusher)
	writeEvent := func(payload string) {
		fmt.Fprintf(c.Writer, "data: %s\n\n", payload)
		if canFlush {
			flusher.Flush()
		}
	}

	var final strings.Builder   // 最终轮正文（唯一落库/外显的内容）
	var anyEmitted bool         // 是否有任何正文流出过（用于错误兜底判断）
	toolCache := map[string]string{} // 重复的相同调用直接复用结果，防止模型原地打转
	for round := 0; round < aiToolRounds; round++ {
		var rc strings.Builder
		assistant, err := cfg.StreamChat(c.Request.Context(), msgs, tools, func(delta string) {
			rc.WriteString(delta)
			anyEmitted = true
			writeEvent(`{"delta":` + jsonQuote(delta) + `}`)
		})
		if err != nil {
			if !anyEmitted {
				// 一个字都没出来：算失败，删除用户消息避免留垃圾
				a.DB.Delete(&userMsg)
				writeEvent(`{"error":` + jsonQuote(err.Error()) + `}`)
				return
			}
			final.WriteString(rc.String()) // 已有部分内容：提示中断，保留已有回答
			writeEvent(`{"error":` + jsonQuote(err.Error()) + `}`)
			break
		}
		if len(assistant.ToolCalls) == 0 {
			final.WriteString(rc.String())
			break // 最终回答完成
		}
		// 模型请求工具：中间轮的“思考文字”不作为回答，前端重置气泡并显示状态条
		if rc.Len() > 0 {
			writeEvent(`{"reset":true}`)
		}
		var names []string
		// 工具结果不使用 role:"tool" 消息回传（部分网关会丢失/变形该角色消息），
		// 而是以普通 user 消息注入 —— 兼容所有 OpenAI 兼容实现。
		msgs = append(msgs, llm.Message{Role: "assistant", Content: assistant.Content})
		results := strings.Builder{}
		results.WriteString("[工具查询结果]\n")
		for _, tc := range assistant.ToolCalls {
			names = append(names, tc.Function.Name)

			// 敏感操作（删除/恢复/回滚/开启公开分享）：先弹会话内审批，用户确认才执行
			var result string
			if aiNeedsApproval(tc) {
				apprID := util.RandomHex(8)
				decision := make(chan bool, 1)
				a.approvals.Store(apprID, chatApproval{
					UserID: user.ID, Tool: tc.Function.Name, Args: tc.Function.Arguments,
					Ch: decision, Expire: time.Now().Add(chatApprovalTTL),
				})
				writeEvent(`{"approval":{"id":` + jsonQuote(apprID) + `,"tool":` + jsonQuote(tc.Function.Name) + `,"args":` + jsonQuote(maskArgs(tc.Function.Arguments)) + `}}`)
				var approved bool
				select {
				case approved = <-decision:
				case <-time.After(chatApprovalTTL):
					result = "等待用户确认超时，操作未执行。"
				case <-c.Request.Context().Done():
					a.approvals.Delete(apprID)
					return
				}
				a.approvals.Delete(apprID)
				if approved && result == "" {
					result = a.aiExecTool(execTool, tc)
				} else if result == "" {
					result = "用户拒绝了该操作，未执行。请勿重复请求，向用户确认意图后继续。"
					log.Printf("[llm] 用户拒绝 %s(%s)", tc.Function.Name, maskArgs(tc.Function.Arguments))
				}
			} else {
				result = a.aiExecTool(execTool, tc)
			}

			cacheKey := tc.Function.Name + "\x00" + tc.Function.Arguments
			if cached, ok := toolCache[cacheKey]; ok {
				log.Printf("[llm] 重复工具调用命中缓存 %s(%s)", tc.Function.Name, maskArgs(tc.Function.Arguments))
				result = cached + "\n（该查询已执行过且结果相同，请基于已有结果直接作答，不要重复调用）"
			} else {
				logResult := result
				if utf8.RuneCountInString(logResult) > 400 {
					logResult = string([]rune(logResult)[:400]) + "…"
				}
				log.Printf("[llm] 工具调用 %s(%s) -> %s", tc.Function.Name, maskArgs(tc.Function.Arguments), logResult)
				toolCache[cacheKey] = result
			}
			results.WriteString("\n[" + tc.Function.Name + " 参数: " + maskArgs(tc.Function.Arguments) + "]\n" + result + "\n")
		}
		msgs = append(msgs, llm.Message{Role: "user", Content: results.String()})
		writeEvent(`{"tool":` + jsonQuote(strings.Join(names, ", ")) + `}`)
	}

	reply := final.String()

	// 兜底：轮数打满但模型全程未输出正文（如最后一轮仍在调工具）——
	// 去掉工具强制它根据已收集的工具结果作答一次
	if strings.TrimSpace(reply) == "" {
		var fb strings.Builder
		respMsg, err := cfg.StreamChat(c.Request.Context(), msgs, nil, func(delta string) {
			fb.WriteString(delta)
			writeEvent(`{"delta":` + jsonQuote(delta) + `}`)
		})
		if err == nil && strings.TrimSpace(respMsg.Content) != "" {
			reply = respMsg.Content
		} else if fb.Len() > 0 {
			reply = fb.String()
		}
	}
	if strings.TrimSpace(reply) == "" {
		reply = a.tr(c, "chat.generateFailed")
		writeEvent(`{"error":` + jsonQuote(a.tr(c, "chat.generateFailed")) + `}`)
	}
	assistantMsg := model.ChatMessage{SessionID: s.ID, Role: "assistant", Content: reply}
	if err := a.DB.Create(&assistantMsg).Error; err != nil {
		log.Printf("[llm] 回答落库失败 session=%d err=%v", s.ID, err)
	}
	a.DB.Model(&model.ChatSession{}).Where("id = ?", s.ID).UpdateColumn("updated_at", a.DB.NowFunc())
	writeEvent(fmt.Sprintf(`{"done":true,"message_id":%d}`, assistantMsg.ID))
}

// ownSession 取本人会话；不存在时写 404 并返回 false
func (a *App) ownSession(c *gin.Context, userID uint, sid string) (model.ChatSession, bool) {
	var s model.ChatSession
	if err := a.DB.Where("id = ? AND user_id = ?", sid, userID).First(&s).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return s, false
	}
	return s, true
}

// jsonQuote JSON 字符串编码（用标准库 marshal 保证转义正确）
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
