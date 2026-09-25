package util

import "strings"

// MaskName 昵称脱敏：保留首尾字符，中间以单个 * 代替（不暴露原始长度）。
// 规则：1 字 → "*"；2 字 → "首*"；3 字及以上 → "首*尾"；空串原样返回。
// 示例：张三 → 张*，我是一个动词 → 我*词，Tom → T*m
func MaskName(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	switch {
	case len(runes) == 0:
		return ""
	case len(runes) == 1:
		return "*"
	case len(runes) == 2:
		return string(runes[0]) + "*"
	default:
		return string(runes[0]) + "*" + string(runes[len(runes)-1])
	}
}
