package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"doc-share/internal/config"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newHTMLPageAIApp(t *testing.T) (*App, *model.User, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:aihtml_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Project{}, &model.Category{}, &model.Document{},
		&model.ProjectMember{}, &model.SystemSetting{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	owner := model.User{Username: "htmlowner", Nickname: "属主", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	dir := t.TempDir()
	app := &App{
		DB: db,
		Cfg: &config.Config{Upload: config.UploadConfig{
			Dir: dir, MaxSizeMB: 5, HTMLMaxSizeMB: 10, HTMLMaxFiles: 100,
		}},
	}
	return app, &owner, dir
}

func samplePageHTML(title string) string {
	return `<!DOCTYPE html><html><head><meta charset="utf-8"><title>` + title +
		`</title><style>body{font-family:sans-serif}</style></head>` +
		`<body><h1>` + title + `</h1><p>内容</p></body></html>`
}

func TestValidateAIHTMLPage(t *testing.T) {
	if err := validateAIHTMLPage(""); err == nil {
		t.Fatal("empty must fail")
	}
	if err := validateAIHTMLPage("<div>fragment</div>"); err == nil {
		t.Fatal("fragment must fail")
	}
	if err := validateAIHTMLPage(samplePageHTML("ok")); err != nil {
		t.Fatal(err)
	}
	big := "<!DOCTYPE html><html><body>" + strings.Repeat("x", aiMaxContentBytes) + "</body></html>"
	if err := validateAIHTMLPage(big); err == nil {
		t.Fatal("oversized must fail")
	}
}

func TestResolveAIHTMLPageArg(t *testing.T) {
	page := samplePageHTML("别名卷")
	got, err := resolveAIHTMLPageArg(map[string]any{"title": "t", "html": page})
	if err != nil || got != page {
		t.Fatalf("html ok: got=%q err=%v", got, err)
	}
	got, err = resolveAIHTMLPageArg(map[string]any{"title": "t", "content": page})
	if err != nil || got != page {
		t.Fatalf("content alias: got=%q err=%v", got, err)
	}
	// html 优先于 content
	got, err = resolveAIHTMLPageArg(map[string]any{"html": page, "content": samplePageHTML("其他")})
	if err != nil || got != page {
		t.Fatalf("html wins: got=%q err=%v", got, err)
	}
	_, err = resolveAIHTMLPageArg(map[string]any{"title": "仅标题"})
	if err == nil || !strings.Contains(err.Error(), "缺少参数 html") {
		t.Fatalf("missing html: %v", err)
	}
	_, err = resolveAIHTMLPageArg(map[string]any{"title": "t", "html": ""})
	if err == nil || !strings.Contains(err.Error(), "空字符串") {
		t.Fatalf("empty html: %v", err)
	}
	_, err = resolveAIHTMLPageArg(map[string]any{"title": "t", "html": nil})
	if err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatalf("null html: %v", err)
	}
	_, err = resolveAIHTMLPageArg(map[string]any{"html": 123})
	if err == nil || !strings.Contains(err.Error(), "必须是字符串") {
		t.Fatalf("wrong type: %v", err)
	}
}

func TestCreateHTMLPageContentAliasAndEmpty(t *testing.T) {
	app, owner, _ := newHTMLPageAIApp(t)
	_, exec, _ := app.chatTools(owner, "127.0.0.1")

	_, err := exec("create_html_page", `{"title":"空页"}`)
	if err == nil || !strings.Contains(err.Error(), "缺少参数 html") {
		t.Fatalf("title-only: %v", err)
	}
	page := samplePageHTML("content别名")
	out, err := exec("create_html_page", `{"title":"content别名","content":`+strconv.Quote(page)+`}`)
	if err != nil {
		t.Fatal(err, out)
	}
	var doc model.Document
	if err := app.DB.Where("title = ?", "content别名").First(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if doc.Type != model.DocTypeHTML {
		t.Fatalf("type=%s", doc.Type)
	}
}

func TestCreateReplaceReadHTMLPage(t *testing.T) {
	app, owner, dir := newHTMLPageAIApp(t)
	_, exec, inject := app.chatTools(owner, "127.0.0.1")
	idStr := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }

	html1 := samplePageHTML("一年级数学卷")
	out, err := exec("create_html_page", `{"title":"一年级数学卷","html":`+strconv.Quote(html1)+`}`)
	if err != nil {
		t.Fatal(err, out)
	}
	if !strings.Contains(out, "type=html") || !strings.Contains(out, "{{doc:") {
		t.Fatalf("unexpected create result: %s", out)
	}

	var doc model.Document
	if err := app.DB.Where("title = ?", "一年级数学卷").First(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if doc.Type != model.DocTypeHTML || doc.UpdatedVia != "ai" {
		t.Fatalf("doc=%+v", doc)
	}
	m, err := parseHTMLManifest(doc.Content)
	if err != nil || !isSingleFileHTMLManifest(m) {
		t.Fatalf("manifest=%+v err=%v content=%s", m, err, doc.Content)
	}
	entryPath := filepath.Join(dir, filepath.FromSlash(m.Prefix+m.Entry))
	raw, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != html1 {
		t.Fatalf("stored html mismatch: %q", string(raw))
	}

	// create_document type=html 应拒绝
	if _, err := exec("create_document", `{"title":"x","type":"html","content":"<html></html>"}`); err == nil {
		t.Fatal("create_document html must be rejected")
	}

	readOut, err := exec("read_document", `{"id":`+idStr(doc.ID)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readOut, "html单页") || !strings.Contains(readOut, "一年级数学卷") {
		t.Fatalf("read: %s", readOut)
	}

	inj, err := inject(int64(doc.ID), 4000, "")
	if err != nil || !strings.Contains(inj, "html单页") || !strings.Contains(inj, "<style>") {
		t.Fatalf("inject: %v %s", err, inj)
	}

	html2 := samplePageHTML("一年级数学卷·改")
	rep, err := exec("replace_html_page", `{"id":`+idStr(doc.ID)+`,"html":`+strconv.Quote(html2)+`,"base_version":0}`)
	if err != nil {
		t.Fatal(err, rep)
	}
	if err := app.DB.First(&doc, doc.ID).Error; err != nil {
		t.Fatal(err)
	}
	if doc.ContentVersion != 1 {
		t.Fatalf("content_version want 1 got %d", doc.ContentVersion)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	got, _, err := app.readHTMLSiteEntry(ctx, &doc, aiMaxContentBytes)
	cancel()
	if err != nil || got != html2 {
		t.Fatalf("after replace: err=%v got=%q", err, got)
	}

	ok, note := app.taskWriteVerified("replace_html_page",
		`{"id":`+idStr(doc.ID)+`,"html":`+strconv.Quote(html2)+`,"base_version":0}`, rep)
	if !ok {
		t.Fatalf("task verify: %s", note)
	}
	// 过期 base_version 应拒绝
	if _, err := exec("replace_html_page", `{"id":`+idStr(doc.ID)+`,"html":`+strconv.Quote(html1)+`,"base_version":0}`); err == nil {
		t.Fatal("stale base_version must fail")
	}

	// 多文件整站：read 应提示自行查看，不可 replace_html_page
	multi := model.Document{
		Title: "多文件站", Slug: "multisite1", Type: model.DocTypeHTML,
		Content: `{"prefix":"html/9/abc/","entry":"index.html","files":["index.html","a.css"],"size":10,"count":2}`,
		OwnerID: owner.ID,
	}
	if err := app.DB.Create(&multi).Error; err != nil {
		t.Fatal(err)
	}
	mout, err := exec("read_document", `{"id":`+idStr(multi.ID)+`}`)
	if err != nil || !strings.Contains(mout, "多文件整站") {
		t.Fatalf("multi read: %v %s", err, mout)
	}
	if _, err := exec("replace_html_page", `{"id":`+idStr(multi.ID)+`,"html":`+strconv.Quote(html1)+`}`); err == nil {
		t.Fatal("multi-file replace must fail")
	}
}

func TestCreateHTMLPageQuotaAndOwnership(t *testing.T) {
	app, owner, _ := newHTMLPageAIApp(t)
	other := model.User{Username: "htmlother", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := app.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	id := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }

	if err := app.DB.Create(&model.SystemSetting{Key: keyAIMaxCreatesPerReq, Value: "1"}).Error; err != nil {
		t.Fatal(err)
	}
	app.setMu.Lock()
	app.setCache = nil
	app.setMu.Unlock()

	_, exec, _ := app.chatTools(owner, "")
	html := samplePageHTML("配额页")
	if _, err := exec("create_html_page", `{"title":"配额1","html":`+strconv.Quote(html)+`}`); err != nil {
		t.Fatal(err)
	}
	if _, err := exec("create_html_page", `{"title":"配额2","html":`+strconv.Quote(html)+`}`); err == nil {
		t.Fatal("second create must hit quota")
	}

	var doc model.Document
	if err := app.DB.Where("title = ?", "配额1").First(&doc).Error; err != nil {
		t.Fatal(err)
	}
	_, otherExec, _ := app.chatTools(&other, "")
	if _, err := otherExec("replace_html_page", `{"id":`+id(doc.ID)+`,"html":`+strconv.Quote(samplePageHTML("偷改"))+`,"base_version":0}`); err == nil {
		t.Fatal("non-owner replace must fail")
	}
}

func TestIsSingleFileHTMLManifest(t *testing.T) {
	if isSingleFileHTMLManifest(nil) {
		t.Fatal("nil")
	}
	ok := &HTMLSiteManifest{Files: []string{htmlEntryName}}
	if !isSingleFileHTMLManifest(ok) {
		t.Fatal("single")
	}
	multi := &HTMLSiteManifest{Files: []string{htmlEntryName, "a.css"}}
	if isSingleFileHTMLManifest(multi) {
		t.Fatal("multi")
	}
}

func TestReplaceHTMLConflictKeepsOldFiles(t *testing.T) {
	app, owner, dir := newHTMLPageAIApp(t)
	html1 := samplePageHTML("锁测")
	ctx := context.Background()
	doc, err := app.createHTMLDocFromSiteFiles(ctx, owner, "锁测", singlePageSiteFiles(html1), 0, 0, "", "ai")
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := parseHTMLManifest(doc.Content)
	oldPath := filepath.Join(dir, filepath.FromSlash(m1.Prefix+m1.Entry))

	// 先成功替换到 v1
	html2 := samplePageHTML("锁测v2")
	if _, err := app.replaceHTMLDocFromSiteFiles(ctx, doc, owner, singlePageSiteFiles(html2), "ai", 0); err != nil {
		t.Fatal(err)
	}
	// 用过期 expectVersion=0 再替换：应冲突，且当前入口文件仍存在
	html3 := samplePageHTML("锁测v3")
	_, err = app.replaceHTMLDocFromSiteFiles(ctx, doc, owner, singlePageSiteFiles(html3), "ai", 0)
	if err == nil || !errors.Is(err, errHTMLContentConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	// 重新加载：仍为 v1 内容（html2）
	var fresh model.Document
	if err := app.DB.First(&fresh, doc.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.ContentVersion != 1 {
		t.Fatalf("version=%d", fresh.ContentVersion)
	}
	got, _, err := app.readHTMLSiteEntry(ctx, &fresh, aiMaxContentBytes)
	if err != nil || got != html2 {
		t.Fatalf("old content must remain: err=%v got=%q", err, got)
	}
	_ = oldPath // 旧 prefix 可能已被异步删除；以 DB+当前入口为准
}

func TestReadHTMLSiteEntryEnforcesMaxBytes(t *testing.T) {
	app, owner, _ := newHTMLPageAIApp(t)
	// 直接写入略超小上限的文件，绕过 validateAIHTMLPage
	big := "<!DOCTYPE html><html><body>" + strings.Repeat("y", 100) + "</body></html>"
	ctx := context.Background()
	doc, err := app.createHTMLDocFromSiteFiles(ctx, owner, "大页", singlePageSiteFiles(big), 0, 0, "", "ai")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.readHTMLSiteEntry(ctx, doc, 50); err == nil {
		t.Fatal("oversized read must fail")
	}
}
