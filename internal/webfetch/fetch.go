package webfetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	maxBodyBytes = 8 << 20
	fetchTimeout = 15 * time.Second
	maxRedirects = 3
)

// Page 是一次公网读取的结果。Text 为可读纯文本；Markdown 为 HTML 页转出的结构（含图片原地址）。
type Page struct {
	FinalURL    string
	Title       string
	Text        string
	Markdown    string
	ContentType string
}

var blockedNets []*net.IPNet

func init() {
	// 除 IsPrivate/IsLoopback 外，额外挡住常见 SSRF 目标与文档保留网段。
	for _, cidr := range []string{
		"0.0.0.0/8",       // 「本机任意」语义，含 0.0.0.1
		"10.0.0.0/8",      // RFC1918
		"100.64.0.0/10",   // CGNAT / RFC6598
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local / 云 metadata
		"172.16.0.0/12",   // RFC1918
		"192.168.0.0/16",  // RFC1918
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved
		"::/128",          // unspecified
		"::1/128",         // loopback
		"fc00::/7",        // unique local
		"fe80::/10",       // link-local
		"ff00::/8",        // multicast
		"2001:db8::/32",   // documentation
	} {
		_, n, err := net.ParseCIDR(cidr)
		if err == nil {
			blockedNets = append(blockedNets, n)
		}
	}
}

// Read 读取一个公网 http(s) 页面。内网、回环、链路本地、CGNAT 和保留地址会被拒绝。
func Read(ctx context.Context, rawURL string) (Page, error) {
	u, err := parsePublicURL(rawURL)
	if err != nil {
		return Page{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.5")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	client := publicHTTPClient()
	res, err := client.Do(req)
	if err != nil {
		return Page{}, fmt.Errorf("读取失败: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Page{}, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes+1))
	if err != nil {
		return Page{}, err
	}
	if len(body) > maxBodyBytes {
		return Page{}, fmt.Errorf("页面超过 %d 字节上限", maxBodyBytes)
	}
	ct := res.Header.Get("Content-Type")
	title, text, err := readable(ct, body)
	if err != nil {
		return Page{}, err
	}
	final := displayURL(u)
	if res.Request != nil && res.Request.URL != nil {
		final = displayURL(res.Request.URL)
	}
	if challengePage(final, title, text) {
		return Page{}, fmt.Errorf("页面要求验证，无法读取正文")
	}
	page := Page{FinalURL: final, Title: title, Text: text, ContentType: ct}
	if strings.Contains(strings.ToLower(ct), "html") || looksLikeHTML(body) {
		mdTitle, md := HTMLMarkdown(final, body)
		if strings.TrimSpace(page.Title) == "" {
			page.Title = mdTitle
		}
		page.Markdown = md
	}
	return page, nil
}

// Fetch 按与 Read 相同的公网限制下载资源。referer 可空，用于图床校验来源。
func Fetch(ctx context.Context, rawURL, referer string, limit int64) ([]byte, string, error) {
	u, err := parsePublicURL(rawURL)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = maxBodyBytes
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	if strings.TrimSpace(referer) != "" {
		req.Header.Set("Referer", referer)
	}
	res, err := publicHTTPClient().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("读取失败: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > limit {
		return nil, "", fmt.Errorf("文件超过 %d 字节上限", limit)
	}
	return body, res.Header.Get("Content-Type"), nil
}

// challengePage 认出反爬验证页。
// 微信验证地址一律拒绝。其余验证路径只在正文很短时拒绝，避免 /challenge-types 这类长文被误伤。
func challengePage(finalURL, title, text string) bool {
	if strings.Contains(strings.ToLower(finalURL), "wappoc_appmsgcaptcha") {
		return true
	}
	short := utf8.RuneCountInString(text) <= 800
	if short && challengePath(finalURL) {
		return true
	}
	if !short {
		return false
	}
	blob := title + "\n" + text
	for _, marker := range []string{
		"环境异常",
		"完成验证后即可继续访问",
		"当前环境异常",
		"请完成安全验证",
		"Just a moment",
		"Attention Required",
		"cf-browser-verification",
	} {
		if strings.Contains(blob, marker) {
			return true
		}
	}
	return false
}

// challengePath 只匹配路径段本身是验证页的地址，不把 challenge-types 当成 /challenge。
func challengePath(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	for _, seg := range strings.Split(strings.ToLower(u.Path), "/") {
		switch seg {
		case "captcha", "recaptcha", "challenge", "cf-browser-verification":
			return true
		}
	}
	return false
}

func displayURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	out := *u
	out.User = nil
	out.RawQuery = ""
	out.Fragment = ""
	return out.String()
}

// CheckPublicURL 只校验链接是否允许抓取，不发起请求。
func CheckPublicURL(raw string) error {
	_, err := parsePublicURL(raw)
	return err
}

func parsePublicURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("url 不合法")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("只支持 http 或 https")
	}
	if u.User != nil {
		return nil, fmt.Errorf("url 不能包含账号密码")
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return nil, fmt.Errorf("拒绝访问内网或保留地址")
	}
	if ip := net.ParseIP(host); ip != nil && blockedIP(ip) {
		return nil, fmt.Errorf("拒绝访问内网或保留地址")
	}
	return u, nil
}

func publicHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 10 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if ip := net.ParseIP(host); ip != nil {
				if blockedIP(ip) {
					return nil, fmt.Errorf("拒绝访问内网或保留地址")
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			var last error
			for _, ipa := range ips {
				if blockedIP(ipa.IP) {
					last = fmt.Errorf("拒绝访问内网或保留地址")
					continue
				}
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			if last == nil {
				last = fmt.Errorf("无可用地址")
			}
			return nil, last
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("重定向过多")
			}
			if _, err := parsePublicURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

func blockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func readable(contentType string, body []byte) (string, string, error) {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "html") || looksLikeHTML(body) {
		title, text := HTMLText(body)
		return title, text, nil
	}
	if strings.Contains(ct, "text/") || strings.Contains(ct, "json") || strings.Contains(ct, "xml") || ct == "" {
		if looksLikeHTML(body) {
			title, text := HTMLText(body)
			return title, text, nil
		}
		return "", strings.TrimSpace(string(body)), nil
	}
	return "", "", fmt.Errorf("不是可阅读的文本页面")
}

func looksLikeHTML(body []byte) bool {
	s := strings.ToLower(string(body[:min(len(body), 512)]))
	return strings.Contains(s, "<html") || strings.Contains(s, "<!doctype html") || strings.Contains(s, "<body")
}

// HTMLText 抽出标题和可见文本，丢掉 script/style。
func HTMLText(body []byte) (string, string) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", strings.TrimSpace(string(body))
	}
	skip := map[string]bool{
		"script": true, "style": true, "noscript": true, "template": true, "svg": true, "iframe": true,
	}
	block := map[string]bool{
		"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true,
		"h4": true, "h5": true, "h6": true, "section": true, "article": true, "header": true,
		"footer": true, "blockquote": true, "pre": true, "table": true,
	}
	var title, h1 string
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skip[n.Data] {
			return
		}
		if n.Type == html.ElementNode && n.Data == "title" && strings.TrimSpace(title) == "" {
			title = nodeText(n)
			return
		}
		if n.Type == html.ElementNode && n.Data == "meta" && strings.TrimSpace(title) == "" {
			if t := metaTitle(n); t != "" {
				title = t
			}
		}
		if n.Type == html.ElementNode && n.Data == "h1" && strings.TrimSpace(h1) == "" {
			h1 = nodeText(n)
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && block[n.Data] {
			b.WriteByte('\n')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if strings.TrimSpace(title) == "" {
		title = h1
	}
	return collapseWS(title), collapseWS(b.String())
}

func metaTitle(n *html.Node) string {
	var key, content string
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "property", "name":
			key = strings.ToLower(strings.TrimSpace(a.Val))
		case "content":
			content = strings.TrimSpace(a.Val)
		}
	}
	if content == "" {
		return ""
	}
	if key == "og:title" || key == "twitter:title" {
		return content
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func collapseWS(s string) string {
	var b strings.Builder
	prevNL := false
	prevSpace := false
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r':
			if !prevNL && b.Len() > 0 {
				b.WriteByte('\n')
			}
			prevNL = true
			prevSpace = false
		case unicode.IsSpace(r):
			if !prevNL && !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			prevSpace = true
		default:
			b.WriteRune(r)
			prevNL = false
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}
