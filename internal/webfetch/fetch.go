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

	"golang.org/x/net/html"
)

const (
	maxBodyBytes = 1 << 20
	fetchTimeout = 15 * time.Second
	maxRedirects = 3
)

// Page 是一次公网读取的结果。Text 为去掉脚本和样式后的可读文本。
type Page struct {
	FinalURL    string
	Title       string
	Text        string
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
	req.Header.Set("User-Agent", "doc-share-agent/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.5")
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
	return Page{FinalURL: final, Title: title, Text: text, ContentType: ct}, nil
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
	var title string
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skip[n.Data] {
			return
		}
		if n.Type == html.ElementNode && n.Data == "title" && title == "" {
			title = nodeText(n)
			return
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
	return collapseWS(title), collapseWS(b.String())
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
