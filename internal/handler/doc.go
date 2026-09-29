package handler

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// DocsPage 文档列表页（搜索 + 分页 + 来源筛选）
// 可见范围：自己的文档 ∪ 所参与项目内的他人文档（未挂项目的他人文档不可见）
func (a *App) DocsPage(c *gin.Context) {
	q := c.Query("q")
	origin := c.Query("origin") // ""=全部 mine=我创建的 shared=项目共享
	pg := parseWebPage(c)

	user := middleware.CurrentUser(c)
	// 当前用户参与的项目（成员路径的可见范围依据）
	var memberPids []uint
	if user != nil && !user.IsAdmin() {
		a.DB.Model(&model.ProjectMember{}).Where("user_id = ?", user.ID).Pluck("project_id", &memberPids)
	}

	tx := a.DB.Model(&model.Document{})
	if user != nil && !user.IsAdmin() {
		switch origin {
		case "mine":
			tx = tx.Where("owner_id = ?", user.ID)
		case "shared":
			if len(memberPids) == 0 {
				tx = tx.Where("1 = 0") // 未参与任何项目，直接返回空列表
			} else {
				tx = tx.Where("owner_id <> ? AND project_id IN ?", user.ID, memberPids)
			}
		default:
			if len(memberPids) > 0 {
				tx = tx.Where("owner_id = ? OR project_id IN ?", user.ID, memberPids)
			} else {
				tx = tx.Where("owner_id = ?", user.ID)
			}
		}
	} else if origin == "mine" && user != nil {
		tx = tx.Where("owner_id = ?", user.ID)
	}
	// 项目/分类筛选
	if p := c.Query("project"); p != "" {
		if p == "0" {
			tx = tx.Where("project_id = 0")
		} else if pid, err := strconv.Atoi(p); err == nil {
			tx = tx.Where("project_id = ?", pid)
		}
	}
	// 分享状态筛选：1=已分享 0=未分享
	if sh := c.Query("shared"); sh == "1" {
		tx = tx.Where("is_shared = 1")
	} else if sh == "0" {
		tx = tx.Where("is_shared = 0")
	}
	if ct := c.Query("category"); ct != "" {
		if ct == "0" {
			tx = tx.Where("category_id = 0")
		} else if cid, err := strconv.Atoi(ct); err == nil {
			tx = tx.Where("category_id = ?", cid)
		}
	}
	if q != "" {
		like := "%" + q + "%"
		// 正文搜索：html 整站文档的 Content 只是 manifest（正文在对象存储），
		// 须按类型显式排除，否则搜 "index" 会误命中所有整站文档的文件名清单
		tx = tx.Where("title LIKE ? OR slug LIKE ? OR (type <> ? AND content LIKE ?)",
			like, like, model.DocTypeHTML, like)
	}
	var total int64
	tx.Count(&total)
	pg = pg.withTotal(total)

	var docs []model.Document
	// 列表不拉 Content（longtext）；嵌入标签另用 LIKE 只取 id
	tx.Omit("Content").Preload("Owner").Preload("Share").Preload("Project").Preload("Category").Preload("UpdatedBy").
		Order("updated_at desc").Offset(pg.Offset).Limit(pg.Size).Find(&docs)

	embedTags := detectEmbedTags(a.DB, docs)

	// 逐行编辑权限（批量取成员角色，避免逐条查询）：
	// 属主/管理员可编辑；edit 角色成员可编辑所参与项目内的他人文档
	canEdit := make(map[uint]bool, len(docs))
	if user != nil {
		roleByPid := make(map[uint]string)
		if !user.IsAdmin() {
			var ms []model.ProjectMember
			a.DB.Where("user_id = ?", user.ID).Find(&ms)
			for _, m := range ms {
				roleByPid[m.ProjectID] = m.Role
			}
		}
		for _, d := range docs {
			if user.IsAdmin() || d.OwnerID == user.ID {
				canEdit[d.ID] = true
			} else if roleByPid[d.ProjectID] == model.MemberRoleEdit {
				canEdit[d.ID] = true
			}
		}
	}

	projects, categories := a.filterOptions(c)
	// 项目筛选下拉补上参与的项目（他人项目仅用于筛选，不进入新建/导入的归属下拉）
	if user != nil && !user.IsAdmin() && len(memberPids) > 0 {
		var extra []model.Project
		a.DB.Where("id IN ? AND owner_id <> ?", memberPids, user.ID).Order("name asc").Find(&extra)
		projects = append(projects, extra...)
	}
	extra := ""
	if q != "" {
		extra += "&q=" + url.QueryEscape(q)
	}
	if p := c.Query("project"); p != "" {
		extra += "&project=" + url.QueryEscape(p)
	}
	if ct := c.Query("category"); ct != "" {
		extra += "&category=" + url.QueryEscape(ct)
	}
	if sh := c.Query("shared"); sh != "" {
		extra += "&shared=" + url.QueryEscape(sh)
	}
	if origin != "" {
		extra += "&origin=" + url.QueryEscape(origin)
	}
	// 侧栏切换项目时保留其它筛选（不含 project）；无前导 &
	sideParts := []string{"size=" + strconv.Itoa(pg.Size)}
	if q != "" {
		sideParts = append(sideParts, "q="+url.QueryEscape(q))
	}
	if ct := c.Query("category"); ct != "" {
		sideParts = append(sideParts, "category="+url.QueryEscape(ct))
	}
	if sh := c.Query("shared"); sh != "" {
		sideParts = append(sideParts, "shared="+url.QueryEscape(sh))
	}
	if origin != "" {
		sideParts = append(sideParts, "origin="+url.QueryEscape(origin))
	}
	sideQS := strings.Join(sideParts, "&")

	data := gin.H{
		"title":      "文档管理",
		"docs":       docs,
		"q":          q,
		"origin":     origin,
		"project":    c.Query("project"),
		"category":   c.Query("category"),
		"shared":     c.Query("shared"),
		"projects":   projects,
		"categories": categories,
		"canEdit":    canEdit,
		"embedTags":  embedTags,
		"sideQS":     sideQS,
	}
	for k, v := range pagerFields(pg, "/console/docs", extra) {
		data[k] = v
	}
	a.render(c, "docs.html", data)
}

// filterOptions 当前用户自己的项目与分类（个人所有，admin 亦然；
// 文档归属跟随作者，任何人都不能把文档挂到他人的项目/分类下）
func (a *App) filterOptions(c *gin.Context) ([]model.Project, []model.Category) {
	var projects []model.Project
	var categories []model.Category
	user := middleware.CurrentUser(c)
	pTx := a.DB.Model(&model.Project{})
	cTx := a.DB.Model(&model.Category{})
	if user != nil {
		pTx = pTx.Where("owner_id = ?", user.ID)
		cTx = cTx.Where("owner_id = ?", user.ID)
	}
	pTx.Order("name asc").Find(&projects)
	cTx.Order("sort asc, id asc").Find(&categories)
	return projects, categories
}

// NewDocPage 新建文档页；?type=mindmap|board|drawio 时进入结构化画布编辑页；
// ?template=ID 时从模板创建（画布类模板进入对应编辑器并预填内容）
func (a *App) NewDocPage(c *gin.Context) {
	docType := c.Query("type")
	if model.IsCanvasType(docType) {
		a.render(c, "json_edit.html", gin.H{
			"title":         "新建文档",
			"rawTitle":      true,
			"doc":           nil,
			"docKind":       docType,
			"canEditDoc":    true,
			"isOwner":       true,
			"tplContent":    "",
			"tplProjectId":  0,
			"tplCategoryId": 0,
		})
		return
	}
	// 从模板创建：校验可见性（属主/绑定项目的成员/admin）
	var tpl *model.DocTemplate
	tplWarn := false
	if tid, err := strconv.Atoi(c.Query("template")); err == nil && tid > 0 {
		var t model.DocTemplate
		if err := a.DB.First(&t, tid).Error; err == nil && a.canUseTemplate(c, &t) {
			tpl = &t
			if model.IsCanvasType(t.Type) {
				projectID, categoryID := a.tplOwnershipFor(c, &t)
				a.render(c, "json_edit.html", gin.H{
					"title":         "新建文档",
					"rawTitle":      true,
					"doc":           nil,
					"docKind":       t.Type,
					"canEditDoc":    true,
					"isOwner":       true,
					"tplContent":    t.Content,
					"tplProjectId":  projectID,
					"tplCategoryId": categoryID,
				})
				return
			}
		} else {
			tplWarn = true // 模板不存在或无权使用：页面提示，其余按普通新建处理
		}
	}
	projects, categories := a.filterOptions(c)
	// 模板下拉（新建页用，不含内容）
	var templates []model.DocTemplate
	a.visibleTemplateScope(c).Select("id, name, type").Order("updated_at desc").Find(&templates)
	var tplData gin.H
	if tpl != nil {
		projectID, categoryID := a.tplOwnershipFor(c, tpl)
		tplData = gin.H{
			"id":          tpl.ID,
			"name":        tpl.Name,
			"type":        tpl.Type,
			"content":     tpl.Content,
			"project_id":  projectID,
			"category_id": categoryID,
		}
	}
	a.render(c, "doc_edit.html", gin.H{
		"title":      "新建文档",
		"doc":        nil,
		"share":      nil,
		"canEditDoc": true, // 新建页无权限问题，保存按钮始终可见
		"projects":   projects,
		"categories": categories,
		"templates":  templates,
		"tpl":        tplData,
		"tplWarn":    tplWarn,
	})
}

// EditDocPage 编辑文档页
func (a *App) EditDocPage(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	var share model.Share
	hasShare := a.DB.Where("document_id = ?", doc.ID).First(&share).Error == nil
	projects, categories := a.filterOptions(c)
	// 只读成员进入编辑页时仅可浏览：分享设置属主专属，前端据此隐藏入口
	user := middleware.CurrentUser(c)
	canEditDoc := user == nil || user.IsAdmin() || doc.OwnerID == user.ID ||
		a.projectRole(doc, user.ID) == model.MemberRoleEdit
	// M-1：若文档当前挂在可选列表之外的项目/分类（如 admin 分配的），补进下拉，避免 viewer 保存时丢归属
	if doc.ProjectID != 0 {
		found := false
		for _, p := range projects {
			if p.ID == doc.ProjectID {
				found = true
				break
			}
		}
		if !found {
			var cur model.Project
			if a.DB.First(&cur, doc.ProjectID).Error == nil {
				projects = append(projects, cur)
			}
		}
	}
	if doc.CategoryID != 0 {
		found := false
		for _, ct := range categories {
			if ct.ID == doc.CategoryID {
				found = true
				break
			}
		}
		if !found {
			var cur model.Category
			if a.DB.First(&cur, doc.CategoryID).Error == nil {
				categories = append(categories, cur)
			}
		}
	}
	// HTML 整站文档：进入替换/设置页（不可编辑正文）
	if doc.Type == model.DocTypeHTML {
		a.renderHTMLDocEdit(c, doc, shareOrNil(hasShare, &share), shareURL(c, &share, hasShare), canEditDoc, projects, categories)
		return
	}
	// 结构化画布（思维导图/画板/drawio 图表）：专用编辑页
	if model.IsCanvasType(doc.Type) {
		a.render(c, "json_edit.html", gin.H{
			"title":         doc.Title,
			"rawTitle":      true,
			"doc":           doc,
			"docKind":       doc.Type,
			"share":         shareOrNil(hasShare, &share),
			"shareURL":      shareURL(c, &share, hasShare),
			"canEditDoc":    canEditDoc,
			"isOwner":       user != nil && (user.IsAdmin() || user.ID == doc.OwnerID),
			"tplProjectId":  0,
			"tplCategoryId": 0,
		})
		return
	}
	a.render(c, "doc_edit.html", gin.H{
		"title":      "编辑文档",
		"doc":        doc,
		"share":      shareOrNil(hasShare, &share),
		"shareURL":   shareURL(c, &share, hasShare),
		"canEditDoc": canEditDoc,
		"projects":   projects,
		"categories": categories,
	})
}

func shareOrNil(ok bool, s *model.Share) *model.Share {
	if ok {
		return s
	}
	return nil
}

// renderHTMLDocEdit HTML 整站文档的编辑页（替换 / 标题与归属 / 分享设置）
func (a *App) renderHTMLDocEdit(c *gin.Context, doc *model.Document, share *model.Share, shareURL string, canEditDoc bool, projects []model.Project, categories []model.Category) {
	manifest, _ := parseHTMLManifest(doc.Content)
	// 整站替换与分享设置为属主级操作
	isOwner := false
	if user := middleware.CurrentUser(c); user != nil && (user.IsAdmin() || user.ID == doc.OwnerID) {
		isOwner = true
	}
	a.render(c, "html_edit.html", gin.H{
		"title":            doc.Title,
		"rawTitle":         true,
		"doc":              doc,
		"share":            share,
		"shareURL":         shareURL,
		"canEditDoc":       canEditDoc,
		"isOwner":          isOwner,
		"projects":         projects,
		"categories":       categories,
		"manifest":         manifest,
		"ShareLockCanEdit": true, // HTML 整站不可分享页编辑：隐藏「可编辑」选项
	})
}

func shareURL(c *gin.Context, s *model.Share, ok bool) string {
	if !ok {
		return ""
	}
	return scheme(c) + "://" + c.Request.Host + "/s/" + s.ShareToken
}

func scheme(c *gin.Context) string {
	if c.Request.TLS != nil {
		return "https"
	}
	return "http"
}

// loadDoc 按 id 加载文档，找不到时写响应并返回 nil；
// 可见范围：属主 / 管理员 / 挂项目文档的项目成员（写操作再由 requireDocEdit 细分角色）
func (a *App) loadDoc(c *gin.Context) *model.Document {
	id, _ := strconv.Atoi(c.Param("id"))
	var doc model.Document
	// Preload Owner：阅读预览/编辑页展示作者头像与昵称
	if err := a.DB.Preload("Owner").First(&doc, id).Error; err != nil {
		if isAjax(c) {
			c.JSON(http.StatusNotFound, gin.H{"error": errDocNotFound})
		} else {
			c.String(http.StatusNotFound, errDocNotFound)
		}
		return nil
	}
	if user := middleware.CurrentUser(c); user != nil && !user.IsAdmin() && doc.OwnerID != user.ID {
		if a.projectRole(&doc, user.ID) == "" {
			if isAjax(c) {
				c.JSON(http.StatusForbidden, gin.H{"error": errDocForbidden})
			} else {
				c.String(http.StatusForbidden, errDocForbidden)
			}
			return nil
		}
	}
	return &doc
}

// projectRole 用户在文档所属项目中的成员角色；""=非成员或文档未挂项目
func (a *App) projectRole(doc *model.Document, userID uint) string {
	if doc == nil || doc.ProjectID == 0 || userID == 0 {
		return ""
	}
	var m model.ProjectMember
	if err := a.DB.Where("project_id = ? AND user_id = ?", doc.ProjectID, userID).First(&m).Error; err != nil {
		return ""
	}
	return m.Role
}

// requireDocEdit 文档写操作权限（改/删/回滚/评论）：属主 / 管理员 / edit 角色项目成员；不通过时写 403
func (a *App) requireDocEdit(c *gin.Context, doc *model.Document) bool {
	user := middleware.CurrentUser(c)
	if user != nil && (user.IsAdmin() || doc.OwnerID == user.ID || a.projectRole(doc, user.ID) == model.MemberRoleEdit) {
		return true
	}
	if isAjax(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": errDocForbidden})
	} else {
		c.String(http.StatusForbidden, errDocForbidden)
	}
	return false
}

// requireDocOwner 文档属主级操作（分享设置、评论管理）：仅属主 / 管理员；不通过时写 403
func (a *App) requireDocOwner(c *gin.Context, doc *model.Document) bool {
	user := middleware.CurrentUser(c)
	if user != nil && (user.IsAdmin() || doc.OwnerID == user.ID) {
		return true
	}
	if isAjax(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": errDocForbidden})
	} else {
		c.String(http.StatusForbidden, errDocForbidden)
	}
	return false
}

func isAjax(c *gin.Context) bool {
	return c.GetHeader("X-Requested-With") == "XMLHttpRequest" ||
		c.GetHeader("Accept") == "application/json"
}

// openapiVia 更新来源标记：经开放平台密钥调用的请求返回 "api"，其余为空（Web 端）
func openapiVia(c *gin.Context) string {
	if c.GetBool("viaOpenAPI") {
		return "api"
	}
	return ""
}

type docReq struct {
	Title       string  `json:"title" form:"title"`
	Version     *string `json:"version" form:"version"`         // nil = 未传，不修改；传值 = 覆盖（含清空），最长 32 字符
	Content     *string `json:"content" form:"content"`         // nil = 未传：更新时不修改；传值 = 整体覆盖
	Type        string  `json:"type" form:"type"`               // 创建时可选：markdown（默认）/ mindmap / board / drawio；html 走专用上传接口
	ProjectID   *uint   `json:"project_id" form:"project_id"`   // nil 表示未传，不修改；0 表示清空
	CategoryID  *uint   `json:"category_id" form:"category_id"` // 同上
	BaseVersion *int64  `json:"base_version"`                   // 乐观锁：内容更新时校验，不一致返回 409
	Force       bool    `json:"force"`                          // true = 忽略版本冲突强制覆盖
}

// docVersionMaxRunes 业务版本号最大字符数，与 model.Document.Version 的 size:32 一致
const docVersionMaxRunes = 32

// normalizeVersion 去空白并按字符数校验版本号长度，超限返回 ok=false。
// 仅在调用方已判定 req.Version != nil 时使用
func normalizeVersion(v string) (string, bool) {
	trimmed := strings.TrimSpace(v)
	if utf8.RuneCountInString(trimmed) > docVersionMaxRunes {
		return "", false
	}
	return trimmed, true
}

// validProjectRef 项目引用校验：0 合法；与当前值相同（未改动）合法；否则必须存在且非 admin 只能用自己的
func (a *App) validProjectRef(c *gin.Context, projectID, current uint) bool {
	if projectID == 0 || projectID == current {
		return true
	}
	var project model.Project
	if a.DB.First(&project, projectID).Error != nil {
		return false
	}
	// 只能归属到自己的项目（admin 亦然）；与当前值相同（未改动）在入口处已放行
	if user := middleware.CurrentUser(c); user != nil && project.OwnerID != user.ID {
		return false
	}
	return true
}

// validCategoryRef 分类引用校验：0 合法；与当前值相同（未改动）合法；否则必须存在且只能用自己的（与 validProjectRef 同构）
func (a *App) validCategoryRef(c *gin.Context, categoryID, current uint) bool {
	if categoryID == 0 || categoryID == current {
		return true
	}
	var category model.Category
	if err := a.DB.First(&category, categoryID).Error; err != nil {
		return false
	}
	// 只能归属到自己的分类（admin 亦然）
	if user := middleware.CurrentUser(c); user != nil && category.OwnerID != user.ID {
		return false
	}
	return true
}

// CreateDoc 新建文档；标题留空时写入「未命名文档」。
// type 可选：markdown（默认）/ mindmap / board；html 整站走专用上传接口，此处拒绝。
func (a *App) CreateDoc(c *gin.Context) {
	var req docReq
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	docType := strings.TrimSpace(req.Type)
	if docType == "" {
		docType = model.DocTypeMarkdown
	}
	switch docType {
	case model.DocTypeMarkdown, model.DocTypeMindmap, model.DocTypeBoard, model.DocTypeDrawio:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的文档类型"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = a.tr(c, "edit.untitled")
	}
	version := ""
	if req.Version != nil {
		v, ok := normalizeVersion(*req.Version)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
			return
		}
		version = v
	}
	user := middleware.CurrentUser(c)
	content := ""
	if req.Content != nil {
		content = *req.Content
	}
	doc := model.Document{
		Title:       title,
		Version:     version,
		Slug:        util.RandomSlug(8),
		Type:        docType,
		Content:     content,
		OwnerID:     user.ID,
		UpdatedByID: user.ID,
		UpdatedVia:  openapiVia(c),
	}
	if req.ProjectID != nil {
		doc.ProjectID = *req.ProjectID
	}
	if req.CategoryID != nil {
		doc.CategoryID = *req.CategoryID
	}
	if !a.validProjectRef(c, doc.ProjectID, 0) || !a.validCategoryRef(c, doc.CategoryID, 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": errProjCatBad})
		return
	}
	for {
		var n int64
		a.DB.Model(&model.Document{}).Where("slug = ?", doc.Slug).Count(&n)
		if n == 0 {
			break
		}
		doc.Slug = util.RandomSlug(8)
	}
	if err := a.DB.Create(&doc).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": doc})
}

// UpdateDoc 更新文档
func (a *App) UpdateDoc(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocEdit(c, doc) {
		return
	}
	actor := middleware.CurrentUser(c)
	oldTitle, oldContent := doc.Title, doc.Content
	oldProj, oldCat := doc.ProjectID, doc.CategoryID
	oldVersion := doc.Version
	var req docReq
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	// 版本号：传值即覆盖（含清空），未传（nil）则不修改；超长直接 400
	if req.Version != nil {
		v, ok := normalizeVersion(*req.Version)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
			return
		}
		doc.Version = v
	}
	// 内容更新语义：未传 content = 不修改（防止 MCP 等客户端只改标题时清空正文/画布）；
	// 显式传值 = 整体覆盖。HTML 整站的内容（manifest）只经整站替换接口变更。
	var newContent *string
	if req.Content != nil {
		if doc.Type == model.DocTypeHTML {
			if *req.Content != doc.Content {
				c.JSON(http.StatusBadRequest, gin.H{"error": "HTML 整站文档不支持在线编辑内容，请使用整站替换"})
				return
			}
		} else {
			newContent = req.Content
		}
	}
	if req.Title != "" {
		doc.Title = strings.TrimSpace(req.Title)
	}
	if newContent != nil {
		doc.Content = *newContent
	}
	// 指针语义：未传 = 不修改，传 0 = 清空；项目未改动时跳过归属校验（M-1：避免 viewer 保存他人项目下的文档被拒）
	if req.ProjectID != nil && *req.ProjectID != doc.ProjectID {
		if !a.validProjectRef(c, *req.ProjectID, doc.ProjectID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": errProjRefBad})
			return
		}
		doc.ProjectID = *req.ProjectID
	}
	if req.CategoryID != nil && *req.CategoryID != doc.CategoryID {
		if !a.validCategoryRef(c, *req.CategoryID, doc.CategoryID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": errCatRefBad})
			return
		}
		doc.CategoryID = *req.CategoryID
	}
	// 乐观锁 + 字段更新 + 修订快照在同一事务：内容变化时带 content_version 守卫写入
	// （校验与更新是同一条 SQL，无竞态窗口），冲突返回 409；未传 base_version 的旧
	// 客户端 / MCP 不做前置校验，但仍受守卫保护——他人抢先提交时同样得到 409
	// 而非静默覆盖。守卫 UPDATE 同时是行锁获取点，与 RollbackRevision / 分享保存
	// 顺序统一，避免交错写入导致 pruneRevisions 误删对方刚写的修订。
	hasConflict := false
	conflictVersion := int64(0)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var fresh model.Document
		if err := tx.Select("id", "content", "content_version").First(&fresh, doc.ID).Error; err != nil {
			return err
		}
		updates := map[string]any{}
		if newContent != nil {
			if !req.Force && req.BaseVersion != nil && fresh.ContentVersion != *req.BaseVersion {
				hasConflict = true
				conflictVersion = fresh.ContentVersion
				return nil
			}
			updates["content"] = *newContent
			updates["content_version"] = fresh.ContentVersion + 1
		}
		if actor != nil {
			updates["updated_by_id"] = actor.ID
			updates["updated_via"] = openapiVia(c)
		}
		if doc.Title != oldTitle {
			updates["title"] = doc.Title
		}
		if doc.Version != oldVersion {
			updates["version"] = doc.Version
		}
		if doc.ProjectID != oldProj {
			updates["project_id"] = doc.ProjectID
		}
		if doc.CategoryID != oldCat {
			updates["category_id"] = doc.CategoryID
		}
		if len(updates) == 0 {
			return nil
		}
		q := tx.Model(&model.Document{}).Where("id = ?", doc.ID)
		if newContent != nil {
			q = q.Where("content_version = ?", fresh.ContentVersion)
		}
		res := q.Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if newContent != nil && res.RowsAffected == 0 {
			// 守卫落空 = 未传 base_version 的写入者也撞上了并发提交；
			// 此时事务内读到的版本已过期，conflictVersion 置 0 由响应前回读
			hasConflict = true
			return nil
		}
		// 内容实际变化时同事务快照覆盖前正文（历史版本）；仅改标题/归属不产生修订
		if newContent != nil && *newContent != fresh.Content {
			editorID, editorName := uint(0), "系统"
			if actor != nil {
				editorID, editorName = actor.ID, actor.DisplayName()
			}
			if err := tx.Create(&model.DocumentRevision{
				DocumentID: doc.ID, EditorID: editorID, EditorName: editorName, Content: fresh.Content,
			}).Error; err != nil {
				return err
			}
			return pruneRevisions(tx, doc.ID)
		}
		return nil
	})
	if err != nil {
		log.Printf("[doc] 更新失败 doc=%d: %v", doc.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	if hasConflict {
		if conflictVersion == 0 {
			conflictVersion = a.currentContentVersion(doc.ID)
		}
		c.JSON(http.StatusConflict, gin.H{"error": "内容已被其他人修改", "current_version": conflictVersion})
		return
	}
	// 回读最新状态（版本号/并发写入者的其他字段）用于响应
	if err := a.DB.First(doc, doc.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	if actor != nil && (doc.Title != oldTitle || doc.Content != oldContent) {
		a.notifyDocUpdated(doc, actor.DisplayName(), actor.ID)
	}
	c.JSON(http.StatusOK, gin.H{"data": doc})
}

// DeleteDoc 删除文档及其分享配置
func (a *App) DeleteDoc(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocEdit(c, doc) {
		return
	}
	// HTML 整站：后台清理存储中的站点文件
	a.cleanupHTMLDoc(doc)
	if err := a.DB.Where("document_id = ?", doc.ID).Delete(&model.Share{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errDeleteFail})
		return
	}
	if err := a.DB.Where("document_id = ?", doc.ID).Delete(&model.ShareAccessRequest{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errDeleteFail})
		return
	}
	if err := a.DB.Delete(&model.Document{}, doc.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errDeleteFail})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type shareReq struct {
	Enabled        bool   `json:"enabled" form:"enabled"`
	CanEdit        bool   `json:"can_edit" form:"can_edit"`               // 登录用户可编辑
	Password       string `json:"password" form:"password"`               // 非空 = 设置密码；空 = 不修改（新建时无密码）
	RemovePassword bool   `json:"remove_password" form:"remove_password"` // true = 清除已设密码（优先级低于 Password 非空）
	ExpireDays     int    `json:"expire_days" form:"expire_days"`         // 0 表示永不过期
}

// ---- 编辑占用心跳（HTTP 轮询）：提示他人正在编辑同一文档 ----

const editPresenceTTL = 30 * time.Second

type editEntry struct {
	name string
	at   time.Time
}

var editPresence = struct {
	sync.Mutex
	m map[uint]map[uint]editEntry // docID -> userID -> 心跳
}{m: map[uint]map[uint]editEntry{}}

// MarkEditing 编辑页心跳：POST /console/api/docs/:id/editing
// 登记本人心跳并返回其他正在编辑者的名字（30 秒内心跳有效）
func (a *App) MarkEditing(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	now := time.Now()
	editPresence.Lock()
	entries, ok := editPresence.m[doc.ID]
	if !ok {
		entries = map[uint]editEntry{}
		editPresence.m[doc.ID] = entries
	}
	for uid, e := range entries { // 清理过期心跳
		if now.Sub(e.at) > editPresenceTTL {
			delete(entries, uid)
		}
	}
	entries[user.ID] = editEntry{name: user.DisplayName(), at: now}
	var others []string
	for uid, e := range entries {
		if uid != user.ID {
			others = append(others, e.name)
		}
	}
	editPresence.Unlock()
	c.JSON(http.StatusOK, gin.H{"editors": others})
}

// PreviewDoc 管理端阅读预览：作者/管理员/项目成员以读者视图查看文档，
// 不要求开启分享、不写分享凭证、不计浏览数
func (a *App) PreviewDoc(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var doc model.Document
	// Preload Owner：阅读页展示作者信息
	if err := a.DB.Preload("Owner").First(&doc, id).Error; err != nil {
		c.String(http.StatusNotFound, "文档不存在")
		return
	}
	user := middleware.CurrentUser(c)
	allowed := user != nil && (user.IsAdmin() || doc.OwnerID == user.ID)
	// 项目成员（view/edit 角色）可读属主文档
	if !allowed && user != nil && doc.ProjectID != 0 {
		var n int64
		a.DB.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", doc.ProjectID, user.ID).Count(&n)
		allowed = n > 0
	}
	if !allowed {
		c.String(http.StatusForbidden, "无权查看该文档")
		return
	}
	// 结构化画布（思维导图/画板/drawio 图表）：预览页固定只读（编辑走 /edit 页）
	if model.IsCanvasType(doc.Type) {
		a.render(c, "json_view.html", gin.H{
			"title":        doc.Title,
			"rawTitle":     true,
			"doc":          doc,
			"docKind":      doc.Type,
			"user":         user,
			"shareCanEdit": false,
			"token":        "",
		})
		return
	}
	// HTML 整站：iframe sandbox 渲染，入口用短时签名（无需会话 cookie，子资源同前缀继承）
	if doc.Type == model.DocTypeHTML {
		a.render(c, "html_view.html", gin.H{
			"title":       doc.Title,
			"rawTitle":    true, // 用户文档标题，跳过词典反查避免误译
			"doc":         doc,
			"share":       nil,
			"token":       "",
			"user":        user,
			"canModerate": user != nil && (user.ID == doc.OwnerID || user.IsAdmin()),
			"entryURL":    a.adminEntryURL(doc.ID),
			// 预览承诺“不计浏览数”，只读展示最近访客，不写访客记录
			"visitors": a.recentVisitors(doc.ID),
		})
		return
	}
	a.render(c, "share_view.html", gin.H{
		"title":       doc.Title,
		"rawTitle":    true, // 用户文档标题，跳过词典反查避免误译
		"doc":         doc,
		"share":       nil,
		"token":       "",
		"user":        user,
		"canEdit":     false, // 编辑走后台编辑器，预览页只读
		"canModerate": user != nil && (user.ID == doc.OwnerID || user.IsAdmin()),
		// 预览承诺“不计浏览数”，只读展示最近访客，不写访客记录
		"visitors": a.recentVisitors(doc.ID),
	})
}

// UpsertShare 开启/更新文档分享（属主级操作：分享一经开启即对公网可见）
func (a *App) UpsertShare(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocOwner(c, doc) {
		return
	}
	var req shareReq
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	var share model.Share
	found := a.DB.Where("document_id = ?", doc.ID).First(&share).Error == nil

	if !req.Enabled {
		// 关闭分享
		if found {
			a.DB.Delete(&share)
			a.DB.Where("document_id = ?", doc.ID).Delete(&model.ShareAccessRequest{})
		}
		doc.IsShared = false
		a.DB.Save(doc)
		c.JSON(http.StatusOK, gin.H{"ok": true, "enabled": false})
		return
	}

	if !found {
		share = model.Share{DocumentID: doc.ID, ShareToken: util.RandomHex(16)}
	}
	share.CanEdit = req.CanEdit // 编辑权限每次按当前设置覆盖
	// 密码：非空 = 设置密码；空 + remove_password = 清除已设密码；空 = 不修改（新建时无密码）
	if req.Password != "" {
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		share.Password = string(hash)
	} else if req.RemovePassword {
		share.Password = ""
	} else if !found {
		share.Password = ""
	}
	// 有效期
	if req.ExpireDays > 0 {
		t := time.Now().Add(time.Duration(req.ExpireDays) * 24 * time.Hour)
		share.ExpireAt = &t
	} else {
		share.ExpireAt = nil
	}

	if err := a.DB.Save(&share).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "分享设置失败"})
		return
	}
	doc.IsShared = true
	a.DB.Save(doc)

	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"enabled":     true,
		"url":         a.siteBaseURL(c) + "/s/" + share.ShareToken,
		"hasPassword": share.HasPassword(),
	})
}

// DuplicateDoc 复制文档（编辑权限）：复制标题（加「副本」后缀）/类型/内容/归属，
// 不复制分享配置与浏览数据，新文档默认未分享。
// 归属跟随复制者裁剪：项目仅当复制者拥有或为成员时保留，分类仅当复制者拥有时保留
// ——与 CreateDoc/UpdateDoc 的 validProjectRef/validCategoryRef 约定一致
func (a *App) DuplicateDoc(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocEdit(c, doc) {
		return
	}
	user := middleware.CurrentUser(c)
	newProjectID := doc.ProjectID
	if newProjectID != 0 {
		var proj model.Project
		if a.DB.First(&proj, newProjectID).Error != nil {
			newProjectID = 0
		} else if proj.OwnerID != user.ID {
			var n int64
			a.DB.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", newProjectID, user.ID).Count(&n)
			if n == 0 {
				newProjectID = 0
			}
		}
	}
	newCategoryID := doc.CategoryID
	if newCategoryID != 0 {
		var cat model.Category
		if a.DB.First(&cat, newCategoryID).Error != nil || cat.OwnerID != user.ID {
			newCategoryID = 0
		}
	}
	r := []rune(doc.Title)
	if len(r) > 250 {
		r = r[:250]
	}
	newDoc := model.Document{
		Title:       string(r) + "（副本）",
		Version:     doc.Version,
		Slug:        util.RandomSlug(8),
		Type:        doc.Type,
		Content:     doc.Content,
		OwnerID:     user.ID,
		UpdatedByID: user.ID,
		UpdatedVia:  openapiVia(c),
		ProjectID:   newProjectID,
		CategoryID:  newCategoryID,
	}
	for {
		var n int64
		a.DB.Model(&model.Document{}).Where("slug = ?", newDoc.Slug).Count(&n)
		if n == 0 {
			break
		}
		newDoc.Slug = util.RandomSlug(8)
	}
	if err := a.DB.Create(&newDoc).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "复制失败"})
		return
	}
	// HTML 整站：站点文件按原文档前缀存储，需复制到新前缀并重写 manifest，
	// 否则删除原文档或整站替换时会连坐副本（站点文件 404）
	if doc.Type == model.DocTypeHTML {
		if err := a.duplicateHTMLSite(c, doc, &newDoc); err != nil {
			a.DB.Delete(&model.Document{}, newDoc.ID)
			log.Printf("[doc] 复制 HTML 整站失败 doc=%d -> %d: %v", doc.ID, newDoc.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "复制站点文件失败"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": newDoc.ID, "title": newDoc.Title}})
}

// DeleteShare 关闭分享（属主级操作）
func (a *App) DeleteShare(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocOwner(c, doc) {
		return
	}
	a.DB.Where("document_id = ?", doc.ID).Delete(&model.Share{})
	a.DB.Where("document_id = ?", doc.ID).Delete(&model.ShareAccessRequest{})
	doc.IsShared = false
	a.DB.Save(doc)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// contentHasFence 判断 Markdown 是否含指定语言的围栏块（行首 ```lang）
func contentHasFence(content, lang string) bool {
	if content == "" || lang == "" {
		return false
	}
	needle := "```" + lang
	for i := 0; i < len(content); {
		j := strings.Index(content[i:], needle)
		if j < 0 {
			return false
		}
		abs := i + j
		atLineStart := abs == 0 || content[abs-1] == '\n'
		rest := abs + len(needle)
		okTail := rest >= len(content) || content[rest] == '\n' || content[rest] == '\r' || content[rest] == ' ' || content[rest] == '\t'
		if atLineStart && okTail {
			return true
		}
		i = abs + 1
	}
	return false
}

// detectEmbedTags 按当前页文档 ID 用 LIKE 探测围栏，不把 Content 拉进列表实体
func detectEmbedTags(db *gorm.DB, docs []model.Document) map[uint][]string {
	out := make(map[uint][]string, len(docs))
	if db == nil || len(docs) == 0 {
		return out
	}
	ids := make([]uint, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	mark := func(lang, tag string) {
		var hit []uint
		// 只取 id：LIKE 在服务端过滤，避免 SELECT content
		db.Model(&model.Document{}).
			Where("id IN ? AND (content LIKE ? OR content LIKE ?)", ids, "```"+lang+"%", "%\n```"+lang+"%").
			Pluck("id", &hit)
		for _, id := range hit {
			out[id] = append(out[id], tag)
		}
	}
	mark("mindmap", "docs.tagMindmap")
	mark("excalidraw", "docs.tagBoard")
	mark("drawio", "docs.tagDrawio")
	return out
}
