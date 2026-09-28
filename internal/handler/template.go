package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 文档模板相关错误文案（经 middleware.I18n 按词典翻译）
const (
	errTplIDBad      = "模板 id 非法"
	errTplNotFound   = "模板不存在"
	errTplForbidden  = "无权操作他人的模板"
	errTplNameEmpty  = "模板名称不能为空"
	errTplNameLong   = "模板名称不能超过 100 字"
	errTplTypeBad    = "不支持的模板类型（HTML 整站不支持模板）"
	errTplContentBig = "模板内容过大（超过 2MB）"
)

const tplContentMax = 2 << 20 // 2MB，与开放平台请求体上限对齐

// visibleTemplateScope 模板可见范围：admin 全量；
// 普通用户可见「自己创建的」+「绑定到自己参与项目（属主或成员）的」
func (a *App) visibleTemplateScope(c *gin.Context) *gorm.DB {
	tx := a.DB.Model(&model.DocTemplate{})
	user := middleware.CurrentUser(c)
	if user == nil || user.IsAdmin() {
		return tx
	}
	return tx.Where(
		"owner_id = ? OR project_id IN (SELECT project_id FROM project_members WHERE user_id = ?)",
		user.ID, user.ID)
}

// canUseTemplate 模板可见性判断（属主 / admin / 绑定项目的成员）
func (a *App) canUseTemplate(c *gin.Context, tpl *model.DocTemplate) bool {
	user := middleware.CurrentUser(c)
	if user == nil {
		return false
	}
	if user.IsAdmin() || tpl.OwnerID == user.ID {
		return true
	}
	if tpl.ProjectID == 0 {
		return false
	}
	var n int64
	a.DB.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", tpl.ProjectID, user.ID).Count(&n)
	return n > 0
}

// loadTemplate 按 id 加载模板（不做权限校验）
func (a *App) loadTemplate(c *gin.Context) *model.DocTemplate {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": errTplIDBad})
		return nil
	}
	var tpl model.DocTemplate
	if err := a.DB.First(&tpl, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": errTplNotFound})
		return nil
	}
	return &tpl
}

// loadTemplateOwned 按 id 加载模板并做属主校验（编辑/删除仅属主或 admin）
func (a *App) loadTemplateOwned(c *gin.Context) *model.DocTemplate {
	tpl := a.loadTemplate(c)
	if tpl == nil {
		return nil
	}
	if user := middleware.CurrentUser(c); user != nil && !user.IsAdmin() && tpl.OwnerID != user.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": errTplForbidden})
		return nil
	}
	return tpl
}

// TemplatesPage 模板管理页：属主/绑定项目的成员可见，admin 全量
func (a *App) TemplatesPage(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	pg := parseWebPage(c)
	tx := a.visibleTemplateScope(c)
	if q != "" {
		tx = tx.Where("name LIKE ?", "%"+q+"%")
	}
	var total int64
	tx.Count(&total)
	pg = pg.withTotal(total)

	var templates []model.DocTemplate
	tx.Preload("Owner").Preload("Project").Preload("Category").
		Order("updated_at desc").Offset(pg.Offset).Limit(pg.Size).Find(&templates)

	extra := ""
	if q != "" {
		extra += "&q=" + url.QueryEscape(q)
	}
	data := gin.H{
		"title":     "模板管理",
		"templates": templates,
		"q":         q,
	}
	for k, v := range pagerFields(pg, "/console/templates", extra) {
		data[k] = v
	}
	a.render(c, "templates.html", data)
}

// TemplateOptions 可用模板下拉选项（新建文档页用，不含内容）
func (a *App) TemplateOptions(c *gin.Context) {
	var templates []model.DocTemplate
	a.visibleTemplateScope(c).Select("id, name, type, updated_at").Order("updated_at desc").Find(&templates)
	c.JSON(http.StatusOK, gin.H{"data": templates})
}

// tplOwnershipFor 模板创建文档时按当前用户计算可带入的归属：
// project 仅当用户是该项目的属主、category 仅当用户拥有该分类
// ——文档归属跟随作者的系统规则不变（validProjectRef/validCategoryRef）
func (a *App) tplOwnershipFor(c *gin.Context, tpl *model.DocTemplate) (projectID, categoryID uint) {
	user := middleware.CurrentUser(c)
	if user == nil {
		return 0, 0
	}
	if tpl.ProjectID != 0 {
		var proj model.Project
		if a.DB.First(&proj, tpl.ProjectID).Error == nil && proj.OwnerID == user.ID {
			projectID = tpl.ProjectID
		}
	}
	if tpl.CategoryID != 0 {
		var cat model.Category
		if a.DB.First(&cat, tpl.CategoryID).Error == nil && cat.OwnerID == user.ID {
			categoryID = tpl.CategoryID
		}
	}
	return projectID, categoryID
}

// TemplateApply 读取模板内容用于创建文档
func (a *App) TemplateApply(c *gin.Context) {
	tpl := a.loadTemplate(c)
	if tpl == nil {
		return
	}
	if !a.canUseTemplate(c, tpl) {
		c.JSON(http.StatusForbidden, gin.H{"error": errTplForbidden})
		return
	}
	projectID, categoryID := a.tplOwnershipFor(c, tpl)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"id":          tpl.ID,
		"name":        tpl.Name,
		"type":        tpl.Type,
		"content":     tpl.Content,
		"project_id":  projectID,
		"category_id": categoryID,
	}})
}

// tplEditorData 模板编辑器公共数据（新建/编辑共用；页面复用文档编辑器，无分享）
func (a *App) tplEditorData(tpl *model.DocTemplate) gin.H {
	data := gin.H{
		"title":     "新建模板",
		"doc":       nil, // 复用 doc_edit/json_edit：doc 为空即无分享/历史版本入口
		"share":     nil,
		"canEditDoc": true,
		"tplMode":   true,
		"tpl":       tpl,
	}
	if tpl != nil {
		data["title"] = "编辑模板"
	}
	return data
}

// TemplateNewPage 新建模板页：与新建文档同构，?type= 选择类型（html 整站不支持）
func (a *App) TemplateNewPage(c *gin.Context) {
	docType := c.DefaultQuery("type", model.DocTypeMarkdown)
	if docType != model.DocTypeMarkdown && !model.IsCanvasType(docType) {
		c.Redirect(http.StatusTemporaryRedirect, "/console/templates")
		return
	}
	if model.IsCanvasType(docType) {
		data := a.tplEditorData(nil)
		data["docKind"] = docType
		data["isOwner"] = false // 画布编辑页据此隐藏分享入口
		data["tplContent"] = ""
		data["tplProjectId"] = 0
		data["tplCategoryId"] = 0
		data["tplId"] = 0
		a.render(c, "json_edit.html", data)
		return
	}
	projects, categories := a.filterOptions(c)
	data := a.tplEditorData(nil)
	data["projects"] = projects
	data["categories"] = categories
	a.render(c, "doc_edit.html", data)
}

// TemplateEditPage 编辑模板页：仅属主 / admin，与编辑文档同构（无分享）
func (a *App) TemplateEditPage(c *gin.Context) {
	tpl := a.loadTemplateOwned(c)
	if tpl == nil {
		return
	}
	data := a.tplEditorData(tpl)
	data["tplId"] = tpl.ID
	if model.IsCanvasType(tpl.Type) {
		data["docKind"] = tpl.Type
		data["isOwner"] = false
		data["tplContent"] = tpl.Content
		projectID, categoryID := a.tplOwnershipFor(c, tpl)
		data["tplProjectId"] = projectID
		data["tplCategoryId"] = categoryID
		a.render(c, "json_edit.html", data)
		return
	}
	projects, categories := a.filterOptions(c)
	// 绑定值不在可选列表时补入，避免保存时丢归属（与 EditDocPage 的 M-1 处理同思路）
	if tpl.ProjectID != 0 {
		found := false
		for _, p := range projects {
			if p.ID == tpl.ProjectID {
				found = true
				break
			}
		}
		if !found {
			var cur model.Project
			if a.DB.First(&cur, tpl.ProjectID).Error == nil {
				projects = append(projects, cur)
			}
		}
	}
	if tpl.CategoryID != 0 {
		found := false
		for _, ct := range categories {
			if ct.ID == tpl.CategoryID {
				found = true
				break
			}
		}
		if !found {
			var cur model.Category
			if a.DB.First(&cur, tpl.CategoryID).Error == nil {
				categories = append(categories, cur)
			}
		}
	}
	data["projects"] = projects
	data["categories"] = categories
	a.render(c, "doc_edit.html", data)
}

type templateReq struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Content    *string `json:"content"` // nil = 未传（更新时不修改）
	ProjectID  *uint   `json:"project_id"`
	CategoryID *uint   `json:"category_id"`
}

// validateTemplateReq 名称/类型/内容校验；返回规范化后的值与错误文案
func validateTemplateReq(name, tplType string, content *string) (string, string, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", errTplNameEmpty
	}
	if len([]rune(name)) > 100 {
		return "", "", errTplNameLong
	}
	tplType = strings.TrimSpace(tplType)
	switch tplType {
	case model.DocTypeMarkdown, model.DocTypeMindmap, model.DocTypeBoard, model.DocTypeDrawio:
	default:
		return "", "", errTplTypeBad
	}
	if content != nil && len(*content) > tplContentMax {
		return "", "", errTplContentBig
	}
	return name, tplType, ""
}

// validateTemplateRef 模板归属校验：
// 项目可选，绑定后项目成员可共用该模板（属主或成员均可绑定）；
// 分类可选，仅能绑定自己的分类（与文档归属规则一致）
func (a *App) validateTemplateRef(c *gin.Context, projectID, categoryID uint) bool {
	user := middleware.CurrentUser(c)
	if user == nil {
		return false
	}
	if projectID != 0 {
		var proj model.Project
		if a.DB.First(&proj, projectID).Error != nil {
			return false
		}
		if proj.OwnerID != user.ID {
			var n int64
			a.DB.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", projectID, user.ID).Count(&n)
			if n == 0 {
				return false
			}
		}
	}
	if categoryID != 0 {
		var cat model.Category
		if a.DB.First(&cat, categoryID).Error != nil || cat.OwnerID != user.ID {
			return false
		}
	}
	return true
}

// CreateTemplate 新建模板（归属当前用户）
func (a *App) CreateTemplate(c *gin.Context) {
	var req templateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	name, tplType, errMsg := validateTemplateReq(req.Name, req.Type, req.Content)
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		return
	}
	user := middleware.CurrentUser(c)
	tpl := model.DocTemplate{Name: name, Type: tplType, OwnerID: user.ID}
	if req.Content != nil {
		tpl.Content = *req.Content
	}
	if req.ProjectID != nil {
		tpl.ProjectID = *req.ProjectID
	}
	if req.CategoryID != nil {
		tpl.CategoryID = *req.CategoryID
	}
	if !a.validateTemplateRef(c, tpl.ProjectID, tpl.CategoryID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": errProjCatBad})
		return
	}
	if err := a.DB.Create(&tpl).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errCreateFail})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": tpl})
}

// UpdateTemplate 修改模板（仅属主 / admin）
func (a *App) UpdateTemplate(c *gin.Context) {
	tpl := a.loadTemplateOwned(c)
	if tpl == nil {
		return
	}
	var req templateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	// 部分更新语义：name / type 未传（或空白）沿用现值，与接口文档「未传字段不修改」一致
	if strings.TrimSpace(req.Name) == "" {
		req.Name = tpl.Name
	}
	if strings.TrimSpace(req.Type) == "" {
		req.Type = tpl.Type
	}
	name, tplType, errMsg := validateTemplateReq(req.Name, req.Type, req.Content)
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		return
	}
	projectID, categoryID := tpl.ProjectID, tpl.CategoryID
	if req.ProjectID != nil {
		projectID = *req.ProjectID
	}
	if req.CategoryID != nil {
		categoryID = *req.CategoryID
	}
	if !a.validateTemplateRef(c, projectID, categoryID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": errProjCatBad})
		return
	}
	tpl.Name = name
	tpl.Type = tplType
	if req.Content != nil {
		tpl.Content = *req.Content
	}
	tpl.ProjectID = projectID
	tpl.CategoryID = categoryID
	if err := a.DB.Save(tpl).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errUpdateFail})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": tpl})
}

// DeleteTemplate 删除模板（不影响已创建的文档）
func (a *App) DeleteTemplate(c *gin.Context) {
	tpl := a.loadTemplateOwned(c)
	if tpl == nil {
		return
	}
	if err := a.DB.Delete(tpl).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errDeleteFail})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
