package search

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"doc-share/internal/config"
	"doc-share/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ---- ES 认证：API Key 优先于 Basic，两者皆空则不带认证 ----

func TestSetAuthPrecedence(t *testing.T) {
	newReq := func() *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "http://es:8080/idx", nil)
		return r
	}

	// 仅 API Key
	c1 := newESClient(config.SearchConfig{ESIndex: "idx", ESApiKey: "k123"})
	r1 := newReq()
	c1.setAuth(r1)
	if got := r1.Header.Get("Authorization"); got != "ApiKey k123" {
		t.Fatalf("应注入 ApiKey 头，得到 %q", got)
	}

	// API Key 与 Basic 同时配置：API Key 优先
	c2 := newESClient(config.SearchConfig{ESIndex: "idx", ESApiKey: "k", ESUsername: "u", ESPassword: "p"})
	r2 := newReq()
	c2.setAuth(r2)
	if got := r2.Header.Get("Authorization"); got != "ApiKey k" {
		t.Fatalf("API Key 应优先于 Basic，得到 %q", got)
	}

	// 仅 Basic
	c3 := newESClient(config.SearchConfig{ESIndex: "idx", ESUsername: "u", ESPassword: "p"})
	r3 := newReq()
	c3.setAuth(r3)
	if u, p, ok := r3.BasicAuth(); !ok || u != "u" || p != "p" {
		t.Fatalf("应注入 Basic 认证，得到 ok=%v u=%q p=%q", ok, u, p)
	}

	// 两者皆空：不带认证
	c4 := newESClient(config.SearchConfig{ESIndex: "idx"})
	r4 := newReq()
	c4.setAuth(r4)
	if got := r4.Header.Get("Authorization"); got != "" {
		t.Fatalf("未配置凭据不应带 Authorization，得到 %q", got)
	}
}

// ---- ES 索引 mapping：IK 分词器注入与回退 ----

func TestBuildMappingAnalyzer(t *testing.T) {
	propsOf := func(m map[string]any) map[string]any {
		return m["mappings"].(map[string]any)["properties"].(map[string]any)
	}

	// 装了 IK：title/content 用 ik_max_word 建索引、ik_smart 搜索；slug 仍为 keyword
	ik := propsOf(buildMapping(true))
	for _, f := range []string{"title", "content"} {
		field := ik[f].(map[string]any)
		if field["analyzer"] != "ik_max_word" || field["search_analyzer"] != "ik_smart" {
			t.Fatalf("%s 应使用 IK 分词器，得到 %v", f, field)
		}
	}
	if ik["slug"].(map[string]any)["type"] != "keyword" {
		t.Fatal("slug 应始终为 keyword")
	}

	// 未装 IK：回退标准分析器，不写 analyzer（用 ES 默认）
	std := propsOf(buildMapping(false))
	for _, f := range []string{"title", "content"} {
		field := std[f].(map[string]any)
		if field["type"] != "text" {
			t.Fatalf("%s 应为 text", f)
		}
		if _, ok := field["analyzer"]; ok {
			t.Fatalf("%s 未装 IK 时不应指定 analyzer，得到 %v", f, field)
		}
	}
}

// ---- ES 查询构造：权限过滤 / matchNone / html 正文排除 ----

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestBuildQueryPermissionScope(t *testing.T) {
	c := newESClient(config.SearchConfig{ESIndex: "idx"})

	t.Run("non_admin_default_owner_or_member", func(t *testing.T) {
		q, none := c.buildQuery(Params{UserID: 7, MemberPids: []uint{5, 6}, ProjectID: -1, CategoryID: -1, Shared: -1})
		if none {
			t.Fatal("不应为空集")
		}
		s := mustJSON(t, q)
		if !strings.Contains(s, `"owner_id":7`) || !strings.Contains(s, `"project_id":[5,6]`) {
			t.Fatalf("应含 owner 与成员项目过滤: %s", s)
		}
		if !strings.Contains(s, `"deleted":false`) {
			t.Fatalf("非回收站应过滤 deleted=false: %s", s)
		}
	})

	t.Run("shared_without_members_is_match_none", func(t *testing.T) {
		_, none := c.buildQuery(Params{UserID: 7, Origin: "shared", ProjectID: -1, CategoryID: -1, Shared: -1})
		if !none {
			t.Fatal("shared 且未参与任何项目必须为空集（matchNone）")
		}
	})

	t.Run("admin_no_owner_filter", func(t *testing.T) {
		q, _ := c.buildQuery(Params{IsAdmin: true, ProjectID: -1, CategoryID: -1, Shared: -1})
		s := mustJSON(t, q)
		if strings.Contains(s, "owner_id") {
			t.Fatalf("admin 全部视角不应有 owner 过滤: %s", s)
		}
	})

	t.Run("trash_and_filters", func(t *testing.T) {
		q, _ := c.buildQuery(Params{IsAdmin: true, Trash: true, ProjectID: 3, CategoryID: 0, Shared: 1})
		s := mustJSON(t, q)
		for _, want := range []string{`"deleted":true`, `"project_id":3`, `"category_id":0`, `"is_shared":true`} {
			if !strings.Contains(s, want) {
				t.Fatalf("缺少过滤 %s: %s", want, s)
			}
		}
	})
}

func TestBuildQueryTextExcludesHTMLContent(t *testing.T) {
	c := newESClient(config.SearchConfig{ESIndex: "idx"})
	q, _ := c.buildQuery(Params{Query: "紫电", IsAdmin: true, ProjectID: -1, CategoryID: -1, Shared: -1})
	s := mustJSON(t, q)
	if !strings.Contains(s, `"match"`) || !strings.Contains(s, "紫电") {
		t.Fatalf("应含关键词全文匹配: %s", s)
	}
	// title/content 均应用 operator:and（所有词都命中），对齐 MySQL 子串口径
	if strings.Count(s, `"operator":"and"`) != 2 {
		t.Fatalf("title 与 content 均应使用 operator:and，得到: %s", s)
	}
	// content 匹配必须与「排除 html 类型」绑定，避免命中整站 manifest
	if !strings.Contains(s, `"must_not"`) || !strings.Contains(s, model.DocTypeHTML) {
		t.Fatalf("正文匹配应排除 html 类型: %s", s)
	}
	if !strings.Contains(s, `"wildcard"`) || !strings.Contains(s, "*紫电*") {
		t.Fatalf("slug 应做子串通配匹配: %s", s)
	}
}

// slug wildcard 必须转义用户输入的通配符元字符，避免 "*" 被当作真通配符命中全部。
func TestBuildQueryEscapesSlugWildcard(t *testing.T) {
	c := newESClient(config.SearchConfig{ESIndex: "idx"})
	q, _ := c.buildQuery(Params{Query: `a*b?c`, IsAdmin: true, ProjectID: -1, CategoryID: -1, Shared: -1})
	s := mustJSON(t, q)
	// 转义后内存中为 *a\*b\?c*；JSON 序列化会把反斜杠再转义，故文本中为双反斜杠
	if !strings.Contains(s, `*a\\*b\\?c*`) {
		t.Fatalf("slug 通配符应转义用户输入的 * 与 ?，得到: %s", s)
	}
}

// ---- MySQL 降级后端：权限范围 / html 正文排除 / 回收站 ----

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Category{}, &model.Document{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestMySQLFallbackScopeAndTitle(t *testing.T) {
	db := newTestDB(t)
	svc := New(config.SearchConfig{}, db) // ESURL 空 → 仅 MySQL
	if svc.ESEnabled() {
		t.Fatal("未配置 ESURL 不应启用 ES")
	}
	now := time.Now()
	seed := []model.Document{
		{Title: "标题含紫电甲", Slug: "s1", Type: model.DocTypeMarkdown, Content: "正文无此词", OwnerID: 1, UpdatedAt: now.Add(-1 * time.Hour)},
		{Title: "整站丙", Slug: "s2", Type: model.DocTypeHTML, Content: `{"files":["紫电.html"]}`, OwnerID: 1, UpdatedAt: now.Add(-2 * time.Hour)},
		{Title: "私有紫电丁", Slug: "s3", Type: model.DocTypeMarkdown, Content: "别人的", OwnerID: 2, UpdatedAt: now.Add(-3 * time.Hour)},
		{Title: "项目紫电戊", Slug: "s4", Type: model.DocTypeMarkdown, Content: "项目正文", OwnerID: 9, ProjectID: 5, UpdatedAt: now.Add(-4 * time.Hour)},
		{Title: "仅正文有词", Slug: "s5", Type: model.DocTypeMarkdown, Content: "包含关键词紫电", OwnerID: 1, UpdatedAt: now.Add(-5 * time.Hour)},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// MySQL 路径只搜 title/slug：用户 1 参与项目 5，应命中自有标题甲与项目戊；不得命中私有丁、仅正文有词、manifest 含词的整站
	p := DefaultParams()
	p.Query = "紫电"
	p.UserID = 1
	p.MemberPids = []uint{5}
	ids, err := svc.Page(p)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	got := map[uint]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got[seed[0].ID] {
		t.Fatal("应通过标题命中 标题含紫电甲")
	}
	if got[seed[1].ID] {
		t.Fatal("html 整站 manifest 不应参与 MySQL 搜索")
	}
	if !got[seed[3].ID] {
		t.Fatal("项目紫电戊 应通过标题命中")
	}
	if got[seed[2].ID] {
		t.Fatal("私有丁 属于他人且未挂参与项目，不得命中")
	}
	if got[seed[4].ID] {
		t.Fatal("MySQL 路径不应再按 content LIKE 命中")
	}

	// 标题匹配的 html 整站仍可用标题搜到
	p3 := DefaultParams()
	p3.Query = "整站丙"
	p3.UserID = 1
	p3.MemberPids = []uint{5}
	ids3, _ := svc.Page(p3)
	if len(ids3) != 1 || ids3[0] != seed[1].ID {
		t.Fatalf("html 整站应通过标题命中，得到 %v", ids3)
	}

	// ScopeQuery 权限兜底：即便给定越权 id，回表也应剔除不可见文档
	var docs []model.Document
	svc.ScopeQuery(p).Where("id IN ?", []uint{seed[0].ID, seed[2].ID}).Find(&docs)
	for _, d := range docs {
		if d.ID == seed[2].ID {
			t.Fatal("ScopeQuery 兜底应剔除无权访问的 私有丁")
		}
	}
}

func TestMySQLTrashScope(t *testing.T) {
	db := newTestDB(t)
	svc := New(config.SearchConfig{}, db)
	d := model.Document{Title: "已删", Slug: "d1", Type: model.DocTypeMarkdown, Content: "x", OwnerID: 1}
	if err := db.Create(&d).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Delete(&model.Document{}, d.ID).Error; err != nil { // 软删
		t.Fatalf("soft delete: %v", err)
	}
	// 普通视图搜不到软删文档
	p := DefaultParams()
	p.UserID = 1
	if ids, _ := svc.Page(p); len(ids) != 0 {
		t.Fatalf("普通视图不应含软删文档，得到 %v", ids)
	}
	// 回收站视图能搜到
	pt := DefaultParams()
	pt.UserID = 1
	pt.Trash = true
	ids, _ := svc.Page(pt)
	if len(ids) != 1 || ids[0] != d.ID {
		t.Fatalf("回收站视图应含软删文档，得到 %v", ids)
	}
}

// ---- ES 优先路径：报错降级 MySQL / 回表剔除越权 ID 并保持 ES 顺序 ----

// fakeES 启一个伪造 ES：HEAD 返 200（索引已存在，跳过建索引/IK 探测），
// 其余请求（POST _search）交给 search 处理。
func fakeES(t *testing.T, search func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		search(w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newReadyService(t *testing.T, db *gorm.DB, esURL string) *Service {
	t.Helper()
	svc := New(config.SearchConfig{ESURL: esURL, ESIndex: "idx", ESTimeoutMS: 2000}, db)
	if !svc.ESEnabled() {
		t.Fatal("配置 ESURL 应启用 ES")
	}
	svc.setReady(true) // 假装全量已就绪，强制走 ES 分支
	return svc
}

// ES 搜索报错（非连接层）时必须降级 MySQL，而不是报错/返回空。
func TestESSearchErrorFallsBackToMySQL(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	d := model.Document{Title: "降级紫电甲", Slug: "fb1", Type: model.DocTypeMarkdown, Content: "正文无词", OwnerID: 1, UpdatedAt: now}
	if err := db.Create(&d).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := fakeES(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError) // _search 报错
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	svc := newReadyService(t, db, srv.URL)

	p := DefaultParams()
	p.Query = "紫电"
	p.UserID = 1
	ids, err := svc.Page(p)
	if err != nil {
		t.Fatalf("ES 报错时 Page 应降级成功而非报错: %v", err)
	}
	if len(ids) != 1 || ids[0] != d.ID {
		t.Fatalf("ES 报错应降级 MySQL 命中，得到 %v", ids)
	}
	total, err := svc.Count(p)
	if err != nil || total != 1 {
		t.Fatalf("Count 应降级 MySQL 得到 1，得到 total=%d err=%v", total, err)
	}
}

// ES 返回乱序且含越权 ID 时：Page 原样返回 ES 顺序，调用方回表（ScopeQuery + id IN）
// 应剔除越权文档、仅保留有权的，且重排后顺序跟随 ES。
func TestESHydrationExcludesUnauthorizedAndPreservesOrder(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	mine := model.Document{Title: "我的", Slug: "m1", Type: model.DocTypeMarkdown, Content: "紫电", OwnerID: 1, UpdatedAt: now}
	theirs := model.Document{Title: "他人", Slug: "t1", Type: model.DocTypeMarkdown, Content: "紫电", OwnerID: 2, UpdatedAt: now}
	for _, d := range []*model.Document{&mine, &theirs} {
		if err := db.Create(d).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// ES 命中顺序：[theirs, mine]（theirs 不属于 user 1，模拟 ES 权限副本过期）
	srv := fakeES(t, func(w http.ResponseWriter) {
		resp := map[string]any{"hits": map[string]any{
			"total": map[string]any{"value": 2},
			"hits": []map[string]any{
				{"_id": strconv.FormatUint(uint64(theirs.ID), 10)},
				{"_id": strconv.FormatUint(uint64(mine.ID), 10)},
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	svc := newReadyService(t, db, srv.URL)

	p := DefaultParams()
	p.Query = "紫电"
	p.UserID = 1
	ids, err := svc.Page(p)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(ids) != 2 || ids[0] != theirs.ID || ids[1] != mine.ID {
		t.Fatalf("Page 应按 ES 顺序原样返回 ids（含越权），得到 %v", ids)
	}

	// 模拟调用方回表兜底 + 按 ES 顺序重排（与 handler 一致）
	var docs []model.Document
	svc.ScopeQuery(p).Where("id IN ?", ids).Find(&docs)
	byID := make(map[uint]model.Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}
	ordered := make([]uint, 0, len(ids))
	for _, id := range ids {
		if _, ok := byID[id]; ok {
			ordered = append(ordered, id)
		}
	}
	if len(ordered) != 1 || ordered[0] != mine.ID {
		t.Fatalf("回表应剔除越权 theirs、仅保留 mine，得到 %v", ordered)
	}
}

// 并发冒烟：多 goroutine 同时走 ES 优先路径，验证 ensureIndex/indexReady 的锁与原子
// 不会死锁或 panic（共享单例 ESClient 的并发安全回归护栏）。
func TestConcurrentPageNoDeadlock(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	d := model.Document{Title: "并发", Slug: "c1", Type: model.DocTypeMarkdown, Content: "紫电", OwnerID: 1, UpdatedAt: now}
	if err := db.Create(&d).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := fakeES(t, func(w http.ResponseWriter) {
		resp := map[string]any{"hits": map[string]any{
			"total": map[string]any{"value": 1},
			"hits":  []map[string]any{{"_id": strconv.FormatUint(uint64(d.ID), 10)}},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	svc := newReadyService(t, db, srv.URL)

	const n = 16
	errCh := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errCh <- fmt.Errorf("panic: %v", r)
				}
			}()
			p := DefaultParams()
			p.Query = "紫电"
			p.UserID = 1
			if _, err := svc.Page(p); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发 Page 出错: %v", err)
	}
}

// TestRenderHighlightEscapesAndMarks 高亮片段必须先整体转义（正文为用户内容，防 XSS），
// 再把控制字符占位标记替换为 <mark>。
func TestRenderHighlightEscapesAndMarks(t *testing.T) {
	frag := hlPre + "紫电" + hlPost + " <script>alert(1)</script>"
	got := RenderHighlight(frag)
	if !strings.Contains(got, "<mark>紫电</mark>") {
		t.Fatalf("占位标记应替换为 <mark>: %s", got)
	}
	if strings.Contains(got, "<script>") {
		t.Fatalf("正文必须转义防 XSS: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("应保留转义后的文本: %s", got)
	}
}

// TestSearchHighlightOnlyWhenRequested 高亮开销必须隔离：Search（AI/MCP 入口）不请求 highlight，
// 仅 SearchWithHighlight（Web 搜索页）请求且只针对 content（title 整字段高亮为死负载）。
func TestSearchHighlightOnlyWhenRequested(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":{"total":{"value":1},"hits":[{"_id":"7","highlight":{"content":["x\u0000探子\u0001y"]}}]}}`)
	}))
	t.Cleanup(srv.Close)
	c := newESClient(config.SearchConfig{ESURL: srv.URL, ESIndex: "idx", ESTimeoutMS: 2000})
	p := Params{Query: "探子", IsAdmin: true, ProjectID: -1, CategoryID: -1, Shared: -1, Offset: 0, Size: 10}

	if _, err := c.Search(p); err != nil {
		t.Fatalf("Search: %v", err)
	}
	ids, hl, err := c.SearchWithHighlight(p)
	if err != nil {
		t.Fatalf("SearchWithHighlight: %v", err)
	}
	if len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("应解析命中 ID: %v", ids)
	}
	if !strings.Contains(hl[7], "\x00探子\x01") {
		t.Fatalf("应保留占位标记片段: %q", hl[7])
	}
	if len(bodies) != 2 {
		t.Fatalf("应恰好两次 _search: %d", len(bodies))
	}
	if strings.Contains(bodies[0], `"highlight"`) {
		t.Fatalf("Search（AI/MCP）不应请求高亮: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], `"highlight"`) || !strings.Contains(bodies[1], `"content"`) {
		t.Fatalf("SearchWithHighlight 应请求 content 高亮: %s", bodies[1])
	}
	if strings.Contains(bodies[1], `"number_of_fragments":0`) {
		t.Fatalf("不应再请求 title 整字段高亮（死负载）: %s", bodies[1])
	}
}

// TestMySQLFallbackSnippet 标题命中后，降级路径仍可为正文中的同一词切摘要（展示用）；
// 仅 content 含词、标题/slug 不含时不会进结果集。html manifest 不参与匹配。
func TestMySQLFallbackSnippet(t *testing.T) {
	db := newTestDB(t)
	svc := New(config.SearchConfig{}, db)
	now := time.Now()
	seed := []model.Document{
		{Title: "探子报告", Slug: "s1", Type: model.DocTypeMarkdown, Content: strings.Repeat("铺垫内容，", 20) + "这里出现探子关键词" + strings.Repeat("，收尾内容", 20), OwnerID: 1, UpdatedAt: now},
		{Title: "整站乙", Slug: "s2", Type: model.DocTypeHTML, Content: `{"files":["探子.html"]}`, OwnerID: 1, UpdatedAt: now.Add(-time.Hour)},
		{Title: "仅正文", Slug: "s3", Type: model.DocTypeMarkdown, Content: "这里出现探子关键词", OwnerID: 1, UpdatedAt: now.Add(-2 * time.Hour)},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	p := DefaultParams()
	p.Query = "探子"
	p.UserID = 1
	ids, hl, err := svc.PageWithHighlight(p)
	if err != nil {
		t.Fatalf("PageWithHighlight: %v", err)
	}
	if len(ids) != 1 || ids[0] != seed[0].ID {
		t.Fatalf("应仅标题命中 markdown，得到 %v", ids)
	}
	frag, ok := hl[seed[0].ID]
	if !ok {
		t.Fatal("降级路径应为命中文档提供正文摘要")
	}
	if !strings.Contains(frag, hlPre+"探子"+hlPost) {
		t.Fatalf("摘要应包占位标记: %q", frag)
	}
	if !strings.HasPrefix(frag, "…") || !strings.HasSuffix(frag, "…") {
		t.Fatalf("非整篇窗口两端应有省略号: %q", frag)
	}
	if got := RenderHighlight(frag); !strings.Contains(got, "<mark>探子</mark>") {
		t.Fatalf("RenderHighlight 应标黄: %s", got)
	}
}
