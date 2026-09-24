package handler

import (
	"net/http"
	"strconv"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// DocsPage 文档列表页（搜索 + 分页）
func (a *App) DocsPage(c *gin.Context) {
	q := c.Query("q")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const size = 15

	tx := a.DB.Model(&model.Document{})
	// 非管理员仅可见自己的文档
	user := middleware.CurrentUser(c)
	if user != nil && !user.IsAdmin() {
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

	var docs []model.Document
	tx.Preload("Owner").Preload("Share").Preload("Project").Preload("Category").Order("updated_at desc").
		Offset((page - 1) * size).Limit(size).Find(&docs)

	totalPages := int((total + int64(size) - 1) / int64(size))

	projects, categories := a.filterOptions(c)
	a.render(c, "docs.html", gin.H{
		"title":      "文档管理",
		"docs":       docs,
		"q":          q,
		"page":       page,
		"total":      total,
		"totalPages": totalPages,
		"project":    c.Query("project"),
		"category":   c.Query("category"),
		"projects":   projects,
		"categories": categories,
	})
}

// filterOptions 当前用户可见的项目与分类（个人所有，admin 看全部；供列表筛选与编辑页下拉复用）
func (a *App) filterOptions(c *gin.Context) ([]model.Project, []model.Category) {
	var projects []model.Project
	var categories []model.Category
	pTx := a.DB.Model(&model.Project{})
	cTx := a.DB.Model(&model.Category{})
	if user := middleware.CurrentUser(c); user != nil && !user.IsAdmin() {
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
// 非管理员只能访问自己拥有的文档（覆盖编辑/删除/分享接口）
func (a *App) loadDoc(c *gin.Context) *model.Document {
	id, _ := strconv.Atoi(c.Param("id"))
	var doc model.Document
	if err := a.DB.First(&doc, id).Error; err != nil {
		if isAjax(c) {
			c.JSON(http.StatusNotFound, gin.H{"error": "文档不存在"})
		} else {
			c.String(http.StatusNotFound, "文档不存在")
		}
		return nil
	}
	if user := middleware.CurrentUser(c); user != nil && !user.IsAdmin() && doc.OwnerID != user.ID {
		if isAjax(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权操作他人的文档"})
		} else {
			c.String(http.StatusForbidden, "无权操作他人的文档")
		}
		return nil
	}
	return &doc
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
	if user := middleware.CurrentUser(c); user != nil && !user.IsAdmin() && project.OwnerID != user.ID {
		return false
	}
	return true
}

// validCategoryRef 分类引用校验：0 合法；与当前值相同（未改动）合法；否则必须存在且非 admin 只能用自己的（与 validProjectRef 同构）
func (a *App) validCategoryRef(c *gin.Context, categoryID, current uint) bool {
	if categoryID == 0 || categoryID == current {
		return true
	}
	var category model.Category
	if err := a.DB.First(&category, categoryID).Error; err != nil {
		return false
	}
	if user := middleware.CurrentUser(c); user != nil && !user.IsAdmin() && category.OwnerID != user.ID {
		return false
	}
	return true
}

// CreateDoc 新建文档
func (a *App) CreateDoc(c *gin.Context) {
	var req docReq
	if err := c.ShouldBind(&req); err != nil || req.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题不能为空"})
		return
	}
	user := middleware.CurrentUser(c)
	doc := model.Document{
		Title:   req.Title,
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目或分类非法"})
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
	var req docReq
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Title != "" {
		doc.Title = req.Title
	}
	doc.Content = req.Content
	// 指针语义：未传 = 不修改，传 0 = 清空；项目未改动时跳过归属校验（M-1：避免 viewer 保存他人项目下的文档被拒）
	if req.ProjectID != nil && *req.ProjectID != doc.ProjectID {
		if !a.validProjectRef(c, *req.ProjectID, doc.ProjectID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "项目非法或无权归属到该项目"})
			return
		}
		doc.ProjectID = *req.ProjectID
	}
	if req.CategoryID != nil && *req.CategoryID != doc.CategoryID {
		if !a.validCategoryRef(c, *req.CategoryID, doc.CategoryID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分类非法或无权归属到该分类"})
			return
		}
		doc.CategoryID = *req.CategoryID
	}
	if err := a.DB.Save(doc).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": doc})
}

// DeleteDoc 删除文档及其分享配置
func (a *App) DeleteDoc(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if err := a.DB.Where("document_id = ?", doc.ID).Delete(&model.Share{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	if err := a.DB.Delete(&model.Document{}, doc.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type shareReq struct {
	Enabled    bool   `json:"enabled" form:"enabled"`
	Password   string `json:"password" form:"password"`
	ExpireDays int    `json:"expire_days" form:"expire_days"` // 0 表示永不过期
}

// PreviewDoc 管理端阅读预览：作者或管理员以读者视图查看文档，
// 不要求开启分享、不写分享凭证、不计浏览数
func (a *App) PreviewDoc(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	a.render(c, "share_view.html", gin.H{
		"title": doc.Title,
		"doc":   doc,
	})
}

// UpsertShare 开启/更新文档分享
func (a *App) UpsertShare(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
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
		}
		doc.IsShared = false
		a.DB.Save(doc)
		c.JSON(http.StatusOK, gin.H{"ok": true, "enabled": false})
		return
	}

	if !found {
		share = model.Share{DocumentID: doc.ID, ShareToken: util.RandomHex(16)}
	}
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

// DeleteShare 关闭分享
func (a *App) DeleteShare(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	a.DB.Where("document_id = ?", doc.ID).Delete(&model.Share{})
	doc.IsShared = false
	a.DB.Save(doc)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
