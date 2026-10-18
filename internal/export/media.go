package export

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/image/bmp"
	"golang.org/x/image/webp"
)

// 匹配 Markdown 图片：![alt](url) 可选 title
var mdImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)(\s+"[^"]*"|\s+'[^']*')?\)`)

// resolveUnderRoot 将 rel（正斜杠相对路径）解析到 root 下，拒绝穿越。
func resolveUnderRoot(root, rel string) (string, bool) {
	rel = strings.TrimSpace(rel)
	rel = strings.ReplaceAll(rel, "\\", "/")
	if rel == "" || strings.HasPrefix(rel, "/") {
		return "", false
	}
	// Clean 前拒绝 ..，避免 "/../x" 被收成根下合法路径却掩盖穿越意图
	for _, p := range strings.Split(rel, "/") {
		if p == "" || p == "." || p == ".." {
			return "", false
		}
	}
	rel = path.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", false
	}
	abs := filepath.Clean(filepath.Join(absRoot, filepath.FromSlash(rel)))
	relCheck, err := filepath.Rel(absRoot, abs)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", false
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return "", false
	}
	return abs, true
}

// copyImageForExport 拷贝图片到 mediaDir；gif/webp/bmp 转为 PNG，便于 PDF/DOCX。
// 返回写入的相对路径 media/xxx.ext。
func copyImageForExport(src, mediaDir string, seq int) (string, error) {
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		return "", err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	outData, ext, err := normalizeImageBytes(data, filepath.Ext(src))
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%03d%s", seq, ext)
	dst := filepath.Join(mediaDir, name)
	if err := os.WriteFile(dst, outData, 0o600); err != nil {
		return "", err
	}
	return "media/" + name, nil
}

func normalizeImageBytes(data []byte, srcExt string) (out []byte, ext string, err error) {
	srcExt = strings.ToLower(srcExt)
	ctype := sniffImage(data, srcExt)

	switch ctype {
	case "image/png":
		return data, ".png", nil
	case "image/jpeg":
		return data, ".jpg", nil
	case "image/gif", "image/webp", "image/bmp":
		img, err := decodeImage(data, ctype)
		if err != nil {
			return nil, "", err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), ".png", nil
	default:
		return nil, "", fmt.Errorf("不支持的图片类型: %s", ctype)
	}
}

func sniffImage(data []byte, ext string) string {
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 6 && (bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))) {
		return "image/gif"
	}
	if len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp"
	}
	if len(data) >= 2 && data[0] == 'B' && data[1] == 'M' {
		return "image/bmp"
	}
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	}
	return ""
}

func decodeImage(data []byte, ctype string) (image.Image, error) {
	r := bytes.NewReader(data)
	switch ctype {
	case "image/gif":
		return gif.Decode(r)
	case "image/webp":
		return webp.Decode(r)
	case "image/bmp":
		return bmp.Decode(r)
	case "image/png":
		return png.Decode(r)
	case "image/jpeg":
		return jpeg.Decode(r)
	default:
		img, _, err := image.Decode(r)
		return img, err
	}
}

func mimeFromImageBytes(data []byte, ext string) string {
	switch sniffImage(data, ext) {
	case "image/png":
		return "image/png"
	case "image/jpeg":
		return "image/jpeg"
	default:
		return ""
	}
}

func rewriteMarkdownImages(md string, rewrite func(alt, url, titleSuffix string) string) string {
	return mdImageRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := mdImageRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		alt := sub[1]
		url := sub[2]
		titleSuffix := ""
		if len(sub) > 3 {
			titleSuffix = sub[3]
		}
		return rewrite(alt, url, titleSuffix)
	})
}

func isHTTPURL(u string) bool {
	u = strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}
