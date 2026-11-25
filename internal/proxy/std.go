package proxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func isVLESS(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "vless://")
}

// parseStdProxy 解析 http:// / https:// / socks5:// / socks5h:// 代理地址。
// socks5h 归一为 socks5：Go 的 SOCKS5 拨号对域名目标本就交由代理端解析（远程 DNS）。
func parseStdProxy(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		// 不包裹 url.Parse 的原始错误：其中带完整地址，可能含代理账号密码
		return nil, errors.New("代理地址无法解析")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5":
		u.Scheme = strings.ToLower(u.Scheme)
	case "socks5h":
		u.Scheme = "socks5"
	default:
		return nil, fmt.Errorf("不支持的代理协议 %q，仅支持 vless/http/https/socks5", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("代理地址缺少主机")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return nil, fmt.Errorf("代理端口非法: %q", p)
		}
	} else if u.Scheme == "socks5" {
		return nil, errors.New("socks5 代理地址缺少端口")
	}
	return u, nil
}

func stdTransport(raw string) (*http.Transport, error) {
	u, err := parseStdProxy(raw)
	if err != nil {
		return nil, err
	}
	return &http.Transport{
		Proxy:                 http.ProxyURL(u),
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}, nil
}

// Validate 校验代理地址（空串合法，表示直连）。
func Validate(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if isVLESS(raw) {
		_, err := ParseVLESS(raw)
		return err
	}
	_, err := parseStdProxy(raw)
	return err
}

// Preview 返回不含凭据的展示串（协议 + host:port），用于管理界面回显；无法解析返回空。
func Preview(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if isVLESS(raw) {
		if n, err := ParseVLESS(raw); err == nil {
			return "vless://" + n.ServerPreview()
		}
		return ""
	}
	u, err := parseStdProxy(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
