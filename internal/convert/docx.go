package convert

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var headingRe = regexp.MustCompile(`(?i)heading\s*([1-9])`)

type seg struct {
	text             string
	bold, italic     bool
	href             string
}

// docxConverter 流式解析 word/document.xml（OOXML），边解析边产出 Markdown
type docxConverter struct {
	sb       strings.Builder
	styles   map[string]string          // styleId → 样式名（判断标题层级）
	rels     map[string]string          // rId → 外部链接 URL
	numFmt   map[string]map[int]string  // numId → ilvl → "bullet" / "decimal"
	counters map[string]int             // numId|ilvl → 有序列表当前序号

	// 段落上下文
	inP       bool
	pStyle    string
	hasNum    bool
	numID     int
	ilvl      int
	paraSegs  []seg
	curRun    *seg
	inT       bool
	pendingH  string // 超链接上下文
	inHyperN  int

	// 表格上下文
	inTable bool
	inCell  bool
	cellPS  []string // 当前单元格的段落
	rowCells []string
	tblRows [][]string
}

func docxToMarkdown(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("无法读取 docx 压缩包：%v", err)
	}
	c := &docxConverter{
		styles:   map[string]string{},
		rels:     map[string]string{},
		numFmt:   map[string]map[int]string{},
		counters: map[string]int{},
	}
	if b := zipEntry(zr, "word/styles.xml"); b != nil {
		c.parseStyles(b)
	}
	if b := zipEntry(zr, "word/numbering.xml"); b != nil {
		c.parseNumbering(b)
	}
	if b := zipEntry(zr, "word/_rels/document.xml.rels"); b != nil {
		c.parseRels(b)
	}
	doc := zipEntry(zr, "word/document.xml")
	if doc == nil {
		return "", fmt.Errorf("docx 缺少 word/document.xml，文件可能已损坏")
	}
	if err := c.parseDocument(doc); err != nil {
		return "", fmt.Errorf("解析 docx 内容失败：%v", err)
	}
	out := collapseBlank(c.sb.String())
	if out == "" {
		return "", fmt.Errorf("docx 中没有可提取的文本内容")
	}
	return out, nil
}

func zipEntry(zr *zip.Reader, name string) []byte {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			return nil
		}
		return b
	}
	return nil
}

func (c *docxConverter) parseStyles(b []byte) {
	var root struct {
		Styles []struct {
			ID   string `xml:"styleId,attr"`
			Name struct {
				Val string `xml:"val,attr"`
			} `xml:"name"`
		} `xml:"style"`
	}
	if xml.Unmarshal(b, &root) != nil {
		return
	}
	for _, s := range root.Styles {
		c.styles[s.ID] = s.Name.Val
	}
}

func (c *docxConverter) parseNumbering(b []byte) {
	var root struct {
		Abstract []struct {
			ID   int `xml:"abstractNumId,attr"`
			Lvls []struct {
				Ilvl   int `xml:"ilvl,attr"`
				NumFmt struct {
					Val string `xml:"val,attr"`
				} `xml:"numFmt"`
			} `xml:"lvl"`
		} `xml:"abstractNum"`
		Nums []struct {
			ID  int `xml:"numId,attr"`
			Abs struct {
				Val int `xml:"val,attr"`
			} `xml:"abstractNumId"`
		} `xml:"num"`
	}
	if xml.Unmarshal(b, &root) != nil {
		return
	}
	absFmt := map[int]map[int]string{}
	for _, a := range root.Abstract {
		m := map[int]string{}
		for _, l := range a.Lvls {
			m[l.Ilvl] = l.NumFmt.Val
		}
		absFmt[a.ID] = m
	}
	for _, n := range root.Nums {
		if fm, ok := absFmt[n.Abs.Val]; ok {
			c.numFmt[strconv.Itoa(n.ID)] = fm
		}
	}
}

func (c *docxConverter) parseRels(b []byte) {
	var root struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
			Mode   string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if xml.Unmarshal(b, &root) != nil {
		return
	}
	for _, r := range root.Rels {
		if r.Mode == "External" {
			c.rels[r.ID] = r.Target
		}
	}
}

func (c *docxConverter) parseDocument(b []byte) error {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			c.start(t)
		case xml.EndElement:
			c.end(t)
		case xml.CharData:
			if c.inT && c.curRun != nil {
				c.curRun.text += string(t)
			}
		}
	}
}

func (c *docxConverter) start(t xml.StartElement) {
	switch t.Name.Local {
	case "p":
		c.inP, c.pStyle, c.hasNum, c.numID, c.ilvl = true, "", false, 0, 0
		c.paraSegs = nil
	case "tbl":
		c.inTable, c.tblRows = true, nil
	case "tr":
		c.rowCells = nil
	case "tc":
		c.inCell, c.cellPS = true, nil
	case "pPr":
	case "pStyle":
		c.pStyle = attrVal(t, "val")
	case "numPr":
		c.hasNum = true
	case "numId":
		c.numID, _ = strconv.Atoi(attrVal(t, "val"))
	case "ilvl":
		c.ilvl, _ = strconv.Atoi(attrVal(t, "val"))
	case "hyperlink":
		c.inHyperN++
		if id := attrVal(t, "id"); id != "" {
			c.pendingH = c.rels[id]
		}
	case "r":
		c.curRun = &seg{href: c.pendingH}
	case "rPr":
	case "b":
		if c.curRun != nil {
			c.curRun.bold = !hasAttr(t, "val", "false", "0")
		}
	case "i":
		if c.curRun != nil {
			c.curRun.italic = !hasAttr(t, "val", "false", "0")
		}
	case "t":
		c.inT = true
	case "tab":
		if c.curRun != nil {
			c.curRun.text += "    "
		}
	case "br", "cr":
		if c.curRun != nil {
			c.curRun.text += "\n"
		}
	}
}

func (c *docxConverter) end(t xml.EndElement) {
	switch t.Name.Local {
	case "t":
		c.inT = false
	case "r":
		if c.curRun != nil && strings.TrimSpace(c.curRun.text) != "" {
			c.paraSegs = append(c.paraSegs, *c.curRun)
		}
		c.curRun = nil
	case "hyperlink":
		c.inHyperN--
		if c.inHyperN <= 0 {
			c.pendingH = ""
		}
	case "p":
		if c.inP {
			c.inP = false
			c.emitPara()
		}
	case "tc":
		if c.inCell {
			c.inCell = false
			c.rowCells = append(c.rowCells, strings.Join(c.cellPS, " "))
		}
	case "tr":
		if len(c.rowCells) > 0 {
			c.tblRows = append(c.tblRows, c.rowCells)
		}
	case "tbl":
		if c.inTable {
			c.inTable = false
			c.emitTable()
		}
	}
}

// emitPara 段落结束：标题 / 列表 / 正文
func (c *docxConverter) emitPara() {
	text := renderSegs(c.paraSegs)
	if c.inCell {
		c.cellPS = append(c.cellPS, strings.ReplaceAll(strings.ReplaceAll(text, "|", "\\|"), "\n", " "))
		return
	}
	// stripMd 仅用于判空（防止只有格式标记的空段被误判有内容），不能用于输出
	if strings.TrimSpace(stripMd(text)) == "" {
		return // 空段落跳过
	}
	if n := headingLevel(c.pStyle, c.styles); n > 0 {
		c.sb.WriteString("\n" + strings.Repeat("#", n) + " " + strings.TrimSpace(text) + "\n\n")
		return
	}
	if c.hasNum {
		c.sb.WriteString(c.listPrefix() + text + "\n\n")
		return
	}
	c.sb.WriteString(text + "\n\n")
}

// listPrefix 依据 numbering.xml 生成列表前缀（有序递增，无序 -）
func (c *docxConverter) listPrefix() string {
	indent := strings.Repeat("  ", min(c.ilvl, 8))
	key := strconv.Itoa(c.numID) + "|" + strconv.Itoa(c.ilvl)
	fmtStr := ""
	if m, ok := c.numFmt[strconv.Itoa(c.numID)]; ok {
		fmtStr = m[c.ilvl]
	}
	switch fmtStr {
	case "decimal", "lowerLetter", "upperLetter", "lowerRoman", "upperRoman":
		// 更深层级序号归零
		for l := c.ilvl + 1; l <= 8; l++ {
			delete(c.counters, strconv.Itoa(c.numID)+"|"+strconv.Itoa(l))
		}
		c.counters[key]++
		return indent + orderedLabel(fmtStr, c.counters[key]) + " "
	default:
		return indent + "- "
	}
}

func orderedLabel(fmtStr string, n int) string {
	switch fmtStr {
	case "lowerLetter":
		return string(rune('a' + (n-1)%26))
	case "upperLetter":
		return string(rune('A' + (n-1)%26))
	case "lowerRoman":
		return strings.ToLower(roman(n))
	case "upperRoman":
		return roman(n)
	}
	return strconv.Itoa(n)
}

func roman(n int) string {
	if n <= 0 || n > 3999 {
		return strconv.Itoa(n)
	}
	vals := []struct {
		v int
		s string
	}{{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"},
		{50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}}
	var sb strings.Builder
	for _, p := range vals {
		for n >= p.v {
			sb.WriteString(p.s)
			n -= p.v
		}
	}
	return sb.String()
}

// emitTable 表格结束：输出管道表格（首行作表头）
func (c *docxConverter) emitTable() {
	if len(c.tblRows) == 0 {
		return
	}
	cols := 0
	for _, r := range c.tblRows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return
	}
	c.sb.WriteString("\n")
	for i, r := range c.tblRows {
		c.sb.WriteString("| ")
		for j := 0; j < cols; j++ {
			cell := ""
			if j < len(r) {
				cell = strings.TrimSpace(r[j])
			}
			c.sb.WriteString(cell + " | ")
		}
		c.sb.WriteString("\n")
		if i == 0 {
			c.sb.WriteString("|")
			for j := 0; j < cols; j++ {
				c.sb.WriteString(" --- |")
			}
			c.sb.WriteString("\n")
		}
	}
	c.sb.WriteString("\n")
}

// renderSegs 合并相邻同格式片段并输出加粗/斜体/链接
func renderSegs(segs []seg) string {
	var out strings.Builder
	for i := 0; i < len(segs); i++ {
		s := segs[i]
		for i+1 < len(segs) && segs[i+1].bold == s.bold && segs[i+1].italic == s.italic && segs[i+1].href == s.href {
			i++
			s.text += segs[i].text
		}
		text := s.text
		if s.italic {
			text = "*" + text + "*"
		}
		if s.bold {
			text = "**" + text + "**"
		}
		if s.href != "" {
			text = "[" + text + "](" + s.href + ")"
		}
		out.WriteString(text)
	}
	return out.String()
}

// headingLevel 由样式 ID/名称判断标题层级（兼容中文文档 styleId="1" + name="heading 1"）
func headingLevel(styleID string, styles map[string]string) int {
	if m := headingRe.FindStringSubmatch(styleID); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := headingRe.FindStringSubmatch(styles[styleID]); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func stripMd(s string) string {
	return strings.NewReplacer("*", "", "[", "", "]", "", "(", "", ")", "", "\\", "").Replace(s)
}

func attrVal(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func hasAttr(t xml.StartElement, local, v1, v2 string) bool {
	v := attrVal(t, local)
	return v == v1 || v == v2
}
