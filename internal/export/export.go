// Package export 将已保存的 Markdown 导出为 PDF / DOCX（纯 Go，无外部二进制）。
package export

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	FormatPDF  = "pdf"
	FormatDOCX = "docx"

	defaultTimeout = 90 * time.Second
	maxConcurrent  = 2 // 全局同时导出上限，防止渲染打满 CPU
)

// 站内相对链接，允许可选 title：](/uploads/a.png "x")
var relLinkRe = regexp.MustCompile(`\]\((/(?:uploads|static)/[^)\s]+)(\s+"[^"]*"|\s+'[^']*')?\)`)

// 危险协议：导出前剥离，避免渲染器读本地或执行脚本
var mdDangerLinkRe = regexp.MustCompile(`(?i)\]\((?:file|filesystem|data|javascript|vbscript):[^)]*\)`)
var htmlDangerSrcDQ = regexp.MustCompile(`(?i)\b(src|href)\s*=\s*"\s*(?:file|filesystem|data|javascript|vbscript):[^"]*"`)
var htmlDangerSrcSQ = regexp.MustCompile(`(?i)\b(src|href)\s*=\s*'\s*(?:file|filesystem|data|javascript|vbscript):[^']*'`)

// 全局导出槽：非阻塞申请，满则调用方返回忙碌
var exportSlots = make(chan struct{}, maxConcurrent)

// Available 检查导出是否可用（DOCX 恒可用；PDF 需中文字体）。
func Available(format string) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatDOCX:
		return nil
	case FormatPDF:
		_, err := resolveFonts()
		return err
	default:
		return fmt.Errorf("不支持的导出格式")
	}
}

// ErrBusy 并发槽已满
var ErrBusy = fmt.Errorf("导出繁忙，请稍后再试")

// TryAcquire 非阻塞占用一个导出槽；成功时必须配对 Release。
func TryAcquire() bool {
	select {
	case exportSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release 释放导出槽。
func Release() {
	select {
	case <-exportSlots:
	default:
	}
}

// Options 单次导出参数
type Options struct {
	Title      string
	Markdown   string
	BaseURL    string   // 站点绝对前缀，用于解析相对链接
	UploadDir  string   // 本地上传目录；安全校验后把图片拷入临时目录供渲染
	ImageHosts []string // 允许拉取远程图片的主机名（站点域名、对象存储等）；空则不拉取
	Timeout    time.Duration
}

// Export 按 format 导出；返回文件字节与建议 Content-Type。
// 调用方应先 TryAcquire，并在结束后 Release（handler 层负责）。
func Export(ctx context.Context, format string, opt Options) (data []byte, contentType string, err error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if opt.Timeout <= 0 {
		opt.Timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	work, err := os.MkdirTemp("", "docshare-export-*")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(work)

	title := SanitizeMetaTitle(opt.Title)
	if title == "" {
		title = "document"
	}
	md := SanitizeMarkdown(opt.Markdown)
	md = MaterializeMarkdown(md, opt.BaseURL, opt.UploadDir, filepath.Join(work, "media"))
	md = EnsureTitleHeading(md, title)
	hosts := opt.ImageHosts
	if len(hosts) == 0 {
		hosts = hostsFromBaseURL(opt.BaseURL)
	}
	seq := countMediaFiles(filepath.Join(work, "media"))
	// 行内 http(s) 图：白名单拉取，其余剥离
	md, err = localizeRemoteImages(ctx, work, md, hosts, seq)
	if err != nil {
		return nil, "", err
	}
	seq = countMediaFiles(filepath.Join(work, "media"))
	// 参考定义 [id]: http…：白名单拉取到 media/，否则改为 #，堵住 webFs SSRF
	md, _, err = localizeOrNeutralizeRefs(ctx, work, md, hosts, seq)
	if err != nil {
		return nil, "", err
	}
	// 兜底：残留行内 http(s) 图一律降级
	md = blockRemoteImagesKeepLocal(md)

	switch format {
	case FormatDOCX:
		var out []byte
		err = runWithContext(ctx, func() error {
			var e error
			out, e = toDOCX(ctx, work, md, title)
			return e
		})
		return out, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", err
	case FormatPDF:
		var out []byte
		err = runWithContext(ctx, func() error {
			var e error
			out, e = toPDF(ctx, work, md, title)
			return e
		})
		return out, "application/pdf", err
	default:
		return nil, "", fmt.Errorf("不支持的导出格式")
	}
}

func hostsFromBaseURL(base string) []string {
	base = strings.TrimSpace(base)
	if base == "" {
		return nil
	}
	// 复用 fetch 的主机规范化
	set := normalizeHostSet([]string{base})
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	return out
}

func countMediaFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// SanitizeMetaTitle 清洗文档标题：去掉换行/控制字符。
func SanitizeMetaTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(title))
	prevSpace := false
	for _, r := range title {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		case r < 32 || r == 127:
			continue
		case r == ' ':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// SanitizeMarkdown 剥离可导致本地文件读取或脚本执行的链接协议。
// 注意：会去掉 data: 链接；DOCX 导出在物化后再自行嵌入 media 为 data URI。
func SanitizeMarkdown(md string) string {
	md = mdDangerLinkRe.ReplaceAllString(md, "](#)")
	md = htmlDangerSrcDQ.ReplaceAllString(md, `$1="#"`)
	md = htmlDangerSrcSQ.ReplaceAllString(md, `$1='#'`)
	return md
}

// EnsureTitleHeading 正文尚无一级标题时补上文档标题，避免与已有 H1 重复。
func EnsureTitleHeading(md, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return md
	}
	trim := strings.TrimLeft(md, " \t")
	if strings.HasPrefix(trim, "# ") || strings.HasPrefix(trim, "#\t") || strings.HasPrefix(trim, "#\n") {
		return md
	}
	return "# " + title + "\n\n" + md
}

// MaterializeMarkdown 将安全的 /uploads 图片拷入 mediaDir（必要时转为 PNG），
// 其余站内路径改为绝对 http(s) URL（随后由 localizeRemoteImages 按白名单拉取或剥离）。
func MaterializeMarkdown(md, baseURL, uploadDir, mediaDir string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	uploadDir = strings.TrimSpace(uploadDir)
	n := 0
	return relLinkRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := relLinkRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		urlPath := sub[1]
		titleSuffix := ""
		if len(sub) > 2 {
			titleSuffix = sub[2]
		}
		if abs, ok := resolveSafeUpload(uploadDir, urlPath); ok {
			n++
			if rel, err := copyImageForExport(abs, mediaDir, n); err == nil {
				return "](" + rel + titleSuffix + ")"
			}
			n-- // 拷贝失败则回退序号
		}
		if baseURL == "" {
			return m
		}
		return "](" + baseURL + urlPath + titleSuffix + ")"
	})
}

// PrepareMarkdown 兼容旧调用：等价于 Sanitize + Materialize（媒体写入系统临时目录旁路时用 baseURL）。
// 测试与简单场景使用；生产 Export 走带 workdir 的 MaterializeMarkdown。
func PrepareMarkdown(md, baseURL, uploadDir string) string {
	md = SanitizeMarkdown(md)
	if uploadDir == "" {
		return MaterializeMarkdown(md, baseURL, "", "")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return relLinkRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := relLinkRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		urlPath := sub[1]
		titleSuffix := ""
		if len(sub) > 2 {
			titleSuffix = sub[2]
		}
		if baseURL == "" {
			return m
		}
		return "](" + baseURL + urlPath + titleSuffix + ")"
	})
}

// resolveSafeUpload 仅允许 uploadDir 内、非 html/ 前缀的常规文件。
func resolveSafeUpload(uploadDir, urlPath string) (string, bool) {
	if uploadDir == "" || !strings.HasPrefix(urlPath, "/uploads/") {
		return "", false
	}
	rel := strings.TrimPrefix(urlPath, "/uploads/")
	rel = path.Clean("/" + rel)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." {
		return "", false
	}
	for _, p := range strings.Split(rel, "/") {
		if p == "" || p == "." || p == ".." {
			return "", false
		}
	}
	if rel == "html" || strings.HasPrefix(rel, "html/") {
		return "", false
	}
	root, err := filepath.Abs(filepath.Clean(uploadDir))
	if err != nil {
		return "", false
	}
	abs := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	relCheck, err := filepath.Rel(root, abs)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", false
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return "", false
	}
	return abs, true
}

// SafeFilename 生成适合 Content-Disposition 的文件名（不含扩展名）。
func SafeFilename(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "document"
	}
	var b strings.Builder
	for _, r := range title {
		switch {
		case r < 32 || r == 127:
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Trim(b.String(), " .")
	if s == "" {
		return "document"
	}
	runes := []rune(s)
	if len(runes) > 80 {
		s = string(runes[:80])
	}
	return s
}
