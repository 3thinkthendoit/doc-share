package handler

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
)

// allowedImageExts 允许上传的图片扩展名白名单（svg 可携带脚本，禁止）
var allowedImageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true,
}

// Upload 图片上传：按系统设置写入本地或 RustFS，返回可直接插入 Markdown 的 URL
func (a *App) Upload(c *gin.Context) {
	maxBytes := int64(a.Cfg.Upload.MaxSizeMB) << 20
	// 预留 multipart 边界开销，超限时立即报错而非挂起
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+(1<<20))

	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取上传文件失败（可能超过大小限制）"})
		return
	}
	if fh.Size > maxBytes {
		// 含动态参数，中间件的原文反查帮不上，直接按 key 翻译
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "err.imgTooBig", a.Cfg.Upload.MaxSizeMB)})
		return
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !allowedImageExts[ext] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 png / jpg / gif / webp / bmp 图片"})
		return
	}

	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取文件失败"})
		return
	}
	defer src.Close()

	// 读入内存：魔数校验 + 交给存储后端（RustFS Put 需要可 seek/完整 body）
	data, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取文件失败"})
		return
	}
	if int64(len(data)) > maxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "err.imgTooBig", a.Cfg.Upload.MaxSizeMB)})
		return
	}
	if len(data) == 0 || !strings.HasPrefix(http.DetectContentType(data), "image/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "文件内容不是有效图片"})
		return
	}

	backend, err := a.fileStorage()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	key := time.Now().Format("200601") + "/" + util.RandomSlug(16) + ext
	ctype := http.DetectContentType(data)
	res, err := backend.Put(c.Request.Context(), key, bytes.NewReader(data), int64(len(data)), ctype)
	if err != nil {
		log.Printf("[upload] 保存失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存文件失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": res.URL})
}
