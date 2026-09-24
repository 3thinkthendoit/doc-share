package handler

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	CookieGuest       = "ds_guest"
	guestCookieMaxAge = 365 * 24 * 3600 // 游客身份一年有效
	commentMaxRunes   = 1000
)

// sessionUser 从 ds_session cookie 解析当前登录用户（公开路由用，无登录态返回 nil）
func (a *App) sessionUser(c *gin.Context) *model.User {
	token, err := c.Cookie(middleware.CookieSession)
	if err != nil || token == "" {
		return nil
	}
	uid, err := a.Signer.ParseUserToken(token)
	if err != nil {
		return nil
	}
	var u model.User
	if err := a.DB.First(&u, uid).Error; err != nil || u.Status != model.StatusEnabled {
		return nil
	}
	return &u
}

// guestIdentity 读取/生成游客身份「游客+4位随机码」（HMAC 签名防伪造）；isNew 表示需写 cookie
func (a *App) guestIdentity(c *gin.Context) (name string, isNew bool) {
	if cred, err := c.Cookie(CookieGuest); err == nil && cred != "" {
		if payload, ok := a.Signer.Verify(cred); ok && strings.HasPrefix(payload, "g:") {
			if n := strings.TrimSpace(payload[2:]); n != "" && len([]rune(n)) <= 32 {
				return n, false
			}
		}
	}
	return "游客" + util.RandomSlug(4), true
}

// recordVisitor 记录本次访问（按 文档+身份 去重累计），并返回最近访客列表供页面展示
func (a *App) recordVisitor(c *gin.Context, doc *model.Document) []model.DocumentVisitor {
	var identity, name, avatar string
	var userID uint
	if u := a.sessionUser(c); u != nil {
		identity = fmt.Sprintf("u:%d", u.ID)
		name, avatar, userID = u.DisplayName(), u.Avatar, u.ID
	} else {
		guest, isNew := a.guestIdentity(c)
		if isNew {
			setCookie(c, CookieGuest, a.Signer.Sign("g:"+guest), guestCookieMaxAge)
		}
		identity, name = "g:"+guest, guest
	}

	now := time.Now()
	var v model.DocumentVisitor
	if err := a.DB.Where("document_id = ? AND identity = ?", doc.ID, identity).First(&v).Error; err != nil {
		if err := a.DB.Create(&model.DocumentVisitor{
			DocumentID: doc.ID, Identity: identity, UserID: userID,
			Name: name, Avatar: avatar, Visits: 1, FirstSeenAt: now, LastSeenAt: now,
		}).Error; err != nil {
			// 并发首访可能撞 (document_id, identity) 唯一索引，下次访问会补录，记日志即可
			log.Printf("[collab] 记录访客失败: %v", err)
		}
	} else {
		a.DB.Model(&v).Updates(map[string]any{
			"name": name, "avatar": avatar, "user_id": userID,
			"last_seen_at": now, "visits": gorm.Expr("visits + 1"),
		})
	}
	return a.recentVisitors(doc.ID)
}

// recentVisitors 最近访客列表（只读查询，预览页复用、不写记录）
func (a *App) recentVisitors(docID uint) []model.DocumentVisitor {
	var visitors []model.DocumentVisitor
	a.DB.Where("document_id = ?", docID).Order("last_seen_at DESC").Limit(24).Find(&visitors)
	return visitors
}

// ---- 评论 ----

// commentView 评论的对外视图（游客/用户统一为 name+avatar）
type commentView struct {
	ID        uint      `json:"id"`
	ParentID  uint      `json:"parent_id"`
	UserID    uint      `json:"user_id"`
	Name      string    `json:"name"`
	Avatar    string    `json:"avatar"`
	IsGuest   bool      `json:"is_guest"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

func (a *App) listCommentViews(docID uint) []commentView {
	var comments []model.Comment
	a.DB.Where("document_id = ?", docID).Order("created_at ASC").Find(&comments)
	if len(comments) == 0 {
		return []commentView{}
	}
	// 收集用户 ID 批量取昵称/头像，避免逐条查询
	ids := make([]uint, 0, len(comments))
	for _, cm := range comments {
		if cm.UserID > 0 {
			ids = append(ids, cm.UserID)
		}
	}
	users := map[uint]model.User{}
	if len(ids) > 0 {
		var us []model.User
		a.DB.Where("id IN ?", ids).Find(&us)
		for _, u := range us {
			users[u.ID] = u
		}
	}
	views := make([]commentView, 0, len(comments))
	for _, cm := range comments {
		v := commentView{ID: cm.ID, ParentID: cm.ParentID, UserID: cm.UserID, Content: cm.Content, CreatedAt: cm.CreatedAt}
		if u, ok := users[cm.UserID]; cm.UserID > 0 && ok {
			v.Name, v.Avatar = u.DisplayName(), u.Avatar
		} else {
			v.Name, v.IsGuest = cm.GuestName, true
		}
		views = append(views, v)
	}
	return views
}

// shareUnlocked 带密码的分享必须先通过密码校验（持有签发的免密凭证），
// 防止绕过密码直接访问公开 API（评论读写、内容保存）；未通过时写 403 响应并返回 false
func (a *App) shareUnlocked(c *gin.Context, token string, share *model.Share) bool {
	if !share.HasPassword() {
		return true
	}
	cred, err := c.Cookie(CookieShare)
	if err != nil || !a.Signer.VerifyShareToken(cred, token) {
		c.JSON(http.StatusForbidden, gin.H{"error": "请先通过分享页密码验证"})
		return false
	}
	return true
}

// ShareListComments 分享页评论列表（公开，分享有效即可读）
func (a *App) ShareListComments(c *gin.Context) {
	token := c.Param("token")
	doc, share := a.loadShare(c, token)
	if doc == nil {
		return
	}
	if !a.shareUnlocked(c, token, share) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"comments": a.listCommentViews(doc.ID)})
}

// ShareAddComment 分享页发表评论：登录用户带身份，游客用游客身份（限流）
func (a *App) ShareAddComment(c *gin.Context) {
	token := c.Param("token")
	doc, share := a.loadShare(c, token)
	if doc == nil {
		return
	}
	if !a.shareUnlocked(c, token, share) {
		return
	}
	a.addComment(c, doc, "c:"+token)
}

// DocListComments 管理端评论列表（作者/管理员）
func (a *App) DocListComments(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{"comments": a.listCommentViews(doc.ID)})
}

// DocAddComment 管理端发表评论（属主/管理员/edit 角色成员）
func (a *App) DocAddComment(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocEdit(c, doc) {
		return
	}
	a.addComment(c, doc, "")
}

// addComment 发表评论公共实现；limitKey 非空时对该来源限流（游客按 IP，登录用户按账号）
func (a *App) addComment(c *gin.Context, doc *model.Document, limitKey string) {
	user := a.sessionUser(c)
	var limitID string
	if limitKey != "" {
		limitID = c.ClientIP()
		if user != nil {
			limitID = fmt.Sprintf("u:%d", user.ID)
		}
		if !a.Limiter.Allowed(limitID, limitKey) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "操作过于频繁，请稍后再试"})
			return
		}
	}
	var req struct {
		Content  string `json:"content"`
		ParentID uint   `json:"parent_id"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "评论内容不能为空"})
		return
	}
	if len([]rune(content)) > commentMaxRunes {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("评论最多 %d 字", commentMaxRunes)})
		return
	}
	// 一级回复：回复目标必须是本文档的顶层评论
	if req.ParentID > 0 {
		var parent model.Comment
		if err := a.DB.Where("id = ? AND document_id = ?", req.ParentID, doc.ID).First(&parent).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "回复的评论不存在"})
			return
		}
		if parent.ParentID != 0 {
			req.ParentID = parent.ParentID // 回复回复时归并到其顶层
		}
		// 不能回复自己的评论（仅登录用户可判定；游客无稳定身份，不做限制）
		if user != nil && parent.UserID == user.ID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "不能回复自己的评论"})
			return
		}
	}

	cm := model.Comment{DocumentID: doc.ID, ParentID: req.ParentID, Content: content}
	if user != nil {
		cm.UserID = user.ID
	} else {
		guest, isNew := a.guestIdentity(c)
		if isNew {
			setCookie(c, CookieGuest, a.Signer.Sign("g:"+guest), guestCookieMaxAge)
		}
		cm.GuestName = guest
	}
	if err := a.DB.Create(&cm).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "发表失败"})
		return
	}
	if limitID != "" {
		a.Limiter.Fail(limitID, limitKey) // 占用一次限额
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": cm.ID})
}

// DocDeleteComment 删除评论（属主级操作：文档属主或管理员）
func (a *App) DocDeleteComment(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocOwner(c, doc) {
		return
	}
	cid, _ := parseIntParam(c.Param("cid"))
	if cid == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	res := a.DB.Where("id = ? AND document_id = ?", cid, doc.ID).Delete(&model.Comment{})
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "评论不存在"})
		return
	}
	// 一并删除其下的回复
	a.DB.Where("parent_id = ?", cid).Delete(&model.Comment{})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- 版本修订 ----

// ListRevisions 修订列表（不含正文，作者/管理员）
func (a *App) ListRevisions(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	var revs []model.DocumentRevision
	a.DB.Where("document_id = ?", doc.ID).
		Select("id", "document_id", "editor_id", "editor_name", "created_at").
		Order("created_at DESC").Limit(100).Find(&revs)
	c.JSON(http.StatusOK, gin.H{"revisions": revs})
}

// RollbackRevision 回滚到指定修订（写操作，需编辑权限）
func (a *App) RollbackRevision(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocEdit(c, doc) {
		return
	}
	rid, _ := parseIntParam(c.Param("rid"))
	var rev model.DocumentRevision
	if rid == 0 || a.DB.Where("id = ? AND document_id = ?", rid, doc.ID).First(&rev).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "修订不存在"})
		return
	}
	user := middleware.CurrentUser(c)
	var editorID uint
	editorName := "系统"
	if user != nil {
		editorID = user.ID
		editorName = user.DisplayName()
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.DocumentRevision{
			DocumentID: doc.ID, EditorID: editorID, EditorName: editorName, Content: doc.Content,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Document{}).Where("id = ?", doc.ID).Update("content", rev.Content).Error; err != nil {
			return err
		}
		return pruneRevisions(tx, doc.ID)
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "回滚失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ShareSaveContent 分享链接编辑保存：PUT /s/:token/content
// 前置：访客持有有效分享凭证（密码校验后签发/无密码直通），且为登录用户，且分享开启了编辑权限
func (a *App) ShareSaveContent(c *gin.Context) {
	token := c.Param("token")
	doc, share := a.loadShare(c, token)
	if doc == nil {
		return
	}
	user := a.sessionUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "编辑前请先登录"})
		return
	}
	if share == nil || !share.CanEdit {
		c.JSON(http.StatusForbidden, gin.H{"error": "该分享未开启编辑权限"})
		return
	}
	// 带密码的分享必须先通过密码校验（持有签发的免密凭证），防止绕过密码直接改内容
	if !a.shareUnlocked(c, token, share) {
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	const maxContent = 1 << 19 // 512KB，与 longtext 上限相比是防滥用软顶
	if len(req.Content) > maxContent {
		c.JSON(http.StatusBadRequest, gin.H{"error": "内容过大"})
		return
	}
	// 覆盖前快照当前内容，与内容更新同事务，避免留下与实际内容不符的修订
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.DocumentRevision{
			DocumentID: doc.ID, EditorID: user.ID, EditorName: user.DisplayName(), Content: doc.Content,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Document{}).Where("id = ?", doc.ID).Update("content", req.Content).Error; err != nil {
			return err
		}
		return pruneRevisions(tx, doc.ID)
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// maxRevisions 每篇文档保留的修订上限，超出时清理最旧的
const maxRevisions = 50

// pruneRevisions 清理超出保留数量的最旧修订（需在事务内调用）
func pruneRevisions(tx *gorm.DB, docID uint) error {
	return tx.Where("document_id = ? AND id NOT IN (?)", docID,
		tx.Model(&model.DocumentRevision{}).Select("id").
			Where("document_id = ?", docID).
			Order("created_at DESC, id DESC").Limit(maxRevisions),
	).Delete(&model.DocumentRevision{}).Error
}

// parseIntParam 解析数字路径参数，非法返回 0
func parseIntParam(s string) (uint, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	return uint(n), err
}
