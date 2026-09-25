package handler

import (
	"bytes"
	"context"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"doc-share/internal/storage"
	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
)

// allowedImageExts 允许上传的图片扩展名白名单（svg 可携带脚本，禁止）
var allowedImageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true,
}

// Upload 图片上传：按系统设置写入本地或 RustFS，返回可直接插入 Markdown 的 URL。
// purpose=embed-preview（或文件名 embed-preview.*）时写入 embed/ 前缀；
// 可选 replace_url：仅当旧对象也是嵌入预览时，新图成功后异步删除。
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
	if isEmbedPreviewUpload(c, fh) {
		key = "embed/" + key
	}
	ctype := http.DetectContentType(data)
	res, err := backend.Put(c.Request.Context(), key, bytes.NewReader(data), int64(len(data)), ctype)
	if err != nil {
		log.Printf("[upload] 保存失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存文件失败"})
		return
	}

	// 新图已落盘后再异步删旧嵌入预览，避免覆盖失败时误删；普通插图不可经此路径删除
	replaceURL := strings.TrimSpace(c.PostForm("replace_url"))
	if replaceURL != "" && replaceURL != res.URL {
		go a.deleteUploadedObjectAsync(replaceURL)
	}

	c.JSON(http.StatusOK, gin.H{"url": res.URL})
}

// DeleteUpload 异步删除嵌入预览图；普通 Markdown 插图不受理
func (a *App) DeleteUpload(c *gin.Context) {
	var body struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 url"})
		return
	}
	go a.deleteUploadedObjectAsync(strings.TrimSpace(body.URL))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// isEmbedPreviewUpload 识别嵌入预览上传（专用目录，可被安全清理）
func isEmbedPreviewUpload(c *gin.Context, fh *multipart.FileHeader) bool {
	if strings.EqualFold(strings.TrimSpace(c.PostForm("purpose")), "embed-preview") {
		return true
	}
	base := strings.ToLower(filepath.Base(fh.Filename))
	return strings.HasPrefix(base, "embed-preview.")
}

// deleteUploadedObjectAsync 后台删除嵌入预览对象；非 embed/ 前缀一律跳过
func (a *App) deleteUploadedObjectAsync(rawURL string) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[upload] 异步删除 panic: %v", rec)
		}
	}()

	backend, err := a.fileStorage()
	if err != nil {
		log.Printf("[upload] 异步删除跳过（存储不可用）: %v", err)
		return
	}

	var rustCfg *storage.RustFSConfig
	s := a.Settings()
	if s.StorageDriver == storage.DriverRustFS {
		rustCfg = &storage.RustFSConfig{
			Endpoint:  s.RustFSEndpoint,
			Region:    s.RustFSRegion,
			AccessKey: s.RustFSAccessKey,
			SecretKey: s.RustFSSecretKey,
			Bucket:    s.RustFSBucket,
		}
	}

	key := storage.ObjectKeyFromURL(rawURL, rustCfg)
	if key == "" {
		log.Printf("[upload] 异步删除跳过（无法解析 URL）: %s", rawURL)
		return
	}
	if !storage.ValidEmbedUploadKey(key) {
		log.Printf("[upload] 异步删除拒绝（非嵌入预览）: %s", key)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := backend.Delete(ctx, key); err != nil {
		log.Printf("[upload] 异步删除旧图失败 key=%s: %v", key, err)
	}
}
