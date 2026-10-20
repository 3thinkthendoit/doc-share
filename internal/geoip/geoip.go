// Package geoip 用内置 ip2region v4 离线库做 IP → 归属地反查。
package geoip

import (
	"embed"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

//go:embed data/ip2region_v4.xdb
var xdbFS embed.FS

var (
	once     sync.Once
	searcher *xdb.Searcher
	mu       sync.Mutex // Searcher.Search 非并发安全
)

func initSearcher() {
	once.Do(func() {
		b, err := xdbFS.ReadFile("data/ip2region_v4.xdb")
		if err != nil {
			return
		}
		s, err := xdb.NewWithBuffer(xdb.IPv4, b)
		if err != nil {
			return
		}
		searcher = s
	})
}

// Lookup 返回可读归属地（国内多为省名如「广东」；国外为国家/省/市简写）；查不到返回空串。
// 仅支持 IPv4；IPv6 / 非法 IP 返回空。
func Lookup(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	// 去掉 IPv4-mapped IPv6 前缀
	if strings.HasPrefix(ip, "::ffff:") {
		ip = strings.TrimPrefix(ip, "::ffff:")
	}
	initSearcher()
	if searcher == nil {
		return ""
	}
	mu.Lock()
	raw, err := searcher.Search(ip)
	mu.Unlock()
	if err != nil || raw == "" {
		return ""
	}
	return formatRegion(raw)
}

// ShortRegion 将已存归属地压成作者行展示：国内「中国 广东 深圳」→「广东」；其余原样。
func ShortRegion(region string) string {
	region = strings.TrimSpace(region)
	if region == "" || region == "内网" {
		return region
	}
	fields := strings.Fields(region)
	if len(fields) == 0 {
		return region
	}
	if fields[0] == "中国" {
		if len(fields) >= 2 {
			return fields[1]
		}
		return "中国"
	}
	return region
}

// formatRegion 将 ip2region 原始「国家|省份|城市|ISP|iso」压成展示用归属地。
func formatRegion(raw string) string {
	parts := strings.Split(raw, "|")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "内网IP" || p == "本机地址" || p == "局域网" {
			return "内网"
		}
	}
	partAt := func(i int) string {
		if i >= len(parts) {
			return ""
		}
		p := strings.TrimSpace(parts[i])
		if p == "" || p == "0" {
			return ""
		}
		return p
	}
	country, province, city := partAt(0), partAt(1), partAt(2)
	if country == "中国" {
		if province != "" {
			return province
		}
		return "中国"
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range []string{country, province, city} {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return strings.Join(out, " ")
}
