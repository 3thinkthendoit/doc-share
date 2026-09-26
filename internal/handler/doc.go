package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

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
		tx = tx.Where("title LIKE ? OR slug LIKE ?", like, like)
	}
	var total int64
	tx.Count(&total)
	pg = pg.withTotal(total)

	var docs []model.Document
	// 列表不拉 Content（longtext）；嵌入标签另用 LIKE 只取 id
	tx.Omit("Content").Preload("Owner").Preload("Share").Preload("Project").Preload("Category").
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
	for k, v := range pagerFields(pg, "/admin/docs", extra) {
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

// NewDocPage 新建文档页
func (a *App) NewDocPage(c *gin.Context) {
	projects, categories := a.filterOptions(c)
	a.render(c, "doc_edit.html", gin.H{
		"title":      "新建文档",
		"doc":        nil,
		"share":      nil,
		"canEditDoc": true, // 新建页无权限问题，保存按钮始终可见
		"projects":   projects,
		"categories": categories,
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

type docReq struct {
	Title      string `json:"title" form:"title"`
	Content    string `json:"content" form:"content"`
	ProjectID  *uint  `json:"project_id" form:"project_id"`   // nil 表示未传，不修改；0 表示清空
	CategoryID *uint  `json:"category_id" form:"category_id"` // 同上
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

// CreateDoc 新建文档；标题留空时写入「未命名文档」
func (a *App) CreateDoc(c *gin.Context) {
	var req docReq
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = a.tr(c, "edit.untitled")
	}
	user := middleware.CurrentUser(c)
	doc := model.Document{
		Title:   title,
		Slug:    util.RandomSlug(8),
		Content: req.Content,
		OwnerID: user.ID,
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
	var req docReq
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	if req.Title != "" {
		doc.Title = strings.TrimSpace(req.Title)
	}
	doc.Content = req.Content
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
	if err := a.DB.Save(doc).Error; err != nil {
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
	Enabled    bool   `json:"enabled" form:"enabled"`
	CanEdit    bool   `json:"can_edit" form:"can_edit"` // 登录用户可编辑
	Password   string `json:"password" form:"password"`
	ExpireDays int    `json:"expire_days" form:"expire_days"` // 0 表示永不过期
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

// MarkEditing 编辑页心跳：POST /admin/api/docs/:id/editing
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
	// 密码：留空表示不修改（已存在时）/ 无密码（新建时）
	if req.Password != "" {
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		share.Password = string(hash)
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
	return out
}
