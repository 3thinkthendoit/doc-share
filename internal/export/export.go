// Package export 将已保存的 Markdown 导出为 PDF（Chromium 打印）与 DOCX（Pandoc）。
package export

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
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
	maxConcurrent  = 2 // 全局同时导出上限，防止 Chromium/Pandoc 打满 CPU
)

// 站内相对链接，允许可选 title：](/uploads/a.png "x")
var relLinkRe = regexp.MustCompile(`\]\((/(?:uploads|static)/[^)\s]+)(\s+"[^"]*"|\s+'[^']*')?\)`)

// 危险协议：导出前剥离，避免 Chromium/Pandoc 读本地或执行脚本
var mdDangerLinkRe = regexp.MustCompile(`(?i)\]\((?:file|filesystem|data|javascript|vbscript):[^)]*\)`)
var htmlDangerSrcDQ = regexp.MustCompile(`(?i)\b(src|href)\s*=\s*"\s*(?:file|filesystem|data|javascript|vbscript):[^"]*"`)
var htmlDangerSrcSQ = regexp.MustCompile(`(?i)\b(src|href)\s*=\s*'\s*(?:file|filesystem|data|javascript|vbscript):[^']*'`)

// 全局导出槽：非阻塞申请，满则调用方返回忙碌
var exportSlots = make(chan struct{}, maxConcurrent)

// Tools 运行时依赖路径；空字段表示在 PATH 中查找默认名。
type Tools struct {
	Pandoc   string // 默认 pandoc
	Chromium string // 默认 chromium / chromium-browser / google-chrome
}

func (t Tools) pandocBin() string {
	if t.Pandoc != "" {
		return t.Pandoc
	}
	return "pandoc"
}

func (t Tools) chromiumBin() (string, error) {
	if t.Chromium != "" {
		return t.Chromium, nil
	}
	if env := strings.TrimSpace(os.Getenv("CHROME_PATH")); env != "" {
		if p, err := exec.LookPath(env); err == nil {
			return p, nil
		}
		if _, err := os.Stat(env); err == nil {
			return env, nil
		}
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("未找到 Chromium/Chrome，无法导出 PDF")
}

// Available 检查导出所需外部工具是否可用（DOCX 需 Pandoc；PDF 另需 Chromium）。
func Available(format string) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatDOCX:
		if _, err := exec.LookPath("pandoc"); err != nil {
			return fmt.Errorf("未找到 Pandoc，无法导出 DOCX")
		}
		return nil
	case FormatPDF:
		if _, err := exec.LookPath("pandoc"); err != nil {
			return fmt.Errorf("未找到 Pandoc，无法导出 PDF")
		}
		var t Tools
		if _, err := t.chromiumBin(); err != nil {
			return err
		}
		return nil
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
	Title     string
	Markdown  string
	BaseURL   string // 站点绝对前缀，用于解析相对链接
	UploadDir string // 本地上传目录；安全校验后把图片拷入临时目录供渲染
	Tools     Tools
	Timeout   time.Duration
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

	switch format {
	case FormatDOCX:
		data, err = toDOCX(ctx, opt.Tools, work, md, title)
		return data, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", err
	case FormatPDF:
		data, err = toPDF(ctx, opt.Tools, work, md, title)
		return data, "application/pdf", err
	default:
		return nil, "", fmt.Errorf("不支持的导出格式")
	}
}

// SanitizeMetaTitle 清洗传入 Pandoc --metadata 的标题：去掉换行/控制字符，避免 metadata 注入。
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

// MaterializeMarkdown 将安全的 /uploads 图片拷入 mediaDir，其余站内路径改为绝对 http(s) URL。
// 不产出指向上传根目录外的 file://，供 Chromium 仅用临时目录相对路径渲染。
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
			if err := os.MkdirAll(mediaDir, 0o700); err == nil {
				n++
				ext := filepath.Ext(abs)
				if len(ext) > 8 {
					ext = ""
				}
				name := fmt.Sprintf("%03d%s", n, ext)
				dst := filepath.Join(mediaDir, name)
				if copyFile(abs, dst) == nil {
					return "](media/" + name + titleSuffix + ")"
				}
			}
		}
		if baseURL == "" {
			return m
		}
		// html/ 等不安全本地路径：仅走 http(s)；路由本身不公开 html/，导出也不落 file://
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
	// 无媒体目录时不拷贝，只做安全校验后的 http(s) 绝对化（避免 file://）
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
	// URL 路径清洗，去掉 .. 与多余斜杠
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
	// 与公开 /uploads 策略一致：整站 html/ 不可经旁路读出
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func toDOCX(ctx context.Context, tools Tools, work, md, title string) ([]byte, error) {
	in := filepath.Join(work, "in.md")
	out := filepath.Join(work, "out.docx")
	if err := os.WriteFile(in, []byte(md), 0o600); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, tools.pandocBin(),
		in, "-f", "markdown", "-t", "docx", "-o", out,
		"--resource-path="+work,
		"--metadata", "title="+title,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("Pandoc 导出 DOCX 失败: %s", msg)
	}
	return os.ReadFile(out)
}

func toPDF(ctx context.Context, tools Tools, work, md, title string) ([]byte, error) {
	in := filepath.Join(work, "in.md")
	htmlPath := filepath.Join(work, "out.html")
	pdfPath := filepath.Join(work, "out.pdf")
	if err := os.WriteFile(in, []byte(md), 0o600); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, tools.pandocBin(),
		in, "-f", "markdown", "-t", "html5", "-s",
		"--resource-path="+work,
		"--metadata", "title="+title,
		"-o", htmlPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("Pandoc 生成 HTML 失败: %s", msg)
	}

	raw, err := os.ReadFile(htmlPath)
	if err != nil {
		return nil, err
	}
	styled := injectPrintChrome(string(raw))
	if err := os.WriteFile(htmlPath, []byte(styled), 0o600); err != nil {
		return nil, err
	}

	chrome, err := tools.chromiumBin()
	if err != nil {
		return nil, err
	}
	// 仅打开临时目录内 HTML；图片为同目录相对路径 media/*，无需 --allow-file-access-from-files
	htmlURL := fileURL(htmlPath)
	baseArgs := []string{
		"--disable-gpu",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--print-to-pdf=" + pdfPath,
		htmlURL,
	}
	attempts := [][]string{
		append([]string{"--headless=new", "--no-pdf-header-footer"}, baseArgs...),
		append([]string{"--headless=new"}, baseArgs...),
		append([]string{"--headless"}, baseArgs...),
	}
	var lastMsg string
	ok := false
	for _, args := range attempts {
		_ = os.Remove(pdfPath)
		cCmd := exec.CommandContext(ctx, chrome, args...)
		var cErr bytes.Buffer
		cCmd.Stderr = &cErr
		cCmd.Stdout = &cErr
		if err := cCmd.Run(); err != nil {
			lastMsg = strings.TrimSpace(cErr.String())
			if lastMsg == "" {
				lastMsg = err.Error()
			}
			continue
		}
		if st, err := os.Stat(pdfPath); err == nil && st.Size() > 0 {
			ok = true
			break
		}
		lastMsg = "未生成 PDF 文件"
	}
	if !ok {
		return nil, fmt.Errorf("Chromium 导出 PDF 失败: %s", lastMsg)
	}
	data, err := os.ReadFile(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("读取 PDF 失败: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("Chromium 未生成 PDF")
	}
	return data, nil
}

func fileURL(abs string) string {
	abs = filepath.Clean(abs)
	u := url.URL{Scheme: "file"}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u.Path = p
	return u.String()
}

func injectPrintChrome(docHTML string) string {
	style := `
<style>
  @page { size: A4; margin: 18mm 16mm; }
  html, body {
    font-family: "Noto Sans CJK SC", "Noto Sans CJK", "Source Han Sans SC",
      "Microsoft YaHei", "PingFang SC", "Segoe UI", sans-serif;
    font-size: 11.5pt; line-height: 1.7; color: #1a1a1a; background: #fff;
  }
  body { max-width: 720px; margin: 0 auto; padding: 8px 0 24px; }
  h1 { font-size: 1.55em; letter-spacing: -.02em; margin: 0 0 .8em;
    padding-bottom: .35em; border-bottom: 1px solid #e5e7eb; }
  h2 { font-size: 1.25em; margin: 1.15em 0 .45em; }
  h3 { font-size: 1.1em; margin: 1em 0 .4em; }
  img { max-width: 100%; }
  pre {
    background: #f6f8fa; border: 1px solid #e5e7eb; padding: 12px 14px;
    border-radius: 8px; overflow: auto; font-size: .9em; white-space: pre-wrap;
  }
  code {
    font-family: ui-monospace, "JetBrains Mono", Menlo, Consolas, monospace;
    font-size: 85%;
  }
  :not(pre) > code { background: #eef6ff; padding: 1px 5px; border-radius: 4px; }
  blockquote {
    margin: 1em 0; padding: .5em 1em; color: #4b5563;
    border-left: 3px solid #0a8a5f; background: #f8fafc;
  }
  table { border-collapse: collapse; width: 100%; }
  th, td { border: 1px solid #d1d5db; padding: 8px 10px; }
  th { background: #f3f4f6; }
  a { color: #0a6b4a; }
  hr { border: none; border-top: 1px solid #e5e7eb; margin: 1.5em 0; }
</style>`
	if idx := strings.Index(strings.ToLower(docHTML), "</head>"); idx >= 0 {
		return docHTML[:idx] + style + docHTML[idx:]
	}
	return style + docHTML
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
