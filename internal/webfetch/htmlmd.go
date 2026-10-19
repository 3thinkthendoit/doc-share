package webfetch

import (
	"bytes"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// HTMLMarkdown 把 HTML 转成标题、段落、列表、引用、代码和图片的 Markdown。
// 公众号页优先取 id=js_content，避免把推荐栏和按钮文案写进正文。
func HTMLMarkdown(pageURL string, body []byte) (string, string) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", strings.TrimSpace(string(body))
	}
	title := docTitle(doc)
	root := articleRoot(doc)
	if root == nil {
		root = doc
	}
	md := collapseBlank(renderBlocks(root, pageURL))
	return title, md
}

func articleRoot(doc *html.Node) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil || n == nil {
			return
		}
		if n.Type == html.ElementNode && strings.EqualFold(attr(n, "id"), "js_content") {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found
}

func docTitle(doc *html.Node) string {
	var title, h1 string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
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
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if strings.TrimSpace(title) == "" {
		title = h1
	}
	return collapseWS(title)
}

func renderBlocks(n *html.Node, base string) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(renderBlock(c, base))
	}
	return b.String()
}

func renderBlock(n *html.Node, base string) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		t := strings.TrimSpace(n.Data)
		if t == "" {
			return ""
		}
		return t + "\n\n"
	}
	if n.Type != html.ElementNode || skipNode(n.Data) {
		return ""
	}
	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		s := strings.TrimSpace(renderInline(n, base))
		if s == "" {
			return ""
		}
		return strings.Repeat("#", int(n.Data[1]-'0')) + " " + s + "\n\n"
	case "p", "figcaption":
		s := strings.TrimSpace(renderInline(n, base))
		if s == "" {
			return ""
		}
		return s + "\n\n"
	case "img":
		return imageMD(n, base)
	case "ul":
		return listMD(n, base, false)
	case "ol":
		return listMD(n, base, true)
	case "blockquote":
		inner := strings.TrimSpace(renderBlocks(n, base))
		if inner == "" {
			return ""
		}
		var q strings.Builder
		for _, line := range strings.Split(inner, "\n") {
			if strings.TrimSpace(line) == "" {
				q.WriteString(">\n")
				continue
			}
			q.WriteString("> " + line + "\n")
		}
		q.WriteString("\n")
		return q.String()
	case "pre":
		code := strings.TrimSpace(nodeText(n))
		if code == "" {
			return ""
		}
		return "```\n" + code + "\n```\n\n"
	case "br":
		return "\n"
	default:
		if hasBlockChild(n) {
			return renderBlocks(n, base)
		}
		s := strings.TrimSpace(renderInline(n, base))
		if s == "" {
			return ""
		}
		return s + "\n\n"
	}
}

func listMD(n *html.Node, base string, ordered bool) string {
	var b strings.Builder
	i := 1
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "li" {
			continue
		}
		item := strings.TrimSpace(renderInline(c, base))
		if item == "" {
			item = strings.TrimSpace(collapseBlank(renderBlocks(c, base)))
		}
		if item == "" {
			continue
		}
		item = strings.ReplaceAll(item, "\n", " ")
		if ordered {
			b.WriteString(itoa(i) + ". " + item + "\n")
			i++
		} else {
			b.WriteString("- " + item + "\n")
		}
	}
	if b.Len() == 0 {
		return ""
	}
	b.WriteByte('\n')
	return b.String()
}

func renderInline(n *html.Node, base string) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(inlineNode(c, base))
	}
	return b.String()
}

func inlineNode(n *html.Node, base string) string {
	if n.Type == html.TextNode {
		return collapseInline(n.Data)
	}
	if n.Type != html.ElementNode || skipNode(n.Data) {
		return ""
	}
	switch n.Data {
	case "br":
		return "\n"
	case "strong", "b":
		s := strings.TrimSpace(renderInline(n, base))
		if s == "" {
			return ""
		}
		return "**" + s + "**"
	case "em", "i":
		s := strings.TrimSpace(renderInline(n, base))
		if s == "" {
			return ""
		}
		return "*" + s + "*"
	case "code":
		s := strings.TrimSpace(nodeText(n))
		if s == "" {
			return ""
		}
		return "`" + strings.ReplaceAll(s, "`", "") + "`"
	case "a":
		s := strings.TrimSpace(renderInline(n, base))
		href := resolveRef(base, attr(n, "href"))
		if s == "" {
			return ""
		}
		if href == "" {
			return s
		}
		return "[" + strings.ReplaceAll(s, "]", "") + "](" + href + ")"
	case "img":
		return strings.TrimSpace(imageMD(n, base))
	default:
		return renderInline(n, base)
	}
}

func imageMD(n *html.Node, base string) string {
	src := firstNonEmptyAttr(n, "data-src", "data-original", "data-actualsrc", "src")
	abs := resolveRef(base, src)
	if abs == "" {
		return ""
	}
	alt := strings.TrimSpace(attr(n, "alt"))
	alt = strings.ReplaceAll(alt, "]", "")
	alt = strings.ReplaceAll(alt, "\n", " ")
	return "![" + alt + "](" + abs + ")\n\n"
}

func hasBlockChild(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && isBlock(c.Data) {
			return true
		}
	}
	return false
}

func isBlock(tag string) bool {
	switch tag {
	case "p", "div", "section", "article", "header", "footer", "figure", "blockquote", "pre", "table", "ul", "ol", "h1", "h2", "h3", "h4", "h5", "h6", "img", "hr":
		return true
	default:
		return false
	}
}

func skipNode(tag string) bool {
	switch tag {
	case "script", "style", "noscript", "template", "svg", "iframe":
		return true
	default:
		return false
	}
}

func resolveRef(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(strings.ToLower(ref), "data:") || strings.HasPrefix(strings.ToLower(ref), "javascript:") {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if baseU, err := url.Parse(base); err == nil {
		u = baseU.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func firstNonEmptyAttr(n *html.Node, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(attr(n, k)); v != "" {
			return v
		}
	}
	return ""
}

func collapseInline(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || unicode.IsSpace(r) {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

func collapseBlank(s string) string {
	lines := strings.Split(s, "\n")
	var b strings.Builder
	blank := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blank++
			if blank <= 1 && b.Len() > 0 {
				b.WriteByte('\n')
			}
			continue
		}
		blank = 0
		b.WriteString(strings.TrimRight(line, " \t"))
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d [16]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}
