package webfetch

import (
	"strings"
	"testing"
)

func TestHTMLMarkdownStructureAndImages(t *testing.T) {
	body := []byte(`<!doctype html><html><head>
<title></title><meta property="og:title" content="架构详解">
</head><body>
<div>推荐阅读</div>
<div id="js_content">
<h2>第一节</h2>
<p>正文有<strong>重点</strong></p>
<ul><li>甲</li><li>乙</li></ul>
<img data-src="//cdn.example.com/a.png" alt="图一">
</div>
</body></html>`)
	title, md := HTMLMarkdown("https://mp.weixin.qq.com/s/abc", body)
	if title != "架构详解" {
		t.Fatalf("title=%q", title)
	}
	if strings.Contains(md, "推荐阅读") {
		t.Fatalf("chrome leaked: %s", md)
	}
	for _, want := range []string{"## 第一节", "**重点**", "- 甲", "- 乙", "![图一](https://cdn.example.com/a.png)"} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in %s", want, md)
		}
	}
}

func TestResolveRefDropsDataURL(t *testing.T) {
	if resolveRef("https://example.com/a", "data:image/png;base64,aaaa") != "" {
		t.Fatal("data url must be dropped")
	}
	if got := resolveRef("https://example.com/a/b", "/img/x.png"); got != "https://example.com/img/x.png" {
		t.Fatalf("got %s", got)
	}
}
