package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newLLMApp(t *testing.T) *App {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.LLMConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &App{DB: db}
}

func callLLM(a *App, method, id string, body any, h func(*gin.Context)) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	c.Request = httptest.NewRequest(method, "/", &buf)
	c.Request.Header.Set("Content-Type", "application/json")
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	h(c)
	return w
}

func TestAssembleTestLLMDoesNotBorrowOtherKey(t *testing.T) {
	row := &model.LLMConfig{BaseURL: "https://saved.example", APIKey: "saved-secret", Model: "saved-model"}
	cfg, msg := assembleTestLLM(llmTestReq{
		BaseURL: "https://new.example",
		Model:   "new-model",
	}, nil)
	if msg != "" {
		t.Fatal(msg)
	}
	if cfg.APIKey != "" {
		t.Fatalf("new config borrowed key %q", cfg.APIKey)
	}
	if cfg.BaseURL != "https://new.example" || cfg.Model != "new-model" {
		t.Fatalf("cfg=%+v", cfg)
	}

	cfg, msg = assembleTestLLM(llmTestReq{BaseURL: "https://edited.example"}, row)
	if msg != "" {
		t.Fatal(msg)
	}
	if cfg.APIKey != "saved-secret" || cfg.Model != "saved-model" {
		t.Fatalf("edit should keep this row only: %+v", cfg)
	}
	if cfg.BaseURL != "https://edited.example" {
		t.Fatalf("base=%s", cfg.BaseURL)
	}
}

func TestLLMConfigJSONOmitsAPIKey(t *testing.T) {
	a := newLLMApp(t)
	w := callLLM(a, http.MethodPost, "", map[string]any{
		"name": "DeepSeek", "base_url": "https://api.deepseek.com", "api_key": "sk-secret", "model": "deepseek-chat",
	}, a.CreateLLMConfig)
	if w.Code != http.StatusOK {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("sk-secret")) || bytes.Contains(w.Body.Bytes(), []byte("api_key")) {
		t.Fatalf("create response leaked key: %s", w.Body.String())
	}

	w = callLLM(a, http.MethodGet, "", nil, a.ListLLMConfigs)
	if w.Code != http.StatusOK {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("sk-secret")) {
		t.Fatalf("list leaked key: %s", w.Body.String())
	}

	var rows []model.LLMConfig
	if err := a.DB.Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].APIKey != "sk-secret" || !rows[0].IsDefault {
		t.Fatalf("stored=%+v err=%v", rows, err)
	}
}

func TestDeleteOnlyLLMRejected(t *testing.T) {
	a := newLLMApp(t)
	row := model.LLMConfig{Name: "only", BaseURL: "https://a.example", Model: "m", APIKey: "k", IsDefault: true}
	if err := a.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	w := callLLM(a, http.MethodDelete, strconv.FormatUint(uint64(row.ID), 10), nil, a.DeleteLLMConfig)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d %s", w.Code, w.Body.String())
	}
	var n int64
	a.DB.Model(&model.LLMConfig{}).Count(&n)
	if n != 1 {
		t.Fatalf("count=%d", n)
	}
}

func TestDeleteDefaultPromotesNext(t *testing.T) {
	a := newLLMApp(t)
	first := model.LLMConfig{Name: "a", BaseURL: "https://a.example", Model: "m", IsDefault: true}
	second := model.LLMConfig{Name: "b", BaseURL: "https://b.example", Model: "m"}
	if err := a.DB.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	w := callLLM(a, http.MethodDelete, strconv.FormatUint(uint64(first.ID), 10), nil, a.DeleteLLMConfig)
	if w.Code != http.StatusOK {
		t.Fatalf("delete %d %s", w.Code, w.Body.String())
	}
	var left model.LLMConfig
	if err := a.DB.First(&left, second.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !left.IsDefault || left.DefaultMark == nil || *left.DefaultMark != 1 {
		t.Fatalf("remaining row should become the only default: %+v", left)
	}
	cfg := a.DefaultLLM()
	if cfg.BaseURL != "https://b.example" {
		t.Fatalf("default=%+v", cfg)
	}
}
