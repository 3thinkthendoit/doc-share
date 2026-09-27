package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"doc-share/internal/config"
	"doc-share/internal/handler"
	"doc-share/internal/session"

	"github.com/gin-gonic/gin"
)

// TestRoutesRegister 验证全部路由可注册（gin 树冲突会在注册时 panic）且模板可解析
func TestRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	defer gin.SetMode(gin.TestMode)

	cfg := &config.Config{} // 注册路由只读取极少量配置字段，空配置即可
	app := handler.NewApp(cfg, nil, session.NewSigner("0123456789abcdef0123456789abcdef"), nil, nil)
	if app == nil {
		t.Fatal("app is nil")
	}
	// gin 树冲突（如 wildcard 与静态段竞争）会在注册阶段 panic，测试框架捕获即为失败
	_ = New(app, os.DirFS("../web/static"))

	// 模板语法验证：新增模板（html_view / html_edit）与既有模板须全部可解析
	tplRoot := os.Getenv("DOC_SHARE_TPL_ROOT")
	if tplRoot == "" {
		tplRoot = "../../web/templates"
	}
	if _, err := handler.ParseTemplates(os.DirFS(tplRoot)); err != nil {
		t.Fatalf("parse templates: %v", err)
	}
}

// TestUploadsBlocksHTMLSite 安全护栏：/uploads 不得直出 HTML 整站文件（html/ 前缀），
// 否则成为密码分享/未分享文档的公开旁路；普通上传图片不受影响。
func TestUploadsBlocksHTMLSite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	defer gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "html", "9", "ab12cd34"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "202601"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "html", "9", "ab12cd34", "index.html"), []byte("<p>secret site</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "202601", "a.png"), []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.Upload.Dir = dir
	app := handler.NewApp(cfg, nil, session.NewSigner("0123456789abcdef0123456789abcdef"), nil, nil)
	r := New(app, os.DirFS("../web/static"))

	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		return w
	}

	if w := get("/uploads/html/9/ab12cd34/index.html"); w.Code != http.StatusNotFound {
		t.Fatalf("/uploads/html/... must be 404, got %d", w.Code)
	}
	if w := get("/uploads/html/"); w.Code != http.StatusNotFound {
		t.Fatalf("/uploads/html/ must be 404, got %d", w.Code)
	}
	if w := get("/uploads/202601/a.png"); w.Code != http.StatusOK {
		t.Fatalf("normal upload must stay 200, got %d", w.Code)
	}
}
