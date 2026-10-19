package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"doc-share/internal/storage"
	"doc-share/internal/util"
	"doc-share/internal/webfetch"

	"time"
)

const maxImportImages = 40

var mdImageURLRe = regexp.MustCompile(`!\[[^\]]*\]\((https?://[^)\s]+)\)`)

// localizeMarkdownImages 把 Markdown 里的公网图片下载到上传存储，并改写为站内地址。
// 下载失败的图片保留原地址。
func (a *App) localizeMarkdownImages(ctx context.Context, md, referer string) (string, error) {
	matches := mdImageURLRe.FindAllStringSubmatch(md, -1)
	if len(matches) == 0 {
		return md, nil
	}
	backend, err := a.fileStorage()
	if err != nil {
		return md, nil
	}
	limit := int64(10) << 20
	if a.Cfg != nil && a.Cfg.Upload.MaxSizeMB > 0 {
		limit = int64(a.Cfg.Upload.MaxSizeMB) << 20
	}
	replaced := map[string]string{}
	n := 0
	var ctxErr error
	for _, m := range matches {
		if err := ctx.Err(); err != nil {
			ctxErr = err
			break
		}
		if n >= maxImportImages {
			break
		}
		remote := m[1]
		if _, ok := replaced[remote]; ok {
			continue
		}
		n++
		if local, ok := a.storeRemoteImage(ctx, backend, remote, referer, limit); ok {
			replaced[remote] = local
		}
	}
	if ctxErr == nil {
		ctxErr = ctx.Err()
	}
	return applyImageReplacements(md, replaced), ctxErr
}

func applyImageReplacements(md string, replaced map[string]string) string {
	if len(replaced) == 0 {
		return md
	}
	return mdImageURLRe.ReplaceAllStringFunc(md, func(s string) string {
		sub := mdImageURLRe.FindStringSubmatch(s)
		if len(sub) < 2 {
			return s
		}
		if local, ok := replaced[sub[1]]; ok {
			return strings.Replace(s, sub[1], local, 1)
		}
		return s
	})
}

func (a *App) storeRemoteImage(ctx context.Context, backend storage.Storage, remote, referer string, limit int64) (string, bool) {
	data, headerCT, err := webfetch.Fetch(ctx, remote, referer, limit)
	if err != nil || len(data) == 0 {
		return "", false
	}
	if !strings.HasPrefix(http.DetectContentType(data), "image/") && !strings.HasPrefix(strings.ToLower(headerCT), "image/") {
		return "", false
	}
	ext := imageExt(data, headerCT, remote)
	if ext == "" {
		return "", false
	}
	key := time.Now().Format("200601") + "/" + util.RandomSlug(16) + ext
	ctype := http.DetectContentType(data)
	res, err := backend.Put(ctx, key, bytes.NewReader(data), int64(len(data)), ctype)
	if err != nil || res.URL == "" {
		return "", false
	}
	return res.URL, true
}

func imageExt(data []byte, headerCT, rawURL string) string {
	ct := strings.ToLower(http.DetectContentType(data))
	if !strings.HasPrefix(ct, "image/") {
		ct = strings.ToLower(headerCT)
	}
	switch {
	case strings.Contains(ct, "jpeg"):
		return ".jpg"
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "gif"):
		return ".gif"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "bmp"):
		return ".bmp"
	}
	if u, err := url.Parse(rawURL); err == nil {
		ext := strings.ToLower(path.Ext(u.Path))
		if allowedImageExts[ext] {
			return ext
		}
	}
	return ""
}
