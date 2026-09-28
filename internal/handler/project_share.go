package handler

import (
	"fmt"
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

// 项目分享：把整个项目下的文档集合对外分享，语义与文档级分享（/s/:token）同构，
// 但作用于动态文档集合。管理端在 /console/api/projects/:id/share* 配置；
// 访客端走专属路由 /ps/:token（列表）与 /ps/:token/d/:docId（查看/编辑）。
const (
	CookieProjectShare          = "ds_pshare"      // 项目分享密码免密凭证（与文档分享 ds_share 隔离，互不覆盖）
	CookieProjectShareReqPrefix = "ds_pshare_req_" // 按项目隔离的申请凭证：ds_pshare_req_<projectID>
)

func projectShareReqCookieName(projectID uint) string {
	return CookieProjectShareReqPrefix + strconv.FormatUint(uint64(projectID), 10)
}

func (a *App) setProjectShareReqCookie(c *gin.Context, projectID uint, token string) {
	setCookie(c, projectShareReqCookieName(projectID), token, shareAccessReqTTL)
}

// ---- 加载与鉴权 ----

// loadProjectShare 按 token 加载项目分享与项目，处理不存在/过期并写响应
func (a *App) loadProjectShare(c *gin.Context, token string) (*model.Project, *model.ProjectShare) {
	var ps model.ProjectShare
	if err := a.DB.Where("share_token = ?", token).First(&ps).Error; err != nil {
		c.String(http.StatusNotFound, "分享链接不存在或已取消")
		return nil, nil
	}
	if ps.IsExpired() {
		c.String(http.StatusGone, "分享链接已过期")
		return nil, nil
	}
	var project model.Project
	// Preload Owner：密码门展示脱敏属主昵称、列表页展示属主
	if err := a.DB.Preload("Owner").First(&project, ps.ProjectID).Error; err != nil {
		c.String(http.StatusNotFound, "项目不存在")
		return nil, nil
	}
	return &project, &ps
}

// loadProjectShareDoc 加载 :docId 指定的文档并校验其归属该项目
func (a *App) loadProjectShareDoc(c *gin.Context, project *model.Project) *model.Document {
	docID, _ := strconv.Atoi(c.Param("docId"))
	if docID <= 0 {
		c.String(http.StatusBadRequest, "参数错误")
		return nil
	}
	var doc model.Document
	if err := a.DB.Preload("Owner").First(&doc, docID).Error; err != nil || doc.ProjectID != project.ID {
		c.String(http.StatusNotFound, "文档不存在")
		return nil
	}
	return &doc
}

// psOwnerBypass 项目属主或管理员访问自己项目的分享链接时免密
func (a *App) psOwnerBypass(c *gin.Context, project *model.Project) bool {
	user := a.sessionUser(c)
	if user == nil || project == nil {
		return false
	}
	return user.IsAdmin() || user.ID == project.OwnerID
}

// findProjectAccessRequest 按登录用户或 cookie 查找当前项目的访问申请（登录优先）
func (a *App) findProjectAccessRequest(c *gin.Context, projectID uint) *model.ShareAccessRequest {
	if user := a.sessionUser(c); user != nil {
		var approved model.ShareAccessRequest
		if a.DB.Where("project_id = ? AND user_id = ? AND status = ?", projectID, user.ID, model.AccessApproved).
			Order("id desc").First(&approved).Error == nil {
			a.setProjectShareReqCookie(c, projectID, approved.RequestToken)
			return &approved
		}
		var pending model.ShareAccessRequest
		if a.DB.Where("project_id = ? AND user_id = ? AND status = ?", projectID, user.ID, model.AccessPending).
			Order("id desc").First(&pending).Error == nil {
			a.setProjectShareReqCookie(c, projectID, pending.RequestToken)
			return &pending
		}
		var rejected model.ShareAccessRequest
		if a.DB.Where("project_id = ? AND user_id = ? AND status = ?", projectID, user.ID, model.AccessRejected).
			Order("id desc").First(&rejected).Error == nil {
			return &rejected
		}
	}
	if tok, err := c.Cookie(projectShareReqCookieName(projectID)); err == nil && tok != "" {
		var req model.ShareAccessRequest
		if a.DB.Where("request_token = ? AND project_id = ?", tok, projectID).First(&req).Error == nil {
			return &req
		}
	}
	return nil
}

// psHasAccess 是否已通过项目分享的访问门：无密码、属主/管理员、密码凭证 cookie，或已批准的申请
func (a *App) psHasAccess(c *gin.Context, token string, project *model.Project, ps *model.ProjectShare) bool {
	if !ps.HasPassword() {
		return true
	}
	if a.psOwnerBypass(c, project) {
		return true
	}
	if cred, err := c.Cookie(CookieProjectShare); err == nil && a.Signer.VerifyShareToken(cred, token) {
		return true
	}
	if req := a.findProjectAccessRequest(c, project.ID); req != nil && req.Status == model.AccessApproved {
		return true
	}
	return false
}

// psUnlocked 公开 API（评论/保存）守卫：未通过访问门时写 403
func (a *App) psUnlocked(c *gin.Context, token string, project *model.Project, ps *model.ProjectShare) bool {
	if a.psHasAccess(c, token, project, ps) {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "share.needUnlock"})
	return false
}

// ---- 访客端：列表与密码门 ----

// ProjectShareView 项目分享入口：GET /ps/:token（密码门 → 项目文档列表）
func (a *App) ProjectShareView(c *gin.Context) {
	token := c.Param("token")
	project, ps := a.loadProjectShare(c, token)
	if project == nil {
		return
	}
	if a.psHasAccess(c, token, project, ps) {
		a.renderProjectList(c, project, ps, token)
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
	req := a.findProjectAccessRequest(c, project.ID)
	if a.Settings().AllowShareApply && req != nil {
		switch req.Status {
		case model.AccessPending:
			applyStatus = "pending"
		case model.AccessRejected:
			applyStatus = "rejected"
		}
	}
	a.renderProjectSharePassword(c, project, token, errMsg, applyStatus, req)
}

func (a *App) renderProjectSharePassword(c *gin.Context, project *model.Project, token, errMsg, applyStatus string, req *model.ShareAccessRequest) {
	user := a.sessionUser(c)
	data := gin.H{
		// 密码门只展示脱敏昵称，项目名称与完整昵称在验证通过前不可见
		"owner":       util.MaskName(project.Owner.DisplayName()),
		"token":       token,
		"formAction":  "/ps/" + token,
		"applyAction": "/ps/" + token + "/access-request",
		"error":       errMsg,
		"applyStatus": applyStatus,
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

// renderProjectList 渲染项目下文档列表（分享上下文）
func (a *App) renderProjectList(c *gin.Context, project *model.Project, ps *model.ProjectShare, token string) {
	user := a.sessionUser(c)
	pg := parseWebPage(c)
	tx := a.DB.Model(&model.Document{}).Where("project_id = ?", project.ID)
	var total int64
	tx.Count(&total)
	pg = pg.withTotal(total)
	var docs []model.Document
	tx.Select("id", "title", "type", "view_count", "updated_at").
		Order("updated_at desc").Offset(pg.Offset).Limit(pg.Size).Find(&docs)

	data := gin.H{
		"title":       project.Name,
		"rawTitle":    true,
		"project":     project,
		"docs":        docs,
		"token":       token,
		"user":        user,
		"canEdit":     user != nil && ps.CanEdit,
		"canModerate": user != nil && (user.IsAdmin() || user.ID == project.OwnerID),
	}
	for k, v := range pagerFields(pg, "/ps/"+token, "") {
		data[k] = v
	}
	a.render(c, "project_share_view.html", data)
}

// ProjectShareSubmit 项目分享密码提交：POST /ps/:token
func (a *App) ProjectShareSubmit(c *gin.Context) {
	token := c.Param("token")
	project, ps := a.loadProjectShare(c, token)
	if project == nil {
		return
	}
	if !ps.HasPassword() || a.psOwnerBypass(c, project) {
		a.renderProjectList(c, project, ps, token)
		return
	}
	ip := c.ClientIP()
	if !a.Limiter.Allowed(ip, token) {
		c.Redirect(http.StatusSeeOther, "/ps/"+token+"?err=lock")
		return
	}
	password := c.PostForm("password")
	if bcrypt.CompareHashAndPassword([]byte(ps.Password), []byte(password)) != nil {
		a.Limiter.Fail(ip, token)
		c.Redirect(http.StatusSeeOther, "/ps/"+token+"?err=pwd")
		return
	}
	a.Limiter.Reset(ip, token)
	cred := a.Signer.MakeShareToken(token, shareVerifyTTL*time.Second)
	setCookie(c, CookieProjectShare, cred, shareVerifyTTL)
	// PRG：验证成功 303 回跳列表页，后续 GET 命中免密凭证
	c.Redirect(http.StatusSeeOther, "/ps/"+token)
}

// ProjectShareAccessApply 申请查看：POST /ps/:token/access-request（有密码分享）
func (a *App) ProjectShareAccessApply(c *gin.Context) {
	token := c.Param("token")
	project, ps := a.loadProjectShare(c, token)
	if project == nil {
		return
	}
	if !ps.HasPassword() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "share.applyNeedPwd"})
		return
	}
	if !a.Settings().AllowShareApply {
		c.JSON(http.StatusBadRequest, gin.H{"error": "share.applyDisabled"})
		return
	}
	ip := c.ClientIP()
	limitKey := "paccess:" + token
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

	// 已有待审：登录按 user_id；游客按 cookie，其次同项目同 IP 复用 pending（防清 cookie 刷申请）
	if user != nil {
		var exist model.ShareAccessRequest
		if a.DB.Where("project_id = ? AND user_id = ? AND status = ?", project.ID, user.ID, model.AccessPending).
			First(&exist).Error == nil {
			a.setProjectShareReqCookie(c, project.ID, exist.RequestToken)
			c.JSON(http.StatusOK, gin.H{"ok": true, "status": "pending"})
			return
		}
	} else {
		if tok, err := c.Cookie(projectShareReqCookieName(project.ID)); err == nil && tok != "" {
			var exist model.ShareAccessRequest
			if a.DB.Where("request_token = ? AND project_id = ?", tok, project.ID).First(&exist).Error == nil {
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
			if a.DB.Where("project_id = ? AND user_id = 0 AND status = ? AND client_ip = ?",
				project.ID, model.AccessPending, ip).Order("id desc").First(&exist).Error == nil {
				a.setProjectShareReqCookie(c, project.ID, exist.RequestToken)
				c.JSON(http.StatusOK, gin.H{"ok": true, "status": "pending"})
				return
			}
		}
	}

	req := model.ShareAccessRequest{
		ProjectID:    project.ID,
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
	a.setProjectShareReqCookie(c, project.ID, req.RequestToken)
	a.notifyProjectAccessApply(project, name)
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": "pending"})
}

// ---- 访客端：文档查看 / 编辑 / 评论 ----

// ProjectShareDocView 项目内文档查看：GET /ps/:token/d/:docId
func (a *App) ProjectShareDocView(c *gin.Context) {
	token := c.Param("token")
	project, ps := a.loadProjectShare(c, token)
	if project == nil {
		return
	}
	doc := a.loadProjectShareDoc(c, project)
	if doc == nil {
		return
	}
	if !a.psHasAccess(c, token, project, ps) {
		// 未通过密码门：回列表页触发密码门
		c.Redirect(http.StatusSeeOther, "/ps/"+token)
		return
	}
	a.serveProjectDoc(c, ps, doc, token)
}

// serveProjectDoc 渲染项目分享上下文的文档页并累加浏览量（markdown / 画布 / HTML 整站）
func (a *App) serveProjectDoc(c *gin.Context, ps *model.ProjectShare, doc *model.Document, token string) {
	a.DB.Model(&model.Document{}).Where("id = ?", doc.ID).
		UpdateColumn("view_count", gorm.Expr("view_count + 1"))
	doc.ViewCount++
	user := a.sessionUser(c)
	apiBase := "/ps/" + token + "/d/" + strconv.FormatUint(uint64(doc.ID), 10)
	canEdit := user != nil && ps.CanEdit
	canModerate := user != nil && (user.ID == doc.OwnerID || user.IsAdmin())

	if model.IsCanvasType(doc.Type) {
		a.render(c, "json_view.html", gin.H{
			"title":        doc.Title,
			"rawTitle":     true,
			"doc":          doc,
			"docKind":      doc.Type,
			"user":         user,
			"canModerate":  canModerate,
			"visitors":     a.recordVisitor(c, doc),
			"shareCanEdit": canEdit,
			"token":        token,
			"contentApi":   apiBase + "/content",
		})
		return
	}
	if doc.Type == model.DocTypeHTML {
		a.render(c, "html_view.html", gin.H{
			"title":       doc.Title,
			"rawTitle":    true,
			"doc":         doc,
			"token":       token,
			"user":        user,
			"canModerate": canModerate,
			"entryURL":    a.psEntryURL(token, doc.ID),
			"visitors":    a.recordVisitor(c, doc),
		})
		return
	}
	a.render(c, "share_view.html", gin.H{
		"title":       doc.Title,
		"rawTitle":    true,
		"doc":         doc,
		"token":       token,
		"apiBase":     apiBase,
		"user":        user,
		"canEdit":     canEdit,
		"canModerate": canModerate,
		"visitors":    a.recordVisitor(c, doc),
	})
}

// psEntryURL 项目分享 HTML 整站入口：/ps/:token/d/:docId/raw/<sig>/index.html
func (a *App) psEntryURL(token string, docID uint) string {
	sig := a.Signer.MakeShareToken(token, htmlSigTTL)
	return "/ps/" + token + "/d/" + strconv.FormatUint(uint64(docID), 10) + "/raw/" + sig + "/" + htmlEntryName
}

// ProjectShareRaw 项目分享 HTML 整站文件：GET /ps/:token/d/:docId/raw/<sig>/<sitepath>
// 与 ShareRaw 同理凭路径内短时签名鉴权（sandbox iframe 子资源不带 cookie）
func (a *App) ProjectShareRaw(c *gin.Context) {
	token := c.Param("token")
	project, _ := a.loadProjectShare(c, token)
	if project == nil {
		return
	}
	doc := a.loadProjectShareDoc(c, project)
	if doc == nil {
		return
	}
	if doc.Type != model.DocTypeHTML {
		c.String(http.StatusNotFound, "非 HTML 整站文档")
		return
	}
	sig, sitePath := splitRawPath(c.Param("path"))
	if sig == "" || sitePath == "" {
		c.String(http.StatusNotFound, "路径无效")
		return
	}
	if !a.Signer.VerifyShareToken(sig, token) {
		c.String(http.StatusForbidden, "访问凭证已失效，请刷新页面")
		return
	}
	a.serveHTMLSiteFile(c, doc, sitePath)
}

// ProjectShareSaveContent 项目分享编辑保存：PUT /ps/:token/d/:docId/content
// 前置：登录用户 + 分享开启 CanEdit + 已通过访问门；复用文档分享的乐观锁保存实现
func (a *App) ProjectShareSaveContent(c *gin.Context) {
	token := c.Param("token")
	project, ps := a.loadProjectShare(c, token)
	if project == nil {
		return
	}
	doc := a.loadProjectShareDoc(c, project)
	if doc == nil {
		return
	}
	user := a.sessionUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "编辑前请先登录"})
		return
	}
	if !ps.CanEdit {
		c.JSON(http.StatusForbidden, gin.H{"error": "该分享未开启编辑权限"})
		return
	}
	if !a.psUnlocked(c, token, project, ps) {
		return
	}
	a.saveShareContentBody(c, doc, user)
}

// ProjectShareListComments 项目分享文档评论列表：GET /ps/:token/d/:docId/comments
func (a *App) ProjectShareListComments(c *gin.Context) {
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
	c.JSON(http.StatusOK, gin.H{"comments": a.listCommentViews(doc.ID)})
}

// ProjectShareAddComment 项目分享文档发表评论：POST /ps/:token/d/:docId/comments
func (a *App) ProjectShareAddComment(c *gin.Context) {
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
	a.addComment(c, doc, "pc:"+token)
}

// ---- 管理端：项目分享配置与申请审批（属主/管理员） ----

// GetProjectShare 读取项目分享配置：GET /console/api/projects/:id/share
func (a *App) GetProjectShare(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
		return
	}
	var ps model.ProjectShare
	resp := gin.H{"enabled": false}
	if a.DB.Where("project_id = ?", project.ID).First(&ps).Error == nil {
		resp["enabled"] = true
		resp["can_edit"] = ps.CanEdit
		resp["has_password"] = ps.HasPassword()
		resp["url"] = a.siteBaseURL(c) + "/ps/" + ps.ShareToken
	}
	c.JSON(http.StatusOK, gin.H{"data": resp})
}

// UpsertProjectShare 开启/更新项目分享：POST /console/api/projects/:id/share
func (a *App) UpsertProjectShare(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
		return
	}
	var req shareReq
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	var ps model.ProjectShare
	found := a.DB.Where("project_id = ?", project.ID).First(&ps).Error == nil

	if !req.Enabled {
		// 关闭分享：删除配置与项目级访问申请
		if found {
			a.DB.Delete(&ps)
			a.DB.Where("project_id = ?", project.ID).Delete(&model.ShareAccessRequest{})
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "enabled": false})
		return
	}

	if !found {
		ps = model.ProjectShare{ProjectID: project.ID, ShareToken: util.RandomHex(16)}
	}
	ps.CanEdit = req.CanEdit // 编辑权限每次按当前设置覆盖
	// 密码：非空 = 设置密码；空 + remove_password = 清除已设密码；空 = 不修改（新建时无密码）
	if req.Password != "" {
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		ps.Password = string(hash)
	} else if req.RemovePassword {
		ps.Password = ""
	} else if !found {
		ps.Password = ""
	}
	if req.ExpireDays > 0 {
		t := time.Now().Add(time.Duration(req.ExpireDays) * 24 * time.Hour)
		ps.ExpireAt = &t
	} else {
		ps.ExpireAt = nil
	}
	if err := a.DB.Save(&ps).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "分享设置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"enabled":     true,
		"url":         a.siteBaseURL(c) + "/ps/" + ps.ShareToken,
		"hasPassword": ps.HasPassword(),
	})
}

// DeleteProjectShare 取消项目分享：DELETE /console/api/projects/:id/share
func (a *App) DeleteProjectShare(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
		return
	}
	a.DB.Where("project_id = ?", project.ID).Delete(&model.ProjectShare{})
	a.DB.Where("project_id = ?", project.ID).Delete(&model.ShareAccessRequest{})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListProjectAccessRequests 项目访问申请列表：GET /console/api/projects/:id/access-requests
func (a *App) ListProjectAccessRequests(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
		return
	}
	var items []model.ShareAccessRequest
	a.DB.Where("project_id = ?", project.ID).Order("status asc, id desc").Limit(100).Find(&items)
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

// ReviewProjectAccessRequest 审批项目访问申请：POST /console/api/projects/:id/access-requests/:rid
func (a *App) ReviewProjectAccessRequest(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
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
	if err := a.DB.Where("id = ? AND project_id = ?", rid, project.ID).First(&req).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "share.applyNotFound"})
		return
	}
	if req.Status != model.AccessPending {
		c.JSON(http.StatusBadRequest, gin.H{"error": "share.applyNotPending"})
		return
	}
	now := time.Now()
	reviewerID := uint(0)
	if user := middleware.CurrentUser(c); user != nil {
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
	var ps model.ProjectShare
	token := ""
	if a.DB.Where("project_id = ?", project.ID).First(&ps).Error == nil {
		token = ps.ShareToken
	}
	a.notifyProjectAccessReviewed(&req, project, status == model.AccessApproved, token)
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": status})
}

// ---- 项目分享通知 ----

func (a *App) notifyProjectAccessApply(project *model.Project, applicantName string) {
	if project == nil || project.OwnerID == 0 {
		return
	}
	name := strings.TrimSpace(applicantName)
	if name == "" {
		name = "访客"
	}
	a.notify(notifyPayload{
		UserID:  project.OwnerID,
		Kind:    model.MsgAccessApply,
		Title:   "收到项目查看申请",
		Body:    fmt.Sprintf("%s 申请查看项目「%s」", name, clipRunes(project.Name, 80)),
		Link:    "/console/projects",
		RefType: "project",
		RefID:   project.ID,
	})
}

func (a *App) notifyProjectAccessReviewed(req *model.ShareAccessRequest, project *model.Project, approved bool, shareToken string) {
	if req == nil || project == nil {
		return
	}
	kind := model.MsgAccessRejected
	title := "查看申请未通过"
	body := fmt.Sprintf("你对项目「%s」的查看申请未通过", clipRunes(project.Name, 80))
	link := ""
	if shareToken != "" {
		link = "/ps/" + shareToken
	}
	if approved {
		kind = model.MsgAccessApproved
		title = "查看申请已通过"
		body = fmt.Sprintf("你对项目「%s」的查看申请已通过，可以打开分享链接阅读", clipRunes(project.Name, 80))
	}
	p := notifyPayload{
		UserID:  req.UserID,
		Kind:    kind,
		Title:   title,
		Body:    body,
		Link:    link,
		RefType: "access_request",
		RefID:   req.ID,
	}
	if req.UserID == 0 && looksLikeEmail(req.GuestContact) {
		p.EmailTo = strings.TrimSpace(req.GuestContact)
	}
	a.notify(p)
}
