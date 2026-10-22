package handler

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"doc-share/internal/model"
)

// TestDocsTitleSlugSearch MySQL 降级路径仅按 title/slug 匹配（不再 content LIKE）；
// 权限过滤不因搜索放宽。正文全文检索依赖 ES。
func TestDocsTitleSlugSearch(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}

	seed := []model.Document{
		{Title: "标题含紫电甲", Content: "正文无此词", OwnerID: users["owner"].ID},
		{Title: "普通标题乙", Content: "紫电只在正文", OwnerID: users["owner"].ID},
		{Title: "整站丙", Type: model.DocTypeHTML, Content: `{"prefix":"html/9/紫电/","entry":"index.html","files":["index.html"]}`, OwnerID: users["owner"].ID},
		{Title: "私有紫电丁", Content: "别人的", OwnerID: users["outsider"].ID},
		{Title: "项目紫电戊", Content: "项目正文", OwnerID: users["owner"].ID, ProjectID: doc.ProjectID},
	}
	for i := range seed {
		seed[i].Slug = fmt.Sprintf("sea1%04d", i+1)
		if err := a.DB.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed doc %d: %v", i, err)
		}
	}
	preview := func(d model.Document) string {
		return fmt.Sprintf("/console/docs/%d/preview", d.ID)
	}

	render := newRenderApp(t, a.DB)
	doSearch := func(u *model.User, q, extra string) string {
		w, c := newCtx()
		path := "/console/docs?q=" + url.QueryEscape(q)
		if extra != "" {
			path += "&" + extra
		}
		asUser(c, u, http.MethodGet, path, "")
		render.DocsPage(c)
		assertCode(t, w, http.StatusOK)
		return w.Body.String()
	}

	t.Run("title_hit_content_not_searched", func(t *testing.T) {
		body := doSearch(users["owner"], "紫电", "")
		if !strings.Contains(body, preview(seed[0])) {
			t.Fatalf("应通过标题命中 标题含紫电甲")
		}
		if strings.Contains(body, preview(seed[1])) {
			t.Fatalf("MySQL 路径不应再按正文 content LIKE 命中")
		}
	})

	t.Run("permission_not_relaxed_by_search", func(t *testing.T) {
		body := doSearch(users["viewm"], "紫电", "")
		for _, banned := range []model.Document{seed[0], seed[3]} {
			if strings.Contains(body, preview(banned)) {
				t.Fatalf("非项目成员不应通过搜索看到 %s", banned.Title)
			}
		}
	})

	t.Run("shared_origin_hits_project_doc_title", func(t *testing.T) {
		body := doSearch(users["viewm"], "紫电", "origin=shared")
		if !strings.Contains(body, preview(seed[4])) {
			t.Fatalf("shared 筛选下应通过标题搜索看到所参与项目内的他人文档")
		}
		for _, banned := range []model.Document{seed[0], seed[3]} {
			if strings.Contains(body, preview(banned)) {
				t.Fatalf("shared 筛选不得放宽到未挂项目文档 %s", banned.Title)
			}
		}
	})

	t.Run("admin_sees_everyone_by_title", func(t *testing.T) {
		body := doSearch(users["boss"], "紫电", "")
		for _, want := range []model.Document{seed[0], seed[4], seed[3]} {
			if !strings.Contains(body, preview(want)) {
				t.Fatalf("admin 标题搜索应命中 %s", want.Title)
			}
		}
		if strings.Contains(body, preview(seed[1])) {
			t.Fatalf("admin 也不应通过正文 LIKE 命中")
		}
	})
}

// TestHighlightTitleEscapes 标题标黄必须先转义（防 XSS）再包 <mark>；无命中词时原样转义
func TestHighlightTitleEscapes(t *testing.T) {
	hl := titleHighlighter("互助")
	if hl == nil {
		t.Fatal("有查询词时应返回高亮闭包")
	}
	got := string(hl("A <b>互助</b> 平台"))
	if !strings.Contains(got, "<mark>互助</mark>") {
		t.Fatalf("应标黄命中词: %s", got)
	}
	if strings.Contains(got, "<b>") {
		t.Fatalf("标题原有 HTML 必须被转义: %s", got)
	}
	if !strings.Contains(got, "&lt;b&gt;") {
		t.Fatalf("应保留转义文本: %s", got)
	}
	if titleHighlighter("") != nil || titleHighlighter("   ") != nil {
		t.Fatalf("无查询词时不应标黄")
	}
	// 查询词落在转义实体内部时不得切开实体（否则标题被改写）
	if got := string(titleHighlighter("amp")("Tom & Jerry")); got != "Tom &amp; Jerry" {
		t.Fatalf("不应切中 &amp; 实体内部: %s", got)
	}
}
