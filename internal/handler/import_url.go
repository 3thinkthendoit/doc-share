package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/util"
	"doc-share/internal/webfetch"

	"github.com/gin-gonic/gin"
)

const maxImportURLTitleRunes = 200

// importTimeout 页面抓取和图片下载的总时限。控制台后台任务与智能体、开放接口共用。
const importTimeout = 3 * time.Minute

// importPendingMark 控制台导入占位正文。后台写回时用整段占位做条件，避免覆盖用户已改的内容。
const importPendingMark = "正在从链接导入，完成后正文会自动出现。"

// ImportDocFromURL 从公网链接抓取可读正文，创建为可编辑的 Markdown 文档。
// POST /console/api/docs/from-url 与 POST /openapi/v1/docs/from-url
// JSON: {url, title?, project_id?, category_id?}
func (a *App) ImportDocFromURL(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	var req struct {
		URL        string `json:"url" binding:"required"`
		Title      string `json:"title"`
		ProjectID  *uint  `json:"project_id"`
		CategoryID *uint  `json:"category_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	var projectID, categoryID uint
	if req.ProjectID != nil {
		projectID = *req.ProjectID
	}
	if req.CategoryID != nil {
		categoryID = *req.CategoryID
	}
	if !a.validProjectRef(c, projectID, 0) || !a.validCategoryRef(c, categoryID, 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": errProjCatBad})
		return
	}
	if err := webfetch.CheckPublicURL(req.URL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 控制台先落文档再后台抓取，避免图片下载堵住保存请求。开放接口仍同步返回成稿。
	if !c.GetBool("viaOpenAPI") {
		doc, err := a.createPendingImportDoc(user, req.URL, req.Title, a.tr(c, "edit.untitled"), c.ClientIP(), projectID, categoryID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		go a.finishImportFromURL(doc.ID, req.URL, strings.TrimSpace(req.Title), doc.Title)
		c.JSON(http.StatusOK, gin.H{"data": doc, "importing": true})
		return
	}
	doc, err := a.createDocFromPublicURL(c.Request.Context(), user, req.URL, req.Title, a.tr(c, "edit.untitled"), openapiVia(c), c.ClientIP(), projectID, categoryID)
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "创建失败" {
			status = http.StatusInternalServerError
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": doc})
}

// createDocFromPublicURL 抓取公网页面并落成可编辑 Markdown。调用方负责登录态与项目/分类归属校验。
func (a *App) createDocFromPublicURL(ctx context.Context, user *model.User, rawURL, title, untitled, via, clientIP string, projectID, categoryID uint) (*model.Document, error) {
	if user == nil {
		return nil, fmt.Errorf("未登录")
	}
	page, err := webfetch.Read(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = strings.TrimSpace(page.Title)
	}
	if title == "" {
		title = untitled
	}
	if title == "" {
		title = "未命名文档"
	}
	if utf8.RuneCountInString(title) > maxImportURLTitleRunes {
		title = string([]rune(title)[:maxImportURLTitleRunes])
	}
	body := strings.TrimSpace(page.Markdown)
	if body == "" {
		body = page.Text
	} else {
		localized, locErr := a.localizeMarkdownImages(ctx, body, page.FinalURL)
		if locErr != nil {
			return nil, fmt.Errorf("导入未完成：%w", locErr)
		}
		body = localized
	}
	doc := model.Document{
		Title:       title,
		Version:     derivedDocVersion(0),
		Slug:        util.RandomSlug(8),
		Type:        model.DocTypeMarkdown,
		Content:     formatImportedMarkdown(page.FinalURL, page.Title, body),
		OwnerID:     user.ID,
		UpdatedByID: user.ID,
		UpdatedVia:  via,
		ProjectID:   projectID,
		CategoryID:  categoryID,
	}
	stampCreatorGeo(&doc, clientIP)
	for {
		var n int64
		a.DB.Unscoped().Model(&model.Document{}).Where("slug = ?", doc.Slug).Count(&n)
		if n == 0 {
			break
		}
		doc.Slug = util.RandomSlug(8)
	}
	if err := a.DB.Create(&doc).Error; err != nil {
		return nil, fmt.Errorf("创建失败")
	}
	a.reindexDocAsync(doc.ID)
	return &doc, nil
}

func importPendingMarkdown(rawURL string) string {
	return importPendingMark + "\n\n> 来源：" + strings.TrimSpace(rawURL) + "\n"
}

func (a *App) createPendingImportDoc(user *model.User, rawURL, title, untitled, clientIP string, projectID, categoryID uint) (*model.Document, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = untitled
	}
	if title == "" {
		title = "未命名文档"
	}
	if utf8.RuneCountInString(title) > maxImportURLTitleRunes {
		title = string([]rune(title)[:maxImportURLTitleRunes])
	}
	doc := model.Document{
		Title:       title,
		Version:     derivedDocVersion(0),
		Slug:        util.RandomSlug(8),
		Type:        model.DocTypeMarkdown,
		Content:     importPendingMarkdown(rawURL),
		OwnerID:     user.ID,
		UpdatedByID: user.ID,
		ProjectID:   projectID,
		CategoryID:  categoryID,
	}
	stampCreatorGeo(&doc, clientIP)
	for {
		var n int64
		a.DB.Unscoped().Model(&model.Document{}).Where("slug = ?", doc.Slug).Count(&n)
		if n == 0 {
			break
		}
		doc.Slug = util.RandomSlug(8)
	}
	if err := a.DB.Create(&doc).Error; err != nil {
		return nil, fmt.Errorf("创建失败")
	}
	return &doc, nil
}

// blockImportContentWrite 判断这次正文写入会不会盖掉链接导入。
// 库里仍是占位正文时，后台写回优先。客户端还拿着占位正文时，不能盖掉已经导入的成稿。
func blockImportContentWrite(stored, incoming string) bool {
	if strings.Contains(stored, importPendingMark) {
		return true
	}
	return strings.Contains(incoming, importPendingMark)
}

// finishImportFromURL 在后台抓取页面、转成 Markdown 并下载图片，再写回仍是占位正文的文档。
func (a *App) finishImportFromURL(docID uint, rawURL, userTitle, initialTitle string) {
	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	defer cancel()
	pending := importPendingMarkdown(rawURL)
	page, err := webfetch.Read(ctx, rawURL)
	content := ""
	pageTitle := ""
	if err != nil {
		content = "导入失败：" + err.Error() + "\n\n> 来源：" + strings.TrimSpace(rawURL) + "\n"
	} else {
		body := strings.TrimSpace(page.Markdown)
		if body == "" {
			body = page.Text
		} else if localized, locErr := a.localizeMarkdownImages(ctx, body, page.FinalURL); locErr == nil {
			body = localized
		} else if localized != "" {
			body = localized
		}
		content = formatImportedMarkdown(page.FinalURL, page.Title, body)
		if userTitle == "" {
			pageTitle = strings.TrimSpace(page.Title)
		}
	}
	var cur model.Document
	if err := a.DB.Select("id", "content", "content_version").First(&cur, docID).Error; err != nil {
		return
	}
	if cur.Content != pending {
		return
	}
	next := cur.ContentVersion + 1
	res := a.DB.Model(&model.Document{}).
		Where("id = ? AND content = ? AND content_version = ?", docID, pending, cur.ContentVersion).
		Updates(map[string]any{
			"content":         content,
			"content_version": next,
			"version":         derivedDocVersion(next),
		})
	if res.Error != nil || res.RowsAffected == 0 {
		return
	}
	if pageTitle != "" {
		if utf8.RuneCountInString(pageTitle) > maxImportURLTitleRunes {
			pageTitle = string([]rune(pageTitle)[:maxImportURLTitleRunes])
		}
		a.DB.Model(&model.Document{}).Where("id = ? AND title = ?", docID, initialTitle).Update("title", pageTitle)
	}
	a.reindexDocAsync(docID)
}

// ImportDocStatus 给编辑页轮询后台导入结果。
// GET /console/api/docs/:id/import
func (a *App) ImportDocStatus(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"importing":       strings.Contains(doc.Content, importPendingMark),
		"title":           doc.Title,
		"content":         doc.Content,
		"version":         doc.Version,
		"content_version": doc.ContentVersion,
	}})
}

// formatImportedMarkdown 把抓取结果整理成可继续编辑的 Markdown。
func formatImportedMarkdown(sourceURL, pageTitle, text string) string {
	body := strings.TrimSpace(text)
	var b strings.Builder
	title := strings.TrimSpace(pageTitle)
	if title != "" && !strings.HasPrefix(body, "# ") {
		b.WriteString("# ")
		b.WriteString(title)
		b.WriteString("\n\n")
	}
	if sourceURL != "" {
		b.WriteString(fmt.Sprintf("> 来源：%s\n\n", sourceURL))
	}
	b.WriteString(body)
	b.WriteByte('\n')
	return b.String()
}
