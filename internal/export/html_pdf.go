package export

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zlfzx/htmlpdf"
)

// htmlScriptRe 导出前剥离 script（引擎不执行 JS，避免干扰解析）
var htmlScriptRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)

// HTML 资源引用：src/href/poster 等（双引号 / 单引号）
var (
	htmlAttrURLDQ = regexp.MustCompile(`(?i)(\b(?:src|href|poster|data|action|formaction|xlink:href)\s*=\s*")([^"]*)(")`)
	htmlAttrURLSQ = regexp.MustCompile(`(?i)(\b(?:src|href|poster|data|action|formaction|xlink:href)\s*=\s*')([^']*)(')`)
	htmlSrcsetDQ  = regexp.MustCompile(`(?i)(\bsrcset\s*=\s*")([^"]*)(")`)
	htmlSrcsetSQ  = regexp.MustCompile(`(?i)(\bsrcset\s*=\s*')([^']*)(')`)
	cssURLRe      = regexp.MustCompile(`(?i)(url\s*\(\s*)(["']?)([^"')]+)(["']?)(\s*\))`)
	cssImportRe   = regexp.MustCompile(`(?i)@import\s+(?:url\s*\([^)]*\)|["'][^"']*["'])\s*;?`)
)

// ExportHTML 将完整 HTML 页面转为 PDF（纯 Go，无 Chromium）。
// basePath 为站点文件根目录（相对资源/图片从此解析）；可为空（仅内联/data:image 资源）。
// 导出前会沙箱化资源引用：禁止远程拉取与目录穿越，对齐 Markdown 导出的安全模型。
func ExportHTML(ctx context.Context, html, title, basePath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ff, err := resolveFonts()
	if err != nil {
		return nil, err
	}
	title = SanitizeMetaTitle(title)
	if title == "" {
		title = "document"
	}
	html = strings.TrimSpace(html)
	if html == "" {
		return nil, fmt.Errorf("HTML 内容为空")
	}
	if basePath != "" {
		if err := sanitizeExportTree(basePath); err != nil {
			return nil, err
		}
	}
	html = prepareHTMLForPDF(html, basePath)

	opts := htmlpdf.Options{
		PageSize:          htmlpdf.A4,
		Orientation:       htmlpdf.Portrait,
		Margin:            htmlpdf.Margin{Top: 36, Right: 36, Bottom: 36, Left: 36},
		DefaultFontFamily: fontFamilyNotoSC,
		DefaultFontSize:   11,
		EnableImages:      true,
		DPI:               96,
		Title:             title,
		BasePath:          basePath,
		// 故意不设 BaseURL：避免相对路径被解析成可拉取的绝对 URL
	}
	conv := htmlpdf.New(opts)
	conv.RegisterFontFamily(fontFamilyNotoSC, ff.Regular, ff.Bold, ff.Regular, ff.Bold)

	var data []byte
	err = runWithContext(ctx, func() error {
		out, e := conv.ConvertToBytes(html)
		if e != nil {
			return fmt.Errorf("HTML 转 PDF 失败: %w", e)
		}
		if len(out) == 0 {
			return fmt.Errorf("PDF 导出结果为空")
		}
		data = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// prepareHTMLForPDF 剥离 script 并沙箱化资源，再注入默认中文字体。
func prepareHTMLForPDF(html, basePath string) string {
	html = htmlScriptRe.ReplaceAllString(html, "")
	html = sanitizeHTMLResources(html, basePath)
	return ensureHTMLFontFamily(html, fontFamilyNotoSC)
}

// sanitizeExportTree 导出前净化工作目录内 html/css，堵住外链样式/@import 的二次拉取。
func sanitizeExportTree(root string) error {
	root = filepath.Clean(root)
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".html", ".htm", ".css":
		default:
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var out string
		if ext == ".css" {
			out = sanitizeCSSResources(string(raw), root)
		} else {
			out = sanitizeHTMLResources(string(raw), root)
		}
		if out == string(raw) {
			return nil
		}
		return os.WriteFile(path, []byte(out), 0o644)
	})
}

// sanitizeHTMLResources 仅保留安全相对路径与 data:image；远程/绝对/穿越一律中和。
func sanitizeHTMLResources(html, basePath string) string {
	html = htmlAttrURLDQ.ReplaceAllStringFunc(html, func(m string) string {
		sub := htmlAttrURLDQ.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		return sub[1] + safeResourceOrHash(sub[2], basePath) + sub[3]
	})
	html = htmlAttrURLSQ.ReplaceAllStringFunc(html, func(m string) string {
		sub := htmlAttrURLSQ.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		return sub[1] + safeResourceOrHash(sub[2], basePath) + sub[3]
	})
	html = htmlSrcsetDQ.ReplaceAllStringFunc(html, func(m string) string {
		sub := htmlSrcsetDQ.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		return sub[1] + sanitizeSrcset(sub[2], basePath) + sub[3]
	})
	html = htmlSrcsetSQ.ReplaceAllStringFunc(html, func(m string) string {
		sub := htmlSrcsetSQ.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		return sub[1] + sanitizeSrcset(sub[2], basePath) + sub[3]
	})
	return sanitizeCSSResources(html, basePath)
}

func sanitizeCSSResources(css, basePath string) string {
	css = cssImportRe.ReplaceAllString(css, "/* import blocked */")
	return cssURLRe.ReplaceAllStringFunc(css, func(m string) string {
		sub := cssURLRe.FindStringSubmatch(m)
		if len(sub) < 6 {
			return m
		}
		ref := safeResourceOrHash(strings.TrimSpace(sub[3]), basePath)
		return sub[1] + sub[2] + ref + sub[4] + sub[5]
	})
}

func sanitizeSrcset(srcset, basePath string) string {
	parts := strings.Split(srcset, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fields := strings.Fields(p)
		if len(fields) == 0 {
			continue
		}
		u := safeResourceOrHash(fields[0], basePath)
		if u == "#" {
			continue
		}
		if len(fields) > 1 {
			out = append(out, u+" "+strings.Join(fields[1:], " "))
		} else {
			out = append(out, u)
		}
	}
	return strings.Join(out, ", ")
}

// safeResourceOrHash 允许：空、#fragment、data:image/*、工作目录内相对路径。
// 其余（http(s)、file、绝对路径、.. 穿越）替换为 #。
func safeResourceOrHash(ref, basePath string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "#" {
		return ref
	}
	if strings.HasPrefix(ref, "#") {
		return ref
	}
	lower := strings.ToLower(ref)
	if strings.HasPrefix(lower, "data:image/") {
		return ref
	}
	if strings.HasPrefix(lower, "//") ||
		strings.HasPrefix(lower, "http:") ||
		strings.HasPrefix(lower, "https:") ||
		strings.HasPrefix(lower, "file:") ||
		strings.HasPrefix(lower, "filesystem:") ||
		strings.HasPrefix(lower, "javascript:") ||
		strings.HasPrefix(lower, "vbscript:") ||
		strings.HasPrefix(lower, "data:") {
		return "#"
	}
	// 带其它 scheme（含 Windows 盘符 C:\）一律拒绝
	if i := strings.Index(ref, ":"); i >= 0 {
		return "#"
	}
	if strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, `\`) {
		return "#"
	}
	rel := filepath.ToSlash(ref)
	if !SafeSiteRelPath(rel) {
		return "#"
	}
	if basePath != "" {
		joined := filepath.Join(basePath, filepath.FromSlash(rel))
		if !pathUnderRoot(basePath, joined) {
			return "#"
		}
	}
	return ref
}

// pathUnderRoot 确认 absPath 落在 root 之下（Clean 后前缀匹配）。
func pathUnderRoot(root, absPath string) bool {
	root = filepath.Clean(root)
	absPath = filepath.Clean(absPath)
	sep := string(os.PathSeparator)
	if absPath == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(absPath, prefix)
}

// ensureHTMLFontFamily 在 <head> 注入默认中文字体，避免无 font-family 时落到 Helvetica 缺字。
func ensureHTMLFontFamily(html, family string) string {
	inject := `<style data-docshare-export="1">html,body{font-family:` + family + `,sans-serif}</style>`
	lower := strings.ToLower(html)
	if i := strings.Index(lower, "</head>"); i >= 0 {
		return html[:i] + inject + html[i:]
	}
	if i := strings.Index(lower, "<body"); i >= 0 {
		return html[:i] + inject + html[i:]
	}
	return inject + html
}

// SafeSiteRelPath 站点相对路径：拒绝穿越、绝对路径、盘符、反斜杠（与上传校验同口径）。
func SafeSiteRelPath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return false
	}
	// 先查原始反斜杠，再 ToSlash（Windows 上 ToSlash 会把 \ 吃掉）
	if strings.Contains(p, `\`) || strings.ContainsAny(p, ":\x00") {
		return false
	}
	p = filepath.ToSlash(p)
	if strings.HasPrefix(p, "/") {
		return false
	}
	if strings.Contains(p, "..") || strings.Contains(p, "//") {
		return false
	}
	return true
}

// WriteTempFile 将数据写入目录下指定相对路径（自动建父目录）。
func WriteTempFile(dir, rel string, data []byte) error {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if !SafeSiteRelPath(rel) {
		return fmt.Errorf("非法相对路径")
	}
	dst := filepath.Join(dir, filepath.FromSlash(rel))
	if !pathUnderRoot(dir, dst) {
		return fmt.Errorf("非法相对路径")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
