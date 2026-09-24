package handler

import (
	"net/http"
	"os"
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

// Upload 图片上传：保存到本地 uploads 目录，返回可直接插入 Markdown 的 URL
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

	// 魔数校验：内容必须真是图片，防止改扩展名伪装
	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取文件失败"})
		return
	}
	defer src.Close()
	head := make([]byte, 512)
	n, _ := src.Read(head)
	if n == 0 || !strings.HasPrefix(http.DetectContentType(head[:n]), "image/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "文件内容不是有效图片"})
		return
	}

	sub := time.Now().Format("200601") // 按月分目录：uploads/202609/
	if err := os.MkdirAll(filepath.Join(a.Cfg.Upload.Dir, sub), 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建存储目录失败"})
		return
	}
	name := util.RandomSlug(16) + ext // 随机命名，防猜测/防原始文件名注入
	if err := c.SaveUploadedFile(fh, filepath.Join(a.Cfg.Upload.Dir, sub, name)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存文件失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"url": "/uploads/" + sub + "/" + name})
}
