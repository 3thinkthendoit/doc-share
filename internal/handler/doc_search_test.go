package handler

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"doc-share/internal/model"
)

// TestDocsContentSearch 正文搜索：命中 markdown 正文与思维导图节点文字，
// 不搜 html 整站的 manifest，且搜索不放宽既有的权限过滤
func TestDocsContentSearch(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}

	// 关键词只出现在正文/manifest 中、不出现在标题里，确保命中仅来自 content LIKE
	seed := []model.Document{
		{Title: "正文甲", Slug: "sea1aaaa", Content: "# 概述\n包含关键词紫电的正文", OwnerID: users["owner"].ID},
		{Title: "导图乙", Slug: "sea1bbbb", Type: model.DocTypeMindmap, Content: `{"nodes":[{"text":"紫电藏在节点里"}]}`, OwnerID: users["owner"].ID},
		{Title: "整站丙", Slug: "sea1cccc", Type: model.DocTypeHTML, Content: `{"prefix":"html/9/紫电/","entry":"index.html","files":["index.html"]}`, OwnerID: users["owner"].ID},
		{Title: "私有丁", Slug: "sea1dddd", Content: "紫电在别人的私有文档里", OwnerID: users["outsider"].ID},
		// 挂在 viewm 所参与项目下，供 origin=shared 叠加正文搜索的用例使用
		{Title: "项目戊", Slug: "sea1eeee", Content: "紫电出现在项目文档正文", OwnerID: users["owner"].ID, ProjectID: doc.ProjectID},
	}
	for i := range seed {
		if err := a.DB.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed doc %d: %v", i, err)
		}
	}

	render := newRenderApp(t, a.DB)
	search := func(u *model.User, q, extra string) string {
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

	t.Run("content_hit_and_html_excluded", func(t *testing.T) {
		body := search(users["owner"], "紫电", "")
		for _, want := range []string{"正文甲", "导图乙"} {
			if !strings.Contains(body, want) {
				t.Fatalf("应通过正文命中 %s", want)
			}
		}
		if strings.Contains(body, "整站丙") {
			t.Fatalf("html 整站的 manifest 不应参与正文搜索")
		}
	})

	t.Run("title_search_unaffected", func(t *testing.T) {
		if !strings.Contains(search(users["owner"], "整站丙", ""), "整站丙") {
			t.Fatalf("标题搜索应不受影响仍命中")
		}
	})

	t.Run("permission_not_relaxed_by_search", func(t *testing.T) {
		// viewm 仅参与 owner 的项目；除项目戊外的种子文档均未挂项目，搜正文也不得可见
		body := search(users["viewm"], "紫电", "")
		for _, banned := range []string{"正文甲", "私有丁"} {
			if strings.Contains(body, banned) {
				t.Fatalf("非项目成员不应通过正文搜索看到 %s", banned)
			}
		}
	})

	t.Run("shared_origin_hits_project_doc_content", func(t *testing.T) {
		// 项目戊挂在 viewm 参与的项目下：shared 筛选与正文搜索的 OR 嵌套组合应仍命中
		body := search(users["viewm"], "紫电", "origin=shared")
		if !strings.Contains(body, "项目戊") {
			t.Fatalf("shared 筛选下应通过正文搜索看到所参与项目内的他人文档")
		}
		for _, banned := range []string{"正文甲", "私有丁"} {
			if strings.Contains(body, banned) {
				t.Fatalf("shared 筛选不得放宽到未挂项目文档 %s", banned)
			}
		}
	})

	t.Run("admin_sees_everyone_by_content", func(t *testing.T) {
		body := search(users["boss"], "紫电", "")
		for _, want := range []string{"正文甲", "导图乙", "项目戊", "私有丁"} {
			if !strings.Contains(body, want) {
				t.Fatalf("admin 正文搜索应命中 %s", want)
			}
		}
		if strings.Contains(body, "整站丙") {
			t.Fatalf("html 排除对 admin 视角同样成立")
		}
	})
}
