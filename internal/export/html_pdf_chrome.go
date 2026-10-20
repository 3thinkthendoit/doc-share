package export

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// errChromeUnavailable 本机找不到 Chrome/Chromium（可回退或提示安装）。
var errChromeUnavailable = errors.New("chrome unavailable")

// exportHTMLChrome 用无头 Chrome 打开本地站点并 PrintToPDF。
// 复杂 CSS/彩图试卷只能靠浏览器引擎；纯 Go htmlpdf 会叠字、丢图。
// 仅放行本机 FileServer 源；其余 HTTP(S)/内网请求一律拦截，降低导出时的 SSRF。
func exportHTMLChrome(ctx context.Context, siteDir, title string) ([]byte, error) {
	chromePath, err := findChrome()
	if err != nil {
		return nil, err
	}
	entry := filepath.Join(siteDir, htmlEntryName)
	if _, err := os.Stat(entry); err != nil {
		return nil, fmt.Errorf("缺少入口 %s", htmlEntryName)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: http.FileServer(http.Dir(siteDir))}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = ln.Close()
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	allowedOrigin := fmt.Sprintf("http://127.0.0.1:%d", port)
	pageURL := allowedOrigin + "/" + htmlEntryName

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		// 新版 Chrome 需 headless=new；布尔 true 会卡在取 document root
		chromedp.Flag("headless", "new"),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("hide-scrollbars", true),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	// Events 须在主流程先调用（启动 target），再在 goroutine 里消费。
	dialogs := chromedp.Events(taskCtx, page.JavascriptDialogOpening)
	paused := chromedp.Events(taskCtx, fetch.RequestPaused)
	go func() {
		for _, err := range dialogs {
			if err != nil {
				return
			}
			_, _ = chromedp.Call(taskCtx, page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{Accept: true})
		}
	}()
	go func() {
		for ev, err := range paused {
			if err != nil {
				return
			}
			u := ""
			if ev.Request != nil {
				u = ev.Request.URL
			}
			if chromeRequestAllowed(u, allowedOrigin) {
				_, _ = chromedp.Call(taskCtx, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: ev.RequestID})
				continue
			}
			_, _ = chromedp.Call(taskCtx, fetch.FailRequest, fetch.FailRequestParams{
				RequestID:   ev.RequestID,
				ErrorReason: network.ErrorReasonBlockedByClient,
			})
		}
	}()

	if _, err := chromedp.Call(taskCtx, fetch.Enable, fetch.EnableParams{
		Patterns: []*fetch.RequestPattern{{
			URLPattern:   "*",
			RequestStage: fetch.RequestStageRequest,
		}},
	}); err != nil {
		return nil, fmt.Errorf("Chrome 导出 PDF 失败: %w", err)
	}

	if err := chromedp.Do(taskCtx,
		chromedp.Navigate(pageURL),
		chromedp.WaitReady("body"),
		chromedp.Sleep(500*time.Millisecond),
	); err != nil {
		return nil, fmt.Errorf("Chrome 导出 PDF 失败: %w", err)
	}

	pdf, err := chromedp.Run(taskCtx, chromedp.PrintToPDF(
		chromedp.PDFPrintBackground(),
		chromedp.PDFPaper(chromedp.PaperA4),
		chromedp.PDFMargins(0.4, 0.4, 0.4, 0.4),
	))
	if err != nil {
		return nil, fmt.Errorf("Chrome 导出 PDF 失败: %w", err)
	}
	if len(pdf) == 0 {
		return nil, fmt.Errorf("PDF 导出结果为空")
	}
	_ = title // 标题写入 HTML <title>（prepareHTMLForChromePDF）
	return pdf, nil
}

// chromeRequestAllowed 仅放行本机导出 FileServer、data/blob/about；拒绝其余（含内网 IP）。
func chromeRequestAllowed(rawURL, allowedOrigin string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || allowedOrigin == "" {
		return false
	}
	lower := strings.ToLower(rawURL)
	switch {
	case strings.HasPrefix(lower, "data:"):
		return true
	case strings.HasPrefix(lower, "blob:"):
		return true
	case strings.HasPrefix(lower, "about:"):
		return true
	}
	return strings.HasPrefix(rawURL, allowedOrigin)
}

// findChrome 按环境变量与常见安装路径查找 Chrome/Chromium。
func findChrome() (string, error) {
	if p := strings.TrimSpace(os.Getenv("DOC_SHARE_CHROME_PATH")); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("%w: DOC_SHARE_CHROME_PATH=%s 无效", errChromeUnavailable, p)
	}
	candidates := chromeCandidates()
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if filepath.IsAbs(c) {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: 未找到 Chrome/Chromium，请安装或设置 DOC_SHARE_CHROME_PATH", errChromeUnavailable)
}

func chromeCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		prog := os.Getenv("PROGRAMFILES")
		prog86 := os.Getenv("PROGRAMFILES(X86)")
		return []string{
			filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(prog, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(prog86, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(local, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(prog, `Microsoft\Edge\Application\msedge.exe`),
			"chrome.exe", "msedge.exe", "chromium.exe",
		}
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"google-chrome", "chromium", "chromium-browser",
		}
	default:
		return []string{
			"/usr/bin/chromium-browser",
			"/usr/bin/chromium",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/google-chrome",
			"chromium-browser", "chromium", "google-chrome", "google-chrome-stable",
		}
	}
}
