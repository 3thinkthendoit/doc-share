package convert

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode"

	rpdf "github.com/ledongthuc/pdf"
)

// pdfToMarkdown 提取 PDF 各页文本，按行坐标重建阅读顺序，
// 并做段落合并（中文换行直接拼接，英文行未结束且下一行小写开头则合并）。
func pdfToMarkdown(data []byte) (string, error) {
	rd := bytes.NewReader(data)
	r, err := rpdf.NewReader(rd, int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("无法解析 PDF：%v", err)
	}
	if r.NumPage() == 0 {
		return "", fmt.Errorf("PDF 没有可提取的文本内容")
	}

	var sb strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		rows, err := p.GetTextByRow()
		if err != nil {
			continue // 单页失败不阻断整体转换
		}
		// Y 自底向上递增，阅读顺序为从上到下：按 Y 降序
		sort.SliceStable(rows, func(a, b int) bool { return rows[a].Position > rows[b].Position })

		var prev string
		first := sb.Len() == 0
		for _, row := range rows {
			line := joinRowTexts(row.Content)
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !first && shouldJoin(prev, line) {
				sb.WriteString(lineGap(prev, line))
				sb.WriteString(line)
			} else {
				if !first {
					sb.WriteString("\n\n")
				}
				sb.WriteString(line)
			}
			prev = line
			first = false
		}
	}
	out := collapseBlank(sb.String())
	if out == "" {
		return "", fmt.Errorf("PDF 没有可提取的文本内容（可能是扫描件）")
	}
	return out, nil
}

// joinRowTexts 同一行内按 X 排序拼接文本片段：
// 片段间隔小或两侧为中日韩字符直接相连，否则补空格
func joinRowTexts(ts rpdf.TextHorizontal) string {
	sort.SliceStable(ts, func(a, b int) bool { return ts[a].X < ts[b].X })
	var sb strings.Builder
	var last *rpdf.Text
	for i := range ts {
		t := ts[i].S
		if t == "" {
			continue
		}
		if last != nil {
			gap := ts[i].X - (last.X + last.W)
			noCoord := last.W == 0 && ts[i].X == 0 // 简单 PDF 无坐标信息，降级为直接补空格
			if (noCoord || gap > 1.0) && !(isCJKText(last.S) && isCJKText(t)) {
				sb.WriteString(" ")
			}
		}
		sb.WriteString(t)
		last = &ts[i]
	}
	return sb.String()
}

// shouldJoin 判断上一行与当前行是否属于同一段落（硬换行合并）
func shouldJoin(prev, cur string) bool {
	pr := []rune(strings.TrimRight(prev, " \t"))
	cr := []rune(strings.TrimLeft(cur, " \t"))
	if len(pr) == 0 || len(cr) == 0 {
		return false
	}
	last, first := pr[len(pr)-1], cr[0]
	// 中日韩文本排版换行直接拼接
	if isCJK(last) || isCJK(first) {
		return true
	}
	// 英文：上一行不是句子结尾且下一行以小写/逗号等开头 → 同段
	if unicode.IsLower(first) || first == ',' || first == '-' {
		return !endsWithSentence(last)
	}
	return false
}

// lineGap 拼接时两侧需要的连接符
func lineGap(prev, cur string) string {
	pr := []rune(strings.TrimRight(prev, " \t"))
	cr := []rune(strings.TrimLeft(cur, " \t"))
	if len(pr) == 0 || len(cr) == 0 {
		return ""
	}
	if isCJK(pr[len(pr)-1]) && isCJK(cr[0]) {
		return "" // 中文字符之间不需要空格
	}
	return " "
}

func endsWithSentence(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == ':' || r == '。' || r == '！' || r == '？' || r == '：' || r == '；' || r == ';'
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func isCJKText(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return isCJK(r)
		}
	}
	return false
}
