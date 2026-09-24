package handler

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"doc-share/internal/convert"

	"github.com/gin-gonic/gin"
)

// allowedDocExts 允许转换的文档扩展名白名单
var allowedDocExts = map[string]bool{".pdf": true, ".doc": true, ".docx": true}

// Convert 文档转换：pdf / doc / docx → Markdown 文本（不入库不落盘，由前端插入编辑器）
func (a *App) Convert(c *gin.Context) {
	maxBytes := int64(a.Cfg.Upload.MaxSizeMB) << 20
	// 预留 multipart 边界开销，超限时立即报错而非挂起
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+(1<<20))

	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取上传文件失败（可能超过大小限制）"})
		return
	}
	if fh.Size > maxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "err.fileTooBig")})
		return
	}
	if !allowedDocExts[strings.ToLower(filepath.Ext(fh.Filename))] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 pdf / doc / docx 文档"})
		return
	}

	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取文件失败"})
		return
	}
	defer src.Close()
	data, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取文件失败"})
		return
	}
	if int64(len(data)) > maxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "err.fileTooBig")})
		return
	}

	md, err := convert.ToMarkdown(fh.Filename, data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"markdown": md})
}
