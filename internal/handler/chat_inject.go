package handler

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 超长文档按用户意图抽片段注入，避免整篇 40k 挤爆 Flash 上下文。
const (
	aiInjectSegmentThreshold = 12000 // 超过此字符数启用片段策略
	aiInjectHeadTailChars    = 2500  // 无关键词命中时头尾各取
	aiInjectWindowChars      = 1800  // 每个命中窗口半径
	aiInjectMaxWindows       = 4
)

type injectSpan struct{ lo, hi int }

// buildDocInjectBody 生成预读注入正文：短文全量；长文按 query 抽相关片段，否则头尾。
func buildDocInjectBody(title string, contentVersion int64, content, query string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = aiPrereadMaxChars
	}
	runes := []rune(content)
	total := len(runes)
	if total == 0 {
		return fmt.Sprintf("《%s》（content_version=%d）正文为空。", title, contentVersion)
	}
	if total <= maxChars && total <= aiInjectSegmentThreshold {
		return fmt.Sprintf("《%s》（content_version=%d，全文共 %d 字符）：\n\n%s",
			title, contentVersion, total, content)
	}

	if segs := extractQuerySegments(content, query, maxChars); segs != "" {
		return fmt.Sprintf("《%s》（content_version=%d，全文共 %d 字符，已按问题抽取相关片段；局部改用 replace_section，old_text 须逐字取自片段）：\n\n%s",
			title, contentVersion, total, segs)
	}

	headN, tailN := aiInjectHeadTailChars, aiInjectHeadTailChars
	if headN+tailN+64 > maxChars {
		headN = maxChars / 2
		tailN = maxChars - headN - 64
		if tailN < 0 {
			tailN = 0
		}
	}
	if headN > total {
		headN = total
	}
	tailStart := total - tailN
	if tailStart < headN {
		tailStart = headN
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("《%s》（content_version=%d，全文共 %d 字符，预读头尾；中间省略；局部改用 replace_section）：\n\n",
		title, contentVersion, total))
	b.WriteString(string(runes[:headN]))
	if tailStart < total {
		b.WriteString("\n\n…（中间省略）…\n\n")
		b.WriteString(string(runes[tailStart:]))
	}
	return b.String()
}

// extractQuerySegments 按 query 关键词在正文中取若干窗口，合并去重后截到 maxChars。
func extractQuerySegments(content, query string, maxChars int) string {
	keys := injectKeywords(query)
	if len(keys) == 0 {
		return ""
	}
	runes := []rune(content)
	total := len(runes)
	var spans []injectSpan
	for _, k := range keys {
		if len(spans) >= aiInjectMaxWindows {
			break
		}
		idx := strings.Index(content, k)
		if idx < 0 {
			idx = strings.Index(strings.ToLower(content), strings.ToLower(k))
		}
		if idx < 0 {
			continue
		}
		rOff := utf8.RuneCountInString(content[:idx])
		lo := rOff - aiInjectWindowChars
		if lo < 0 {
			lo = 0
		}
		hi := rOff + utf8.RuneCountInString(k) + aiInjectWindowChars
		if hi > total {
			hi = total
		}
		spans = append(spans, injectSpan{lo, hi})
	}
	if len(spans) == 0 {
		return ""
	}
	spans = mergeInjectSpans(spans)
	var b strings.Builder
	remain := maxChars
	for i, s := range spans {
		if remain <= 64 {
			break
		}
		chunkRunes := runes[s.lo:s.hi]
		if len(chunkRunes) > remain-32 {
			chunkRunes = chunkRunes[:remain-32]
		}
		if i > 0 {
			b.WriteString("\n\n…\n\n")
		}
		header := fmt.Sprintf("【片段 %d，字符 %d-%d】\n", i+1, s.lo, s.hi)
		b.WriteString(header)
		b.WriteString(string(chunkRunes))
		remain -= utf8.RuneCountInString(header) + len(chunkRunes) + 4
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String()
}

func mergeInjectSpans(in []injectSpan) []injectSpan {
	if len(in) == 0 {
		return in
	}
	for i := 0; i < len(in); i++ {
		for j := i + 1; j < len(in); j++ {
			if in[j].lo < in[i].lo {
				in[i], in[j] = in[j], in[i]
			}
		}
	}
	out := []injectSpan{in[0]}
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if s.lo <= last.hi+200 {
			if s.hi > last.hi {
				last.hi = s.hi
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// injectKeywords 从用户话里抽可用于定位的词（长度≥2，去停用编辑动词）。
func injectKeywords(query string) []string {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	stop := map[string]bool{
		"修改": true, "改成": true, "换成": true, "改为": true, "更新": true, "调整": true,
		"替换": true, "删掉": true, "删除": true, "补充": true, "加上": true, "增加": true,
		"追加": true, "重写": true, "文档": true, "一下": true, "请帮": true, "帮我": true,
		"把": true, "将": true, "的": true, "了": true, "和": true, "与": true,
	}
	var keys []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if utf8.RuneCountInString(s) < 2 || stop[s] || seen[s] || len(keys) >= 8 {
			return
		}
		seen[s] = true
		keys = append(keys, s)
	}
	for _, m := range reQuotedTitle.FindAllStringSubmatch(q, -1) {
		for i := 1; i < len(m); i++ {
			if m[i] != "" {
				add(m[i])
			}
		}
	}
	var buf []rune
	mode := 0 // 0 none · 1 汉字 · 2 字母数字
	flush := func() {
		if len(buf) >= 2 {
			add(string(buf))
		}
		buf = buf[:0]
		mode = 0
	}
	for _, r := range q {
		switch {
		case unicode.Is(unicode.Han, r):
			if mode == 2 {
				flush()
			}
			mode = 1
			buf = append(buf, r)
			if len(buf) >= 8 {
				flush()
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if mode == 1 {
				flush()
			}
			mode = 2
			buf = append(buf, r)
		default:
			flush()
		}
	}
	flush()
	return keys
}

// verifyReplaceResult 写后抽检文案。
func verifyReplaceResult(content, oldText, newText string) string {
	if newText != "" && !strings.Contains(content, newText) {
		return "校验失败：保存后未检出 new_text，请重新 read_document 核对。"
	}
	if oldText != "" && newText != oldText && !strings.Contains(newText, oldText) {
		if n := strings.Count(content, oldText); n > 0 {
			return fmt.Sprintf("校验注意：正文中仍有 %d 处 old_text（可能为未替换的其它匹配）。", n)
		}
	}
	if newText == "" {
		return "校验通过：目标片段已删除。"
	}
	return "校验通过：新内容已写入。"
}
