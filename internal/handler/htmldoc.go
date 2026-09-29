// Package handler — HTML 整站文档：上传 zip / 文件夹 → 解包写入存储，
// 分享与预览经 iframe sandbox 隔离渲染，不支持在线编辑，替换即整站替换。
package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"path"
	"sort"
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
	// htmlSigTTL 整站文件访问签名有效期（每次打开页面重新签发）
	htmlSigTTL = 2 * time.Hour
	// htmlEntryName 整站入口文件（站点根必须存在）
	htmlEntryName = "index.html"
)

// HTMLSiteManifest html 整站文档的 Content 存储格式（manifest JSON）
type HTMLSiteManifest struct {
	Prefix    string   `json:"prefix"` // 存储前缀 html/{docID}/{rand}/
	Entry     string   `json:"entry"`  // 入口文件，固定 index.html
	Files     []string `json:"files"`  // 站点内全部文件（相对路径）
	Size      int64    `json:"size"`   // 解包后总大小（字节）
	Count     int      `json:"count"`  // 文件数
	UpdatedAt string   `json:"updated_at"`
}

// parseHTMLManifest 解析文档 Content 中的 manifest
func parseHTMLManifest(content string) (*HTMLSiteManifest, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("manifest 为空")
	}
	var m HTMLSiteManifest
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return nil, fmt.Errorf("manifest 解析失败: %w", err)
	}
	if m.Prefix == "" || m.Entry == "" || len(m.Files) == 0 {
		return nil, fmt.Errorf("manifest 不完整")
	}
	return &m, nil
}

func (m *HTMLSiteManifest) fileSet() map[string]struct{} {
	set := make(map[string]struct{}, len(m.Files))
	for _, f := range m.Files {
		set[f] = struct{}{}
	}
	return set
}

func (m *HTMLSiteManifest) toJSON() (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// htmlSiteKeys manifest 展开为存储 key 列表
func htmlSiteKeys(m *HTMLSiteManifest) []string {
	keys := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		keys = append(keys, m.Prefix+f)
	}
	return keys
}

// ---- 站点文件收集与校验 ----

type siteFile struct {
	name string // 站点内相对路径（/ 分隔）
	data []byte
}

// validSitePath 站点内相对路径合法性：拒绝穿越 / 绝对路径 / 盘符
func validSitePath(p string) bool {
	if p == "" || p == "." || strings.HasPrefix(p, "/") {
		return false
	}
	if strings.Contains(p, "..") || strings.Contains(p, "//") || strings.Contains(p, "\\") {
		return false
	}
	if strings.ContainsAny(p, ":\x00") {
		return false
	}
	return true
}

// skipSiteFile 剔除打包垃圾：__MACOSX/、.DS_Store、Thumbs.db、macOS 资源叉（._*）
func skipSiteFile(name string) bool {
	base := path.Base(name)
	if strings.HasPrefix(name, "__MACOSX/") || base == "__MACOSX" {
		return true
	}
	if base == ".DS_Store" || base == "Thumbs.db" || strings.HasPrefix(base, "._") {
		return true
	}
	return false
}

// collectSiteFiles 从 multipart 提取整站文件：
// 单个 .zip（字段 file）或文件夹上传（多个 file 字段 + 一一对应的 path 字段）
func (a *App) collectSiteFiles(c *gin.Context) ([]siteFile, error) {
	maxBytes := int64(a.Cfg.Upload.HTMLMaxSizeMB) << 20
	maxFiles := a.Cfg.Upload.HTMLMaxFiles
	// 多文件 multipart 的边界与路径字段开销给足余量
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+(8<<20))

	form, err := c.MultipartForm()
	if err != nil {
		return nil, fmt.Errorf("读取上传失败（总量可能超过 %d MB 限制）", a.Cfg.Upload.HTMLMaxSizeMB)
	}
	fhs := form.File["file"]
	if len(fhs) == 0 {
		return nil, fmt.Errorf("未收到文件")
	}

	// zip 模式
	if len(fhs) == 1 && len(form.Value["path"]) == 0 && strings.EqualFold(path.Ext(fhs[0].Filename), ".zip") {
		return unzipSite(fhs[0], maxBytes, maxFiles)
	}

	// 文件夹模式：file[] 与 path[] 一一对应
	paths := form.Value["path"]
	if len(paths) != len(fhs) {
		return nil, fmt.Errorf("文件夹上传缺少与文件对应的路径信息")
	}
	out := make([]siteFile, 0, len(fhs))
	var total int64
	seen := make(map[string]bool, len(fhs))
	for i, fh := range fhs {
		name := path.Clean(strings.TrimPrefix(strings.ReplaceAll(paths[i], "\\", "/"), "/"))
		if name == "." || name == "" {
			return nil, fmt.Errorf("存在非法路径: %q", paths[i])
		}
		if skipSiteFile(name) {
			continue
		}
		if !validSitePath(name) {
			return nil, fmt.Errorf("存在非法路径: %q", paths[i])
		}
		if seen[name] {
			return nil, fmt.Errorf("存在重复路径: %q", name)
		}
		seen[name] = true
		if len(out)+1 > maxFiles {
			return nil, fmt.Errorf("文件数超过上限 %d", maxFiles)
		}
		src, err := fh.Open()
		if err != nil {
			return nil, fmt.Errorf("读取文件 %q 失败", name)
		}
		data, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
		src.Close()
		if err != nil {
			return nil, fmt.Errorf("读取文件 %q 失败", name)
		}
		total += int64(len(data))
		if total > maxBytes {
			return nil, fmt.Errorf("解包后总大小超过 %d MB 限制", a.Cfg.Upload.HTMLMaxSizeMB)
		}
		out = append(out, siteFile{name: name, data: data})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("未收到有效文件")
	}
	return out, nil
}

// unzipSite 解包 multipart 上传的 zip：逐条校验路径与累计大小（防 zip bomb / 路径穿越）
func unzipSite(fh *multipart.FileHeader, maxBytes int64, maxFiles int) ([]siteFile, error) {
	src, err := fh.Open()
	if err != nil {
		return nil, fmt.Errorf("读取 zip 失败")
	}
	defer src.Close()
	data, err := io.ReadAll(io.LimitReader(src, maxBytes+(8<<20)))
	if err != nil {
		return nil, fmt.Errorf("读取 zip 失败（可能超过大小限制）")
	}
	return unzipSiteBytes(data, maxBytes, maxFiles)
}

// unzipSiteBytes 从 zip 字节解包（multipart 上传与 openapi 共用管线）
func unzipSiteBytes(data []byte, maxBytes int64, maxFiles int) ([]siteFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("不是有效的 zip 文件")
	}
	out := make([]siteFile, 0, len(zr.File))
	var total int64
	seen := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue // 目录条目跳过
		}
		name := path.Clean(strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), "/"))
		if name == "." || name == "" {
			continue
		}
		if skipSiteFile(name) {
			continue
		}
		if !validSitePath(name) {
			return nil, fmt.Errorf("zip 内存在非法路径: %q", f.Name)
		}
		if seen[name] {
			return nil, fmt.Errorf("zip 内存在重复路径: %q", name)
		}
		seen[name] = true
		if len(out)+1 > maxFiles {
			return nil, fmt.Errorf("文件数超过上限 %d", maxFiles)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("解包 %q 失败", name)
		}
		buf, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("解包 %q 失败", name)
		}
		total += int64(len(buf))
		if total > maxBytes {
			return nil, fmt.Errorf("解包后总大小超过限制（%d MB）", maxBytes>>20)
		}
		out = append(out, siteFile{name: name, data: buf})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("zip 内没有有效文件")
	}
	return out, nil
}

// finalizeSiteFiles 入口约定：站点根必须有 index.html；
// 全部文件被单层顶层目录（如 dist/）包裹时自动剥落一层
func finalizeSiteFiles(files []siteFile) ([]siteFile, error) {
	has := func(name string) bool {
		for _, f := range files {
			if f.name == name {
				return true
			}
		}
		return false
	}
	if !has(htmlEntryName) {
		first := files[0].name
		i := strings.IndexByte(first, '/')
		if i < 0 {
			return nil, fmt.Errorf("站点根目录缺少 %s", htmlEntryName)
		}
		top := first[:i]
		all := true
		for _, f := range files {
			if !strings.HasPrefix(f.name, top+"/") {
				all = false
				break
			}
		}
		if !all {
			return nil, fmt.Errorf("站点根目录缺少 %s", htmlEntryName)
		}
		for i := range files {
			files[i].name = strings.TrimPrefix(files[i].name, top+"/")
		}
		if !has(htmlEntryName) {
			return nil, fmt.Errorf("站点根目录缺少 %s", htmlEntryName)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

// ---- 存储写入 / 清理 ----

// siteContentTypes 站点文件 Content-Type 白名单；未知扩展名一律 octet-stream
var siteContentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".txt":   "text/plain; charset=utf-8",
	".xml":   "application/xml; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".bmp":   "image/bmp",
	".ico":   "image/x-icon",
	".wasm":  "application/wasm",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".eot":   "application/vnd.ms-fontobject",
	".map":   "application/json; charset=utf-8",
	".csv":   "text/csv; charset=utf-8",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
	".ogv":   "video/ogg",
	".mp3":   "audio/mpeg",
	".ogg":   "audio/ogg",
	".wav":   "audio/wav",
	".pdf":   "application/pdf",
}

func siteContentType(name string) string {
	if ct, ok := siteContentTypes[strings.ToLower(path.Ext(name))]; ok {
		return ct
	}
	return "application/octet-stream"
}

// putSiteFiles 写入整站文件并生成 manifest；失败时返回已写入的 key 供调用方清理
func (a *App) putSiteFiles(ctx context.Context, prefix string, files []siteFile) (*HTMLSiteManifest, []string, error) {
	backend, err := a.fileStorage()
	if err != nil {
		return nil, nil, err
	}
	written := make([]string, 0, len(files))
	names := make([]string, 0, len(files))
	var total int64
	for _, f := range files {
		key := prefix + f.name
		if _, err := backend.Put(ctx, key, bytes.NewReader(f.data), int64(len(f.data)), siteContentType(f.name)); err != nil {
			return nil, written, fmt.Errorf("写入 %s 失败: %w", f.name, err)
		}
		written = append(written, key)
		names = append(names, f.name)
		total += int64(len(f.data))
	}
	m := &HTMLSiteManifest{
		Prefix:    prefix,
		Entry:     htmlEntryName,
		Files:     names,
		Size:      total,
		Count:     len(names),
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	return m, written, nil
}

// copyHTMLSiteFiles 把整站文件从旧前缀复制到新前缀（复制文档用）；任一失败即返回
func (a *App) copyHTMLSiteFiles(ctx context.Context, oldPrefix, newPrefix string, names []string) error {
	backend, err := a.fileStorage()
	if err != nil {
		return err
	}
	for _, name := range names {
		rc, size, err := backend.Get(ctx, oldPrefix+name)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", name, err)
		}
		_, err = backend.Put(ctx, newPrefix+name, rc, size, siteContentType(name))
		rc.Close()
		if err != nil {
			return fmt.Errorf("写入 %s 失败: %w", name, err)
		}
	}
	return nil
}

// duplicateHTMLSite 为复制的 HTML 整站文档复制站点文件，并把新文档 manifest 指向新前缀。
// 任一步失败由调用方删除新文档（本函数会尽力清理已复制文件）
func (a *App) duplicateHTMLSite(c *gin.Context, src *model.Document, newDoc *model.Document) error {
	m, err := parseHTMLManifest(src.Content)
	if err != nil {
		return err
	}
	newPrefix := fmt.Sprintf("html/%d/%s/", newDoc.ID, util.RandomSlug(8))
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()
	if err := a.copyHTMLSiteFiles(ctx, m.Prefix, newPrefix, m.Files); err != nil {
		// 部分文件已写入新前缀：尽力清理，避免残留孤儿站点文件
		a.deleteSiteFilesAsync(htmlSiteKeys(&HTMLSiteManifest{Prefix: newPrefix, Files: m.Files}))
		return err
	}
	m.Prefix = newPrefix
	content, err := m.toJSON()
	if err != nil {
		a.deleteSiteFilesAsync(htmlSiteKeys(m))
		return err
	}
	if err := a.DB.Model(&model.Document{}).Where("id = ?", newDoc.ID).Update("content", content).Error; err != nil {
		a.deleteSiteFilesAsync(htmlSiteKeys(m))
		return err
	}
	return nil
}

// deleteSiteFilesAsync 后台清理整站文件（替换旧版本 / 删除文档）
func (a *App) deleteSiteFilesAsync(keys []string) {
	if len(keys) == 0 {
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[htmldoc] 异步删除 panic: %v", rec)
			}
		}()
		backend, err := a.fileStorage()
		if err != nil {
			log.Printf("[htmldoc] 异步删除跳过（存储不可用）: %v", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for _, k := range keys {
			if err := backend.Delete(ctx, k); err != nil {
				log.Printf("[htmldoc] 异步删除失败 key=%s: %v", k, err)
			}
		}
	}()
}

// cleanupHTMLDoc 删除文档时清理站点文件（尽力而为）
func (a *App) cleanupHTMLDoc(doc *model.Document) {
	if doc == nil || doc.Type != model.DocTypeHTML {
		return
	}
	if m, err := parseHTMLManifest(doc.Content); err == nil {
		a.deleteSiteFilesAsync(htmlSiteKeys(m))
	}
}

// ---- API：创建 / 替换 ----

// createHTMLDocFromFiles 由站点文件清单建档并写入存储（multipart 与 openapi 共用）。
// 失败时负责清理已写入文件与已建文档记录。
func (a *App) createHTMLDocFromFiles(c *gin.Context, files []siteFile, title string, projectID, categoryID uint) (*model.Document, error) {
	user := middleware.CurrentUser(c)
	if user == nil {
		return nil, fmt.Errorf("未登录")
	}
	doc := model.Document{
		Title:       title,
		Version:     derivedDocVersion(0), // 版本号自动生成，从 v1.0.0 起算
		Slug:        util.RandomSlug(8),
		Type:        model.DocTypeHTML,
		OwnerID:     user.ID,
		UpdatedByID: user.ID,
		ProjectID:   projectID, CategoryID: categoryID,
	}
	for {
		var n int64
		// Unscoped：同时避开回收站文档占用的 slug，否则撞唯一索引导致插入失败
		a.DB.Unscoped().Model(&model.Document{}).Where("slug = ?", doc.Slug).Count(&n)
		if n == 0 {
			break
		}
		doc.Slug = util.RandomSlug(8)
	}
	if err := a.DB.Create(&doc).Error; err != nil {
		return nil, fmt.Errorf("创建失败")
	}
	// 失败补偿用 Unscoped 物理删除：刚建的残品不应进回收站成为可还原的幽灵文档
	prefix := fmt.Sprintf("html/%d/%s/", doc.ID, util.RandomSlug(8))
	m, written, err := a.putSiteFiles(c.Request.Context(), prefix, files)
	if err != nil {
		a.deleteSiteFilesAsync(written)
		a.DB.Unscoped().Delete(&model.Document{}, doc.ID)
		log.Printf("[htmldoc] 创建站点文件失败 doc=%d: %v", doc.ID, err)
		return nil, fmt.Errorf("保存站点文件失败")
	}
	content, err := m.toJSON()
	if err != nil {
		a.deleteSiteFilesAsync(written)
		a.DB.Unscoped().Delete(&model.Document{}, doc.ID)
		return nil, fmt.Errorf("生成站点清单失败")
	}
	if err := a.DB.Model(&model.Document{}).Where("id = ?", doc.ID).Update("content", content).Error; err != nil {
		a.deleteSiteFilesAsync(written)
		a.DB.Unscoped().Delete(&model.Document{}, doc.ID)
		return nil, fmt.Errorf("保存站点清单失败")
	}
	doc.Content = content
	return &doc, nil
}

// OpenCreateHTMLDoc 创建 HTML 整站文档（openapi）：POST /openapi/v1/docs/html。
// multipart 表单：title、project_id?、category_id?、file（zip）；解包校验与后台接口同管线。
func (a *App) OpenCreateHTMLDoc(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	files, err := a.collectSiteFiles(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	files, err = finalizeSiteFiles(files)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		title = a.tr(c, "edit.untitled")
	}
	var projectID, categoryID uint
	if v := c.PostForm("project_id"); v != "" {
		if pid, e := strconv.ParseUint(v, 10, 32); e == nil {
			projectID = uint(pid)
		}
	}
	if v := c.PostForm("category_id"); v != "" {
		if cid, e := strconv.ParseUint(v, 10, 32); e == nil {
			categoryID = uint(cid)
		}
	}
	if !a.validProjectRef(c, projectID, 0) || !a.validCategoryRef(c, categoryID, 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": errProjCatBad})
		return
	}
	doc, err := a.createHTMLDocFromFiles(c, files, title, projectID, categoryID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": doc})
}

// CreateHTMLDoc 创建 HTML 整站文档：POST /console/api/docs/html（multipart）
func (a *App) CreateHTMLDoc(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	files, err := a.collectSiteFiles(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	files, err = finalizeSiteFiles(files)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		title = a.tr(c, "edit.untitled")
	}
	var projectID, categoryID uint
	if v := c.PostForm("project_id"); v != "" {
		if pid, e := strconv.ParseUint(v, 10, 32); e == nil {
			projectID = uint(pid)
		}
	}
	if v := c.PostForm("category_id"); v != "" {
		if cid, e := strconv.ParseUint(v, 10, 32); e == nil {
			categoryID = uint(cid)
		}
	}
	if !a.validProjectRef(c, projectID, 0) || !a.validCategoryRef(c, categoryID, 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": errProjCatBad})
		return
	}
	doc, err := a.createHTMLDocFromFiles(c, files, title, projectID, categoryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": doc})
}

// ReplaceHTMLDoc 整站替换：PUT /console/api/docs/:id/html（属主 / admin）
func (a *App) ReplaceHTMLDoc(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	if !a.requireDocOwner(c, doc) {
		return
	}
	if doc.Type != model.DocTypeHTML {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅 HTML 整站文档支持整站替换"})
		return
	}
	old, _ := parseHTMLManifest(doc.Content)

	files, err := a.collectSiteFiles(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	files, err = finalizeSiteFiles(files)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 原子替换：写全新前缀目录，全部成功后一次性切换 manifest 指向；旧前缀随后清理
	prefix := fmt.Sprintf("html/%d/%s/", doc.ID, util.RandomSlug(8))
	m, written, err := a.putSiteFiles(c.Request.Context(), prefix, files)
	if err != nil {
		a.deleteSiteFilesAsync(written)
		log.Printf("[htmldoc] 替换写入失败 doc=%d: %v", doc.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存站点文件失败"})
		return
	}
	content, err := m.toJSON()
	if err != nil {
		a.deleteSiteFilesAsync(written)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成站点清单失败"})
		return
	}
	// 记录最后更新人（属主/管理员操作）
	updaterID := uint(0)
	if u := middleware.CurrentUser(c); u != nil {
		updaterID = u.ID
	}
	// 整站替换也是一次内容保存：content_version 原子 +1，版本号同步派生 v1.0.N
	if err := a.DB.Model(&model.Document{}).Where("id = ?", doc.ID).
		Updates(map[string]any{"content": content, "updated_at": time.Now(),
			"content_version": gorm.Expr("content_version + 1"),
			"version":         derivedDocVersion(doc.ContentVersion + 1),
			"updated_by_id":   updaterID, "updated_via": ""}).Error; err != nil {
		a.deleteSiteFilesAsync(written) // 切换失败则清掉新文件，旧版本仍可读
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新站点清单失败"})
		return
	}
	if old != nil {
		a.deleteSiteFilesAsync(htmlSiteKeys(old))
	}
	c.JSON(http.StatusOK, gin.H{"data": m})
}

// ---- raw 文件服务 ----

// splitRawPath 拆出首段签名与站点内路径：/<sig>/<sitepath>
func splitRawPath(p string) (sig, sitePath string) {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", ""
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

// adminRawPayload 管理预览 raw 签名的 HMAC 输入（与分享 token 空间隔离）。
// 注意：VerifyShareToken 以首个冒号分割 payload 比对 token 段，因此该值不能含冒号；
// 分享 token 恒为 32 位 hex（不含 'o'），"doc<十进制id>" 恒含 'o' 且长度不同，不会碰撞。
func adminRawPayload(docID uint) string {
	return "doc" + strconv.FormatUint(uint64(docID), 10)
}

// serveHTMLSiteFile 校验路径精确命中 manifest 后流式返回（防穿越、防读旧版本孤儿文件）
func (a *App) serveHTMLSiteFile(c *gin.Context, doc *model.Document, sitePath string) {
	m, err := parseHTMLManifest(doc.Content)
	if err != nil {
		c.String(http.StatusNotFound, "站点文件不存在")
		return
	}
	sitePath = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(sitePath, "\\", "/")), "/")
	if sitePath == "" || sitePath == "." {
		sitePath = m.Entry
	}
	if _, ok := m.fileSet()[sitePath]; !ok {
		c.String(http.StatusNotFound, "文件不存在")
		return
	}
	backend, err := a.fileStorage()
	if err != nil {
		c.String(http.StatusInternalServerError, "存储不可用")
		return
	}
	rc, size, err := backend.Get(c.Request.Context(), m.Prefix+sitePath)
	if err != nil {
		c.String(http.StatusNotFound, "文件不存在")
		return
	}
	defer rc.Close()
	c.Header("X-Content-Type-Options", "nosniff")
	if sitePath == m.Entry {
		// 入口不缓存：签名过期后子资源路径会 403，缓存旧入口只会更糟
		c.Header("Cache-Control", "no-store")
	} else {
		c.Header("Cache-Control", "private, max-age=3600")
	}
	// 存储未返回长度（如 RustFS 异常对象解析为 0）时传 -1 走 chunked，
	// 避免 Content-Length: 0 把有内容的响应截断为空
	if size <= 0 {
		size = -1
	}
	c.DataFromReader(http.StatusOK, size, siteContentType(sitePath), rc, nil)
}

// ShareRaw 分享页整站文件：GET /s/:token/raw/<sig>/<sitepath>。
// 不依赖 cookie：sandbox iframe（无 allow-same-origin）的子资源请求是跨站请求，
// Lax cookie 不会附带，因此凭据放在路径里由相对 URL 自动继承。
// <sig> 仅在访问者通过密码门后由 serveDoc 签发（无密码分享 = 任何持链接者可读，与 Markdown 分享语义一致）。
func (a *App) ShareRaw(c *gin.Context) {
	token := c.Param("token")
	doc, _ := a.loadShare(c, token)
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

// AdminRaw 管理预览整站文件：GET /console/raw/:id/<sig>/<sitepath>。
// 不挂 RequireAuth（子资源请求不带会话 cookie），凭短时签名鉴权：
// 签名只由 PreviewDoc（已通过会话与文档权限校验）签发。
func (a *App) AdminRaw(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var doc model.Document
	if err := a.DB.First(&doc, id).Error; err != nil {
		c.String(http.StatusNotFound, "文档不存在")
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
	if !a.Signer.VerifyShareToken(sig, adminRawPayload(doc.ID)) {
		c.String(http.StatusForbidden, "访问凭证已失效，请刷新页面")
		return
	}
	a.serveHTMLSiteFile(c, &doc, sitePath)
}

// htmlEntryURL 分享页入口：/s/:token/raw/<sig>/index.html
func (a *App) htmlEntryURL(token string) string {
	sig := a.Signer.MakeShareToken(token, htmlSigTTL)
	return "/s/" + token + "/raw/" + sig + "/" + htmlEntryName
}

// adminEntryURL 管理预览入口：/console/raw/:id/<sig>/index.html
func (a *App) adminEntryURL(docID uint) string {
	sig := a.Signer.MakeShareToken(adminRawPayload(docID), htmlSigTTL)
	return "/console/raw/" + strconv.FormatUint(uint64(docID), 10) + "/" + sig + "/" + htmlEntryName
}
