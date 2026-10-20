package handler

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"doc-share/internal/export"
	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/storage"

	"github.com/gin-gonic/gin"
)

// exportRateLimiter 导出专用限流，与分享密码门（5 次/10 分钟）解耦。
// 按 IP 或登录用户 + 文档维度计数：约 60 次/分钟，覆盖正常反复下载。
var exportRateLimiter = middleware.NewShareLimiter(60, time.Minute)

// ShareExport 分享页导出：GET /s/:token/export?format=pdf|docx
// 权限与评论等公开 API 一致（分享有效 + 密码已解锁）。
func (a *App) ShareExport(c *gin.Context) {
	token := c.Param("token")
	doc, share := a.loadShare(c, token)
	if doc == nil {
		return
	}
	if !a.shareUnlocked(c, token, doc, share) {
		return
	}
	a.writeDocExport(c, doc, "s:"+token)
}

// ProjectShareExport 项目分享文档导出：GET /ps/:token/d/:docId/export?format=pdf|docx
// 权限与项目分享阅读一致（分享有效 + 已通过访问门）。
func (a *App) ProjectShareExport(c *gin.Context) {
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
	a.writeDocExport(c, doc, fmt.Sprintf("ps:%s:%d", token, doc.ID))
}

// PreviewExport 预览页导出：GET /console/api/docs/:id/export?format=pdf|docx
// 权限与 PreviewDoc / loadDoc 一致（属主、管理员或项目成员）。
func (a *App) PreviewExport(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	a.writeDocExport(c, doc, fmt.Sprintf("d:%d", doc.ID))
}

func (a *App) writeDocExport(c *gin.Context, doc *model.Document, limitKey string) {
	format := strings.ToLower(strings.TrimSpace(c.Query("format")))
	switch doc.Type {
	case model.DocTypeMarkdown:
		if format != export.FormatPDF && format != export.FormatDOCX {
			c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "share.exportBadFormat")})
			return
		}
	case model.DocTypeHTML:
		if format != export.FormatPDF {
			c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "share.exportHTMLPdfOnly")})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "share.exportUnsupported")})
		return
	}

	// 先限流再探测字体，避免 Available 的字体探测被未限流请求打满
	limitID := c.ClientIP()
	if u := a.sessionUser(c); u != nil {
		limitID = fmt.Sprintf("u:%d", u.ID)
	}
	exportLimit := "export:" + limitKey
	if !exportRateLimiter.Allowed(limitID, exportLimit) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": a.tr(c, "share.exportBusy")})
		return
	}
	if !export.TryAcquire() {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": a.tr(c, "share.exportBusy")})
		return
	}
	defer export.Release()
	exportRateLimiter.Fail(limitID, exportLimit)

	if err := export.Available(format); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": a.tr(c, "share.exportUnavailable")})
		return
	}

	var data []byte
	var ctype string
	var err error
	if doc.Type == model.DocTypeHTML {
		data, ctype, err = a.exportHTMLDocPDF(c.Request.Context(), doc)
	} else {
		uploadDir := ""
		if a.Cfg != nil {
			uploadDir = a.Cfg.Upload.Dir
		}
		data, ctype, err = export.Export(c.Request.Context(), format, export.Options{
			Title:      doc.Title,
			Markdown:   doc.Content,
			BaseURL:    a.siteBaseURL(c),
			UploadDir:  uploadDir,
			ImageHosts: a.exportImageHosts(c),
		})
	}
	if err != nil {
		log.Printf("export doc=%d format=%s: %v", doc.ID, format, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": a.tr(c, "share.exportFail")})
		return
	}

	name := export.SafeFilename(doc.Title) + "." + format
	c.Header("Content-Type", ctype)
	c.Header("Content-Disposition", contentDisposition(name))
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, ctype, data)
}

// exportHTMLDocPDF 物化整站到临时目录后转 PDF（相对图片/CSS 可解析）。
func (a *App) exportHTMLDocPDF(ctx context.Context, doc *model.Document) ([]byte, string, error) {
	m, err := parseHTMLManifest(doc.Content)
	if err != nil {
		return nil, "", fmt.Errorf("站点清单无效: %w", err)
	}
	backend, err := a.fileStorage()
	if err != nil {
		return nil, "", err
	}
	work, err := os.MkdirTemp("", "docshare-html-export-*")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(work)

	capBytes := a.htmlSiteMaxBytes()
	var total int64
	var entryHTML string
	for _, name := range m.Files {
		if !export.SafeSiteRelPath(name) {
			return nil, "", fmt.Errorf("站点路径非法: %s", name)
		}
		remain := capBytes - total
		if remain <= 0 {
			return nil, "", fmt.Errorf("站点文件过大")
		}
		key := m.Prefix + name
		rc, _, err := backend.Get(ctx, key)
		if err != nil {
			return nil, "", fmt.Errorf("读取 %s 失败: %w", name, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, remain+1))
		rc.Close()
		if err != nil {
			return nil, "", err
		}
		if int64(len(data)) > remain {
			return nil, "", fmt.Errorf("站点文件过大")
		}
		total += int64(len(data))
		if err := export.WriteTempFile(work, name, data); err != nil {
			return nil, "", err
		}
		if name == m.Entry || (m.Entry == "" && name == htmlEntryName) {
			entryHTML = string(data)
		}
	}
	if entryHTML == "" {
		b, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(htmlEntryName)))
		if err != nil {
			return nil, "", fmt.Errorf("缺少入口 %s", htmlEntryName)
		}
		entryHTML = string(b)
	}
	pdf, err := export.ExportHTML(ctx, entryHTML, doc.Title, work)
	if err != nil {
		return nil, "", err
	}
	return pdf, "application/pdf", nil
}

// exportImageHosts 导出时可拉取远程图片的主机白名单：站点域名 + 对象存储 endpoint。
func (a *App) exportImageHosts(c *gin.Context) []string {
	hosts := []string{a.siteBaseURL(c)}
	s := a.Settings()
	switch s.StorageDriver {
	case storage.DriverRustFS:
		if ep := strings.TrimSpace(s.RustFSEndpoint); ep != "" {
			hosts = append(hosts, ep)
		}
	case storage.DriverCOS:
		if ep := strings.TrimSpace(s.COSEndpoint); ep != "" {
			hosts = append(hosts, ep)
		}
	}
	return hosts
}

// contentDisposition 同时给出 ASCII fallback 与 RFC 5987 filename*（支持中文标题）。
func contentDisposition(filename string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 128 && r >= 32 && !strings.ContainsRune(`<>:"/\|?*`, r) {
			return r
		}
		return '_'
	}, filename)
	if strings.Trim(ascii, "._") == "" {
		ascii = "document"
		if i := strings.LastIndex(filename, "."); i >= 0 {
			ascii += filename[i:]
		}
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, url.PathEscape(filename))
}
