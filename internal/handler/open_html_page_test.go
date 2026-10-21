package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"doc-share/internal/config"
	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newOpenHTMLApp(t *testing.T) (*App, *model.User, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// cache=shared + 单连接：避免 memory SQLite 连接池开出空库导致 Updates 静默 0 行
	db, err := gorm.Open(sqlite.Open("file:openhtml_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Document{}, &model.Project{}, &model.Category{}, &model.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	owner := model.User{Username: "openhtml", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	app := &App{
		DB: db,
		Cfg: &config.Config{Upload: config.UploadConfig{
			Dir: dir, HTMLMaxSizeMB: 10, HTMLMaxFiles: 100,
		}},
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ContextUser, &owner)
		c.Set("viaOpenAPI", true)
		c.Next()
	})
	r.POST("/openapi/v1/docs/html-page", app.OpenCreateHTMLPage)
	r.GET("/openapi/v1/docs/:id/html-page", app.OpenGetHTMLPage)
	r.PUT("/openapi/v1/docs/:id/html-page", app.OpenReplaceHTMLPage)
	return app, &owner, r
}

func TestOpenAPIHTMLPageCRUD(t *testing.T) {
	app, owner, r := newOpenHTMLApp(t)
	html1 := samplePageHTML("开放平台试卷")
	body, _ := json.Marshal(map[string]any{"title": "开放平台试卷", "html": html1})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openapi/v1/docs/html-page", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Data model.Document `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Type != model.DocTypeHTML || created.Data.OwnerID != owner.ID || created.Data.UpdatedVia != "api" {
		t.Fatalf("doc=%+v", created.Data)
	}
	m, err := parseHTMLManifest(created.Data.Content)
	if err != nil || !isSingleFileHTMLManifest(m) {
		t.Fatalf("manifest=%+v err=%v", m, err)
	}

	id := strconv.FormatUint(uint64(created.Data.ID), 10)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/openapi/v1/docs/"+id+"/html-page", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Data struct {
			HTML string `json:"html"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Data.HTML != html1 {
		t.Fatalf("get html: %v %q", err, got.Data.HTML)
	}

	html2 := samplePageHTML("修订卷")
	repBody, _ := json.Marshal(map[string]any{"html": html2, "base_version": 0})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/openapi/v1/docs/"+id+"/html-page", bytes.NewReader(repBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("replace status=%d body=%s", w.Code, w.Body.String())
	}
	// 过期 base_version → 409
	stale, _ := json.Marshal(map[string]any{"html": html1, "base_version": 0})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/openapi/v1/docs/"+id+"/html-page", bytes.NewReader(stale))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale base_version want 409 got %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/openapi/v1/docs/"+id+"/html-page", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Data.HTML != html2 {
		t.Fatalf("after replace: %q", got.Data.HTML)
	}

	var doc model.Document
	if err := app.DB.First(&doc, created.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if doc.ContentVersion != 1 || doc.UpdatedVia != "api" {
		t.Fatalf("after replace doc=%+v", doc)
	}

	// 非法片段
	bad, _ := json.Marshal(map[string]any{"title": "x", "html": "<div>no</div>"})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/openapi/v1/docs/html-page", bytes.NewReader(bad))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad html want 400 got %d", w.Code)
	}

	// content 别名；缺 html/content → 缺参
	aliasHTML := samplePageHTML("content别名开放")
	aliasBody, _ := json.Marshal(map[string]any{"title": "content别名开放", "content": aliasHTML})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/openapi/v1/docs/html-page", bytes.NewReader(aliasBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("content alias create status=%d body=%s", w.Code, w.Body.String())
	}
	emptyBody, _ := json.Marshal(map[string]any{"title": "无正文"})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/openapi/v1/docs/html-page", bytes.NewReader(emptyBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte("缺少参数 html")) {
		t.Fatalf("empty want 缺少参数 html got %d %s", w.Code, w.Body.String())
	}
}
