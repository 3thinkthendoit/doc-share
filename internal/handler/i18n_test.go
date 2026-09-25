package handler

import (
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"doc-share/internal/i18n"
	"doc-share/internal/model"
)

// TestParseTemplates 保证页面模板本身可解析（多语言改造后模板里大量使用 {{.T "..."}}，
// 语法错误只有在渲染时才会暴露，这里提前拦住）
func TestParseTemplates(t *testing.T) {
	tmpl, err := ParseTemplates(os.DirFS("../../web/templates"))
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}
	for _, name := range []string{"head", "nav", "langSwitch", "pager"} {
		if tmpl.Lookup(name) == nil {
			t.Errorf("公共片段 %s 未定义", name)
		}
	}
}

func newBundle(t *testing.T) *i18n.Bundle {
	t.Helper()
	b, err := i18n.Load(os.DirFS("../../web/locales"))
	if err != nil {
		t.Fatalf("i18n.Load: %v", err)
	}
	return b
}

// TestPagesRender 用真实数据渲染每个页面：模板里的 {{.T}} 必须能带参调用，
// 且英文页面不得残留中文（语言切换器里的语言自称除外，它们故意不翻译）
func TestPagesRender(t *testing.T) {
	bundle := newBundle(t)
	tmpl, err := ParseTemplates(os.DirFS("../../web/templates"))
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}
	a := &App{I18N: bundle, Tmpl: tmpl}
	now := time.Now()
	user := &model.User{ID: 1, Username: "alice", Nickname: "Alice", Role: model.RoleAdmin, Status: 1}
	owner := model.User{ID: 1, Username: "alice", Nickname: "Alice"}
	doc := model.Document{ID: 1, Title: "Design Doc", Slug: "design-doc", Owner: owner, ViewCount: 3, UpdatedAt: now,
		Share: &model.Share{ShareToken: "tok123"}}
	pj := model.Project{ID: 1, Name: "Platform", Owner: owner, UpdatedAt: now}
	ct := model.Category{ID: 1, Name: "Requirements", Owner: owner}
	key := model.ApiKey{ID: 1, Name: "sync", AppKey: "ak_1", Owner: owner, Status: 1, CreatedAt: now}

	base := func() View {
		return View{"user": user, "Lang": "en-US", "Langs": i18n.Supported, "Path": "/admin", "Dict": "", viewBundleKey: bundle}
	}
	withPager := func(v View, path string) View {
		v["page"], v["size"], v["total"], v["totalPages"] = 1, 15, int64(1), 1
		v["pageSizes"], v["pagerPath"], v["pagerExtra"] = []int{15, 30, 50}, path, ""
		return v
	}
	cases := []struct {
		name string
		data func() View
	}{
		{"login.html", func() View { v := base(); v["title"] = "登录"; return v }},
		{"home.html", func() View { v := base(); v["user"] = nil; v["Path"] = "/"; return v }},
		{"register.html", func() View { v := base(); v["title"] = "注册"; v["username"] = "bob"; return v }},
		{"dashboard.html", func() View {
			v := base()
			v["isAdmin"], v["docCount"], v["userCount"], v["shareCount"], v["recent"] = true, int64(1), int64(1), int64(1), []model.Document{doc}
			return v
		}},
		{"docs.html", func() View {
			v := withPager(base(), "/admin/docs")
			v["docs"], v["q"] = []model.Document{doc}, ""
			v["project"], v["category"], v["projects"], v["categories"] = "", "", []model.Project{pj}, []model.Category{ct}
			return v
		}},
		{"doc_edit.html", func() View {
			v := base()
			v["doc"], v["share"], v["shareURL"], v["projects"], v["categories"] = &doc, doc.Share, "/s/tok123", []model.Project{pj}, []model.Category{ct}
			return v
		}},
		{"projects.html", func() View {
			v := withPager(base(), "/admin/projects")
			v["projects"], v["q"], v["owner"], v["scope"] = []model.Project{pj}, "", "", ""
			return v
		}},
		{"categories.html", func() View {
			v := withPager(base(), "/admin/categories")
			v["categories"] = []model.Category{ct}
			return v
		}},
		{"apikeys.html", func() View {
			v := withPager(base(), "/admin/apikeys")
			v["keys"], v["owner"] = []model.ApiKey{key}, ""
			return v
		}},
		{"users.html", func() View {
			v := withPager(base(), "/admin/users")
			v["users"] = []model.User{*user}
			return v
		}},
		{"share_view.html", func() View { v := base(); v["user"] = nil; v["doc"] = &doc; return v }},
		{"share_password.html", func() View {
			v := base()
			v["user"] = nil
			v["token"] = "tok123"
			v["owner"] = "A*ice"
			v["error"] = "share.pwdWrong"
			v["applyStatus"] = ""
			v["applyName"] = ""
			return v
		}},
	}

	for _, lang := range []string{"zh-CN", "zh-TW", "ja-JP", "en-US", "fr-FR"} {
		han := regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}]`)
		// 语言下拉里的语言自称故意用本族语书写（简体中文/日本語…），不计入残留
		switcher := regexp.MustCompile(`(?s)<form class="(lang-switch|l-lang)".*?</form>`)
		// 脚本不参与检查：内联 JS 的中文字面量由 UI.t 按注入的译文表在运行时翻译
		scripts := regexp.MustCompile(`(?s)<script.*?</script>`)
		keys := dictKeys(t)
		for _, c := range cases {
			var buf strings.Builder
			data := c.data()
			data["Lang"] = lang
			data["title"] = a.i18nText(lang, "文档管理")
			if s, ok := data["error"].(string); ok {
				data["error"] = a.i18nText(lang, s)
			}
			data["Dict"] = template.JS(a.dictJSON(lang))
			if err := tmpl.ExecuteTemplate(&buf, c.name, data); err != nil {
				t.Errorf("%s/%s 渲染失败：%v", lang, c.name, err)
				continue
			}
			out := scripts.ReplaceAllString(switcher.ReplaceAllString(buf.String(), ""), "")
			// 未解析的 i18n key 会直接当作文本输出（如 nav.dashboard），任何语言下都是 bug
			for _, k := range keys {
				if strings.Contains(out, k) {
					t.Errorf("%s/%s 输出了未解析的 i18n key：%s", lang, c.name, k)
					break
				}
			}
			if lang == "en-US" || lang == "fr-FR" {
				// 只要求欧语系页面无 CJK；ja-JP 正常使用汉字，不能规入残留
				if loc := han.FindStringIndex(out); loc != nil {
					lo := max(0, loc[0]-30)
					t.Errorf("%s/%s 页面残留中文：%q", lang, c.name, truncate(out[lo:min(len(out), loc[1]+30)]))
				}
			}
		}
	}
}

// TestTemplateStringsResolve 扫描所有模板里的 {{$.T "中文"}} 文案，逐条确认已收录进词典，
// 且欧语系译文不残留汉字。渲染测试只会报出第一个残留点，靠它一条一条试太慢，这里一次性列出全部漏网词条。
// 注意：不能拿「TZ 返回值 == 原文」当未收录，zh-TW/ja-JP 里“操作”“取消”“保存”这类词本来就与原文同形。
func TestTemplateStringsResolve(t *testing.T) {
	b := newBundle(t)
	literal := regexp.MustCompile(`\{\{\$\.TH?\s+"([^"]+)"`)
	han := regexp.MustCompile(`\p{Han}`)
	files, err := filepath.Glob("../../web/templates/*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("扫描模板目录失败：files=%d err=%v", len(files), err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("读取 %s 失败：%v", f, err)
			continue
		}
		seen := map[string]bool{}
		for _, m := range literal.FindAllStringSubmatch(string(raw), -1) {
			s := m[1]
			if !han.MatchString(s) || seen[s] {
				continue // 写成键名的形式由 TestLocaleParity 保证各语言键集一致
			}
			seen[s] = true
			if !b.HasSource(s) {
				t.Errorf("%s 的文案 %q 未收录进 zh-CN 词典，非中文页面会原样输出中文", filepath.Base(f), truncate(s))
				continue
			}
			for _, lang := range []string{"en-US", "fr-FR"} {
				if got := b.TZ(lang, s); han.MatchString(got) {
					t.Errorf("%s 的文案 %q 在 %s 译文仍含中文：%q", filepath.Base(f), truncate(s), lang, truncate(got))
				}
			}
		}
	}
}

// dictKeys 读出词典里的全部 i18n 键，用于检测「键名泄漏到页面文本」
func dictKeys(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../web/locales/zh-CN.json")
	if err != nil {
		t.Fatalf("read dict: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse dict: %v", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func truncate(s string) string {
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}
