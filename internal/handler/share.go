package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	CookieShare          = "ds_share"
	CookieShareReqPrefix = "ds_share_req_" // 按文档隔离：ds_share_req_<docID>
	CookieShareReqLegacy = "ds_share_req"  // 旧版单 cookie，读时迁移
	shareVerifyTTL       = 24 * 3600       // 分享密码凭证有效期（秒）
	shareAccessReqTTL    = 30 * 24 * 3600  // 申请查看凭证 cookie 有效期（秒）
)

func shareReqCookieName(docID uint) string {
	return CookieShareReqPrefix + strconv.FormatUint(uint64(docID), 10)
}

// setShareReqCookie 写入按文档隔离的申请凭证，并尽量消化旧版全局 cookie
func (a *App) setShareReqCookie(c *gin.Context, docID uint, token string) {
	setCookie(c, shareReqCookieName(docID), token, shareAccessReqTTL)
	a.migrateLegacyShareReqCookie(c)
}

// getShareReqCookie 读取本文档申请凭证；兼容旧版 ds_share_req 并迁移到按文档 cookie
func (a *App) getShareReqCookie(c *gin.Context, docID uint) string {
	if tok, err := c.Cookie(shareReqCookieName(docID)); err == nil && tok != "" {
		return tok
	}
	a.migrateLegacyShareReqCookie(c)
	if tok, err := c.Cookie(shareReqCookieName(docID)); err == nil && tok != "" {
		return tok
	}
	return ""
}

// migrateLegacyShareReqCookie 将旧版全局 cookie 迁到对应文档的 cookie 后清除
func (a *App) migrateLegacyShareReqCookie(c *gin.Context) {
	tok, err := c.Cookie(CookieShareReqLegacy)
	if err != nil || tok == "" {
		return
	}
	var req model.ShareAccessRequest
	if a.DB.Where("request_token = ?", tok).First(&req).Error == nil {
		setCookie(c, shareReqCookieName(req.DocumentID), tok, shareAccessReqTTL)
	}
	setCookie(c, CookieShareReqLegacy, "", -1)
}

// ShareView 分享阅读入口：GET /s/:token
func (a *App) ShareView(c *gin.Context) {
	token := c.Param("token")
	doc, share := a.loadShare(c, token)
	if doc == nil {
		return
	}

	// 无密码直接渲染
	if !share.HasPassword() {
		a.serveDoc(c, doc, share, token)
		return
	}

	// 有密码：校验已签发的免密凭证
	if cred, err := c.Cookie(CookieShare); err == nil && a.Signer.VerifyShareToken(cred, token) {
		a.serveDoc(c, doc, share, token)
		return
	}

	// 已批准的申请始终放行（即使后来关闭了「允许申请」）
	req := a.findAccessRequest(c, doc.ID)
	if req != nil && req.Status == model.AccessApproved {
		a.serveDoc(c, doc, share, token)
		return
	}

	errMsg := ""
	switch c.Query("err") {
	case "pwd":
		errMsg = "share.pwdWrong"
	case "lock":
		errMsg = "share.tryLater"
	}

	applyStatus := ""
	var applyReq *model.ShareAccessRequest
	allowApply := a.Settings().AllowShareApply
	if allowApply && req != nil {
		switch req.Status {
		case model.AccessPending:
			applyStatus, applyReq = "pending", req
		case model.AccessRejected:
			applyStatus, applyReq = "rejected", req
		}
	}
	a.renderSharePassword(c, doc, share, token, errMsg, applyStatus, applyReq)
}

func (a *App) renderSharePassword(c *gin.Context, doc *model.Document, share *model.Share, token, errMsg, applyStatus string, req *model.ShareAccessRequest) {
	user := a.sessionUser(c)
	data := gin.H{
		// 密码门只展示脱敏昵称，真实标题与完整昵称在验证通过前不可见
		"owner":       util.MaskName(doc.Owner.DisplayName()),
		"token":       token,
		"error":       errMsg,
		"applyStatus": applyStatus, // "" | pending | rejected
		"allowApply":  a.Settings().AllowShareApply,
		"user":        user,
	}
	if user != nil {
		data["applyName"] = user.DisplayName()
	}
	if req != nil && req.GuestName != "" {
		data["applyName"] = req.GuestName
	}
	a.render(c, "share_password.html", data)
}

// ShareSubmit 分享密码提交：POST /s/:token
func (a *App) ShareSubmit(c *gin.Context) {
	token := c.Param("token")
	doc, share := a.loadShare(c, token)
	if doc == nil {
		return
	}
	if !share.HasPassword() {
		a.serveDoc(c, doc, share, token)
		return
	}

	ip := c.ClientIP()
	if !a.Limiter.Allowed(ip, token) {
		c.Redirect(http.StatusSeeOther, "/s/"+token+"?err=lock")
		return
	}

	password := c.PostForm("password")
	if bcrypt.CompareHashAndPassword([]byte(share.Password), []byte(password)) != nil {
		a.Limiter.Fail(ip, token)
		c.Redirect(http.StatusSeeOther, "/s/"+token+"?err=pwd")
		return
	}

	a.Limiter.Reset(ip, token)
	cred := a.Signer.MakeShareToken(token, shareVerifyTTL*time.Second)
	setCookie(c, CookieShare, cred, shareVerifyTTL)
	// PRG：验证成功 303 回跳阅读页，后续 GET 命中免密凭证，刷新不再重复提交表单
	c.Redirect(http.StatusSeeOther, "/s/"+token)
}

// ShareAccessApply 申请查看：POST /s/:token/access-request（有密码分享）
func (a *App) ShareAccessApply(c *gin.Context) {
	token := c.Param("token")
	doc, share := a.loadShare(c, token)
	if doc == nil {
		return
	}
	if !share.HasPassword() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "share.applyNeedPwd"})
		return
	}
	if !a.Settings().AllowShareApply {
		c.JSON(http.StatusBadRequest, gin.H{"error": "share.applyDisabled"})
		return
	}

	ip := c.ClientIP()
	limitKey := "access:" + token
	if !a.Limiter.Allowed(ip, limitKey) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "share.tryLater"})
		return
	}

	var body struct {
		Name    string `json:"name" form:"name"`
		Contact string `json:"contact" form:"contact"`
		Message string `json:"message" form:"message"`
	}
	_ = c.ShouldBind(&body)
	name := strings.TrimSpace(body.Name)
	contact := strings.TrimSpace(body.Contact)
	message := strings.TrimSpace(body.Message)
	if len([]rune(name)) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "share.applyNameLong"})
		return
	}
	if len([]rune(contact)) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "share.applyContactLong"})
		return
	}
	if len([]rune(message)) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "share.applyMsgLong"})
		return
	}

	user := a.sessionUser(c)
	uid := uint(0)
	if user != nil {
		uid = user.ID
		if name == "" {
			name = user.DisplayName()
		}
	}
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "share.applyNameRequired"})
		return
	}

	// 已有待审：登录按 user_id；游客按 cookie，其次同文档同 IP 复用 pending（防清 cookie 刷申请）
	if user != nil {
		var exist model.ShareAccessRequest
		if a.DB.Where("document_id = ? AND user_id = ? AND status = ?", doc.ID, user.ID, model.AccessPending).
			First(&exist).Error == nil {
			a.setShareReqCookie(c, doc.ID, exist.RequestToken)
			c.JSON(http.StatusOK, gin.H{"ok": true, "status": "pending"})
			return
		}
	} else {
		if tok := a.getShareReqCookie(c, doc.ID); tok != "" {
			var exist model.ShareAccessRequest
			if a.DB.Where("request_token = ? AND document_id = ?", tok, doc.ID).First(&exist).Error == nil {
				if exist.Status == model.AccessPending {
					c.JSON(http.StatusOK, gin.H{"ok": true, "status": "pending"})
					return
				}
				if exist.Status == model.AccessApproved {
					c.JSON(http.StatusOK, gin.H{"ok": true, "status": "approved"})
					return
				}
			}
		}
		if ip != "" {
			var exist model.ShareAccessRequest
			if a.DB.Where("document_id = ? AND user_id = 0 AND status = ? AND client_ip = ?",
				doc.ID, model.AccessPending, ip).Order("id desc").First(&exist).Error == nil {
				a.setShareReqCookie(c, doc.ID, exist.RequestToken)
				c.JSON(http.StatusOK, gin.H{"ok": true, "status": "pending"})
				return
			}
		}
	}

	req := model.ShareAccessRequest{
		DocumentID:   doc.ID,
		UserID:       uid,
		GuestName:    name,
		GuestContact: contact,
		Message:      message,
		ClientIP:     ip,
		Status:       model.AccessPending,
		RequestToken: util.RandomSlug(24),
	}
	if err := a.DB.Create(&req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "share.applyFail"})
		return
	}
	a.Limiter.Fail(ip, limitKey) // 占用限额，防刷
	a.setShareReqCookie(c, doc.ID, req.RequestToken)
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": "pending"})
}

// findAccessRequest 按登录用户或 cookie 查找当前文档的访问申请。
// 登录用户优先：避免同浏览器残留游客 cookie 盖住本人已通过的申请。
func (a *App) findAccessRequest(c *gin.Context, docID uint) *model.ShareAccessRequest {
	if user := a.sessionUser(c); user != nil {
		var approved model.ShareAccessRequest
		if a.DB.Where("document_id = ? AND user_id = ? AND status = ?", docID, user.ID, model.AccessApproved).
			Order("id desc").First(&approved).Error == nil {
			a.setShareReqCookie(c, docID, approved.RequestToken)
			return &approved
		}
		var pending model.ShareAccessRequest
		if a.DB.Where("document_id = ? AND user_id = ? AND status = ?", docID, user.ID, model.AccessPending).
			Order("id desc").First(&pending).Error == nil {
			a.setShareReqCookie(c, docID, pending.RequestToken)
			return &pending
		}
		var rejected model.ShareAccessRequest
		if a.DB.Where("document_id = ? AND user_id = ? AND status = ?", docID, user.ID, model.AccessRejected).
			Order("id desc").First(&rejected).Error == nil {
			return &rejected
		}
	}
	if tok := a.getShareReqCookie(c, docID); tok != "" {
		var req model.ShareAccessRequest
		if a.DB.Where("request_token = ? AND document_id = ?", tok, docID).First(&req).Error == nil {
			return &req
		}
	}
	return nil
}

// loadShare 加载分享与文档，处理不存在/过期等情况并写响应
func (a *App) loadShare(c *gin.Context, token string) (*model.Document, *model.Share) {
	var share model.Share
	if err := a.DB.Where("share_token = ?", token).First(&share).Error; err != nil {
		c.String(http.StatusNotFound, "分享链接不存在或已取消")
		return nil, nil
	}
	if share.IsExpired() {
		c.String(http.StatusGone, "分享链接已过期")
		return nil, nil
	}
	var doc model.Document
	// Preload Owner：阅读页展示作者头像与昵称
	if err := a.DB.Preload("Owner").First(&doc, share.DocumentID).Error; err != nil {
		c.String(http.StatusNotFound, "文档不存在")
		return nil, nil
	}
	return &doc, &share
}

// serveDoc 渲染文档阅读页并累加浏览量；share 可为 nil（如管理端预览）
func (a *App) serveDoc(c *gin.Context, doc *model.Document, share *model.Share, token string) {
	a.DB.Model(&model.Document{}).Where("id = ?", doc.ID).
		UpdateColumn("view_count", gorm.Expr("view_count + 1"))
	doc.ViewCount++
	user := a.sessionUser(c)
	a.render(c, "share_view.html", gin.H{
		"title":       doc.Title,
		"rawTitle":    true, // 用户文档标题，跳过词典反查避免误译
		"doc":         doc,
		"share":       share,
		"token":       token,
		"user":        user,
		"canEdit":     user != nil && share != nil && share.CanEdit,
		"canModerate": user != nil && (user.ID == doc.OwnerID || user.IsAdmin()),
		"visitors":    a.recordVisitor(c, doc),
	})
}

// ListPendingAccessRequests 仪表盘待审访问申请汇总：GET /admin/api/access-requests?status=pending
// 管理员看全站；普通用户仅自己文档。
func (a *App) ListPendingAccessRequests(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	q := a.DB.Model(&model.ShareAccessRequest{}).Where("status = ?", model.AccessPending)
	if !user.IsAdmin() {
		q = q.Where("document_id IN (SELECT id FROM documents WHERE owner_id = ?)", user.ID)
	}
	var total int64
	q.Count(&total)
	var items []model.ShareAccessRequest
	// Session 克隆：避免 Count 污染后续 Find 的 SELECT
	q.Session(&gorm.Session{}).Order("id desc").Limit(100).Find(&items)

	docIDs := make([]uint, 0, len(items))
	seen := map[uint]struct{}{}
	for _, r := range items {
		if _, ok := seen[r.DocumentID]; ok {
			continue
		}
		seen[r.DocumentID] = struct{}{}
		docIDs = append(docIDs, r.DocumentID)
	}
	titles := map[uint]string{}
	if len(docIDs) > 0 {
		var docs []model.Document
		a.DB.Select("id, title").Where("id IN ?", docIDs).Find(&docs)
		for _, d := range docs {
			titles[d.ID] = d.Title
		}
	}

	out := make([]gin.H, 0, len(items))
	for _, r := range items {
		title := titles[r.DocumentID]
		if title == "" {
			title = "—"
		}
		out = append(out, gin.H{
			"id":          r.ID,
			"document_id": r.DocumentID,
			"doc_title":   title,
			"name":        r.GuestName,
			"contact":     r.GuestContact,
			"message":     r.Message,
			"status":      r.Status,
			"created_at":  r.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": total})
}

// ListAccessRequests 文档访问申请列表（属主/admin）：GET /admin/api/docs/:id/access-requests
func (a *App) ListAccessRequests(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocOwner(c, doc) {
		return
	}
	var items []model.ShareAccessRequest
	a.DB.Where("document_id = ?", doc.ID).Order("status asc, id desc").Limit(100).Find(&items)
	out := make([]gin.H, 0, len(items))
	for _, r := range items {
		row := gin.H{
			"id":         r.ID,
			"user_id":    r.UserID,
			"name":       r.GuestName,
			"contact":    r.GuestContact,
			"message":    r.Message,
			"status":     r.Status,
			"created_at": r.CreatedAt.Format("2006-01-02 15:04"),
		}
		if r.ReviewedAt != nil {
			row["reviewed_at"] = r.ReviewedAt.Format("2006-01-02 15:04")
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// ReviewAccessRequest 审批访问申请：POST /admin/api/docs/:id/access-requests/:rid {action: approve|reject}
func (a *App) ReviewAccessRequest(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocOwner(c, doc) {
		return
	}
	rid, err := strconv.Atoi(c.Param("rid"))
	if err != nil || rid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	var status int
	switch strings.TrimSpace(body.Action) {
	case "approve":
		status = model.AccessApproved
	case "reject":
		status = model.AccessRejected
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	var req model.ShareAccessRequest
	if err := a.DB.Where("id = ? AND document_id = ?", rid, doc.ID).First(&req).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "share.applyNotFound"})
		return
	}
	if req.Status != model.AccessPending {
		c.JSON(http.StatusBadRequest, gin.H{"error": "share.applyNotPending"})
		return
	}
	now := time.Now()
	user := middleware.CurrentUser(c)
	reviewerID := uint(0)
	if user != nil {
		reviewerID = user.ID
	}
	if err := a.DB.Model(&req).Updates(map[string]any{
		"status":      status,
		"reviewed_at": now,
		"reviewer_id": reviewerID,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "share.applyReviewFail"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": status})
}
