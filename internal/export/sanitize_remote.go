package export

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 参考定义：[id]: url 或 [id]: <url> "title"
var mdLinkRefDefRe = regexp.MustCompile(`(?mi)^([ \t]{0,3}\[[^\]\n]+\]:[ \t]*)<?([^\s>]+)>?(.*)$`)

// localizeOrNeutralizeRefs 处理链接参考定义中的远程 URL：
// 白名单主机则拉到 media/；否则改为 #，避免 goldmark-pdf webFs SSRF。
func localizeOrNeutralizeRefs(ctx context.Context, work, md string, allowHosts []string, seqStart int) (string, int, error) {
	allow := normalizeHostSet(allowHosts)
	client := ssrfHTTPClient(allow)
	mediaDir := filepath.Join(work, "media")
	n := seqStart
	var firstErr error

	out := mdLinkRefDefRe.ReplaceAllStringFunc(md, func(line string) string {
		sub := mdLinkRefDefRe.FindStringSubmatch(line)
		if len(sub) < 3 {
			return line
		}
		prefix, dest, rest := sub[1], sub[2], ""
		if len(sub) > 3 {
			rest = sub[3]
		}
		if !isRemoteOrProtocolRelative(dest) {
			return line
		}
		// 协议相对 //host → 当作 https
		raw := dest
		if strings.HasPrefix(dest, "//") {
			raw = "https:" + dest
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return prefix + "#" + rest
		}
		host := strings.ToLower(u.Hostname())
		if !allow[host] {
			return prefix + "#" + rest
		}
		select {
		case <-ctx.Done():
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			return prefix + "#" + rest
		default:
		}
		data, err := fetchURLBytes(ctx, client, u)
		if err != nil {
			return prefix + "#" + rest
		}
		outData, ext, err := normalizeImageBytes(data, filepath.Ext(u.Path))
		if err != nil {
			return prefix + "#" + rest
		}
		if err := os.MkdirAll(mediaDir, 0o700); err != nil {
			return prefix + "#" + rest
		}
		n++
		name := fmt.Sprintf("%03d%s", n, ext)
		if err := os.WriteFile(filepath.Join(mediaDir, name), outData, 0o600); err != nil {
			return prefix + "#" + rest
		}
		return prefix + "media/" + name + rest
	})
	return out, n, firstErr
}

func neutralizeRemoteImageRefs(md string) string {
	// 无白名单时的快速路径：全部远程参考定义改 #
	return mdLinkRefDefRe.ReplaceAllStringFunc(md, func(line string) string {
		sub := mdLinkRefDefRe.FindStringSubmatch(line)
		if len(sub) < 3 {
			return line
		}
		if isRemoteOrProtocolRelative(sub[2]) {
			rest := ""
			if len(sub) > 3 {
				rest = sub[3]
			}
			return sub[1] + "#" + rest
		}
		return line
	})
}

func isRemoteOrProtocolRelative(u string) bool {
	u = strings.TrimSpace(u)
	if u == "" {
		return false
	}
	lower := strings.ToLower(u)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return true
	}
	return strings.HasPrefix(u, "//")
}
