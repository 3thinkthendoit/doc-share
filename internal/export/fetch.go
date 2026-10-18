package export

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxRemoteImageBytes = 5 << 20 // 5 MiB
	remoteFetchTimeout  = 15 * time.Second
)

// localizeRemoteImages 将白名单主机上的 http(s) 图片拉入 mediaDir，其余远程图降级为 alt 文本。
// 这样 PDF 渲染时不会走 goldmark-pdf 内置 webFs（SSRF）。
func localizeRemoteImages(ctx context.Context, work, md string, allowHosts []string, seqStart int) (string, error) {
	allow := normalizeHostSet(allowHosts)
	if len(allow) == 0 {
		return blockRemoteImagesKeepLocal(md), nil
	}
	client := ssrfHTTPClient(allow)
	mediaDir := filepath.Join(work, "media")
	n := seqStart
	var firstErr error

	out := rewriteMarkdownImages(md, func(alt, rawURL, titleSuffix string) string {
		if !isHTTPURL(rawURL) {
			return formatMDImage(alt, rawURL, titleSuffix)
		}
		select {
		case <-ctx.Done():
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			return fallbackAlt(alt)
		default:
		}
		u, err := url.Parse(rawURL)
		if err != nil || u.Host == "" {
			return fallbackAlt(alt)
		}
		host := strings.ToLower(u.Hostname())
		if !allow[host] {
			return fallbackAlt(alt)
		}
		data, err := fetchURLBytes(ctx, client, u)
		if err != nil {
			return fallbackAlt(alt)
		}
		outData, ext, err := normalizeImageBytes(data, filepath.Ext(u.Path))
		if err != nil {
			return fallbackAlt(alt)
		}
		if err := os.MkdirAll(mediaDir, 0o700); err != nil {
			return fallbackAlt(alt)
		}
		n++
		name := fmt.Sprintf("%03d%s", n, ext)
		dst := filepath.Join(mediaDir, name)
		if err := os.WriteFile(dst, outData, 0o600); err != nil {
			return fallbackAlt(alt)
		}
		return formatMDImage(alt, "media/"+name, titleSuffix)
	})
	return out, firstErr
}

func blockRemoteImagesKeepLocal(md string) string {
	return rewriteMarkdownImages(md, func(alt, rawURL, titleSuffix string) string {
		if isHTTPURL(rawURL) {
			return fallbackAlt(alt)
		}
		return formatMDImage(alt, rawURL, titleSuffix)
	})
}

func formatMDImage(alt, rawURL, titleSuffix string) string {
	if titleSuffix != "" {
		return fmt.Sprintf("![%s](%s%s)", alt, rawURL, titleSuffix)
	}
	return fmt.Sprintf("![%s](%s)", alt, rawURL)
}

func fallbackAlt(alt string) string {
	alt = strings.TrimSpace(alt)
	if alt == "" {
		return "[image]"
	}
	return alt
}

func normalizeHostSet(hosts []string) map[string]bool {
	out := map[string]bool{}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		// url.Parse 需要 scheme 才能稳定解析 host / IPv6 / port
		raw := h
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host != "" {
			out[host] = true
		}
	}
	return out
}

func ssrfHTTPClient(allow map[string]bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 10 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			var last error
			for _, ipa := range ips {
				if isBlockedIP(ipa.IP) {
					last = fmt.Errorf("拒绝访问内网/保留地址: %s", ipa.IP)
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
		Timeout:   remoteFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("重定向过多")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("非法重定向 scheme")
			}
			host := strings.ToLower(req.URL.Hostname())
			if !allow[host] {
				return fmt.Errorf("重定向目标不在白名单: %s", host)
			}
			return nil
		},
	}
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

func fetchURLBytes(ctx context.Context, client *http.Client, u *url.URL) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "doc-share-export/1.0")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxRemoteImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRemoteImageBytes {
		return nil, fmt.Errorf("图片超过 %d 字节", maxRemoteImageBytes)
	}
	return data, nil
}
