package handler

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

// mentionTokenRe 匹配评论中的 @token（用户名 / 昵称片段 / DocShare）
var mentionTokenRe = regexp.MustCompile(`@([A-Za-z0-9_\p{Han}.-]{1,64})`)

// mentionCandidate 评论 @ 自动补全项
type mentionCandidate struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Kind     string `json:"kind"` // user | bot
}

// isDocShareMention token 是否为智能体（大小写不敏感）
func isDocShareMention(token string) bool {
	return strings.EqualFold(strings.TrimSpace(token), model.DocShareBotName)
}

// extractMentionTokens 从评论文本提取去重后的 @token（不含 @）
func extractMentionTokens(content string) []string {
	matches := mentionTokenRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		tok := strings.TrimSpace(m[1])
		if tok == "" {
			continue
		}
		key := strings.ToLower(tok)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tok)
	}
	return out
}

// contentMentionsDocShare 评论是否 @ 了智能体
func contentMentionsDocShare(content string) bool {
	for _, tok := range extractMentionTokens(content) {
		if isDocShareMention(tok) {
			return true
		}
	}
	return false
}

// docMentionUsers 文档可 @ 的登录用户：属主 ∪ 归属项目成员（启用账号）
func (a *App) docMentionUsers(doc *model.Document) []model.User {
	if doc == nil {
		return nil
	}
	idSet := map[uint]struct{}{}
	if doc.OwnerID > 0 {
		idSet[doc.OwnerID] = struct{}{}
	}
	if doc.ProjectID > 0 {
		var ms []model.ProjectMember
		a.DB.Where("project_id = ?", doc.ProjectID).Find(&ms)
		for _, m := range ms {
			if m.UserID > 0 {
				idSet[m.UserID] = struct{}{}
			}
		}
		var proj model.Project
		if a.DB.Select("owner_id").First(&proj, doc.ProjectID).Error == nil && proj.OwnerID > 0 {
			idSet[proj.OwnerID] = struct{}{}
		}
	}
	if len(idSet) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	var us []model.User
	a.DB.Where("id IN ? AND status = ?", ids, model.StatusEnabled).Find(&us)
	return us
}

// resolveMentionedUserIDs 将文中 @token 解析为可通知的用户 ID（不含自己、不含 DocShare）
func (a *App) resolveMentionedUserIDs(doc *model.Document, content string, actorID uint) []uint {
	tokens := extractMentionTokens(content)
	if len(tokens) == 0 || doc == nil {
		return nil
	}
	users := a.docMentionUsers(doc)
	if len(users) == 0 {
		return nil
	}
	byUser, byNick, nickAmbiguous := buildMentionLookup(users)
	seen := map[uint]struct{}{}
	out := make([]uint, 0)
	for _, tok := range tokens {
		if isDocShareMention(tok) {
			continue
		}
		key := strings.ToLower(tok)
		uid, ok := byUser[key]
		if !ok {
			// 昵称冲突或与他人用户名撞车：不猜测，要求用 @username
			if nickAmbiguous[key] {
				continue
			}
			uid, ok = byNick[key]
		}
		if !ok || uid == 0 || uid == actorID {
			continue
		}
		if _, dup := seen[uid]; dup {
			continue
		}
		seen[uid] = struct{}{}
		out = append(out, uid)
	}
	return out
}

// buildMentionLookup 用户名优先；同昵称多人、或昵称等于他人用户名时标为歧义，禁止靠昵称通知。
func buildMentionLookup(users []model.User) (byUser, byNick map[string]uint, nickAmbiguous map[string]bool) {
	byUser = map[string]uint{}
	byNick = map[string]uint{}
	nickAmbiguous = map[string]bool{}
	for _, u := range users {
		byUser[strings.ToLower(u.Username)] = u.ID
	}
	for _, u := range users {
		nick := strings.ToLower(strings.TrimSpace(u.Nickname))
		if nick == "" {
			continue
		}
		// 昵称与他人 username 相同 → 歧义（@token 应解析为用户名主人）
		if other, ok := byUser[nick]; ok && other != u.ID {
			nickAmbiguous[nick] = true
			delete(byNick, nick)
			continue
		}
		if nickAmbiguous[nick] {
			continue
		}
		if existing, ok := byNick[nick]; ok && existing != u.ID {
			delete(byNick, nick)
			nickAmbiguous[nick] = true
			continue
		}
		byNick[nick] = u.ID
	}
	return byUser, byNick, nickAmbiguous
}

func (a *App) notifyCommentMentions(doc *model.Document, cm *model.Comment, actorName string, mentioned []uint) {
	if doc == nil || cm == nil || len(mentioned) == 0 {
		return
	}
	name := strings.TrimSpace(actorName)
	if name == "" {
		name = "访客"
	}
	snippet := clipRunes(strings.TrimSpace(cm.Content), 80)
	if snippet == "" && cm.Images != "" {
		snippet = "[图片]"
	}
	link := a.docNotifyLink(doc)
	for _, uid := range mentioned {
		a.notify(notifyPayload{
			UserID:  uid,
			Kind:    model.MsgCommentMention,
			Title:   "有人在评论中提到了你",
			Body:    fmt.Sprintf("%s 在「%s」的评论中提到了你：%s", name, clipRunes(doc.Title, 60), snippet),
			Link:    link,
			RefType: "comment",
			RefID:   cm.ID,
		})
	}
}

// listMentionCandidates 属主+项目成员 + DocShare bot；q 过滤用户名/昵称
func (a *App) listMentionCandidates(doc *model.Document, q string, excludeUID uint) []mentionCandidate {
	q = strings.TrimSpace(q)
	ql := strings.ToLower(q)
	out := make([]mentionCandidate, 0, 16)

	// 智能体始终可提（过滤时也匹配 DocShare）
	if q == "" || strings.Contains(strings.ToLower(model.DocShareBotName), ql) {
		out = append(out, mentionCandidate{
			ID: 0, Username: model.DocShareBotName, Nickname: model.DocShareBotName, Kind: "bot",
		})
	}

	for _, u := range a.docMentionUsers(doc) {
		if excludeUID > 0 && u.ID == excludeUID {
			continue
		}
		nick := u.DisplayName()
		if q != "" {
			if !strings.Contains(strings.ToLower(u.Username), ql) &&
				!strings.Contains(strings.ToLower(nick), ql) {
				continue
			}
		}
		out = append(out, mentionCandidate{
			ID: u.ID, Username: u.Username, Nickname: nick, Kind: "user",
		})
	}
	return out
}

func (a *App) writeMentionCandidates(c *gin.Context, doc *model.Document) {
	user := middleware.CurrentUser(c)
	if user == nil {
		user = a.sessionUser(c)
	}
	var exclude uint
	if user != nil {
		exclude = user.ID
	}
	c.JSON(http.StatusOK, gin.H{
		"candidates": a.listMentionCandidates(doc, c.Query("q"), exclude),
	})
}

// DocMentionCandidates 预览/管理端：文档可 @ 候选
func (a *App) DocMentionCandidates(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	a.writeMentionCandidates(c, doc)
}

// ShareMentionCandidates 分享页：文档可 @ 候选（需解锁）
func (a *App) ShareMentionCandidates(c *gin.Context) {
	token := c.Param("token")
	doc, share := a.loadShare(c, token)
	if doc == nil {
		return
	}
	if !a.shareUnlocked(c, token, doc, share) {
		return
	}
	a.writeMentionCandidates(c, doc)
}

// ProjectShareMentionCandidates 项目分享文档页 @ 候选
func (a *App) ProjectShareMentionCandidates(c *gin.Context) {
	token := c.Param("token")
	project, ps := a.loadProjectShare(c, token)
	if project == nil {
		return
	}
	doc := a.loadProjectShareDoc(c, project)
	if doc == nil {
		return
	}
	if !a.psUnlocked(c, token, project, ps) {
		return
	}
	a.writeMentionCandidates(c, doc)
}

// mentionAtCursor 供测试：从光标前文本提取正在输入的 @query（不含 @）
func mentionQueryAt(textBeforeCursor string) (query string, ok bool) {
	runes := []rune(textBeforeCursor)
	at := -1
	for i := len(runes) - 1; i >= 0; i-- {
		r := runes[i]
		if r == '@' {
			at = i
			break
		}
		if unicode.IsSpace(r) {
			return "", false
		}
	}
	if at < 0 {
		return "", false
	}
	// @ 前须为行首或空白，避免 email 误触发
	if at > 0 && !unicode.IsSpace(runes[at-1]) {
		return "", false
	}
	return string(runes[at+1:]), true
}
