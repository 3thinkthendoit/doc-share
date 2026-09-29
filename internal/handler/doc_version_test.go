package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

// docDataResp 解析创建/更新接口响应中的 data.id 与 data.version
type docDataResp struct {
	Data struct {
		ID      uint   `json:"id"`
		Version string `json:"version"`
	} `json:"data"`
}

func decodeDocData(t *testing.T, w *httptest.ResponseRecorder) docDataResp {
	t.Helper()
	var resp docDataResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode doc data: %v, body = %s", err, w.Body.String())
	}
	return resp
}

// createDocViaAPI 以 u 身份新建 markdown 文档，返回文档 ID 与响应里的版本号
func createDocViaAPI(t *testing.T, a *App, u *model.User, body string) (uint, string) {
	t.Helper()
	w, c := newCtx()
	asUser(c, u, http.MethodPost, "/console/api/docs", body)
	a.CreateDoc(c)
	assertCode(t, w, http.StatusOK)
	d := decodeDocData(t, w)
	return d.Data.ID, d.Data.Version
}

// updateDocViaAPI 以 u 身份更新文档，返回响应里的版本号
func updateDocViaAPI(t *testing.T, a *App, u *model.User, docID, body string) string {
	t.Helper()
	w, c := newCtx()
	c.Params = gin.Params{{Key: "id", Value: docID}}
	asUser(c, u, http.MethodPut, "/console/api/docs/"+docID, body)
	a.UpdateDoc(c)
	assertCode(t, w, http.StatusOK)
	return decodeDocData(t, w).Data.Version
}

// TestAutoVersionLifecycle 版本号自动派生：新建 v1.0.0，每次内容保存 +1，仅改标题时不变
func TestAutoVersionLifecycle(t *testing.T) {
	a, _ := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	owner := users["owner"]

	id, ver := createDocViaAPI(t, a, owner, `{"title":"版本文档","content":"# 初稿"}`)
	if ver != "v1.0.0" {
		t.Fatalf("新建后 version = %q, want v1.0.0", ver)
	}
	docID := itoa(int(id))

	if v := updateDocViaAPI(t, a, owner, docID, `{"content":"# 第二稿"}`); v != "v1.0.1" {
		t.Fatalf("首次内容保存后 version = %q, want v1.0.1", v)
	}
	if v := updateDocViaAPI(t, a, owner, docID, `{"content":"# 第三稿"}`); v != "v1.0.2" {
		t.Fatalf("二次内容保存后 version = %q, want v1.0.2", v)
	}
	// 仅改标题（未传 content）：内容未落库，版本号应保持
	if v := updateDocViaAPI(t, a, owner, docID, `{"title":"只改标题"}`); v != "v1.0.2" {
		t.Fatalf("仅改标题后 version = %q, want 保持 v1.0.2", v)
	}
}

// TestAutoVersionIgnoresManualInput 请求体里手填的 version 一律被忽略，以系统派生值为准
func TestAutoVersionIgnoresManualInput(t *testing.T) {
	a, _ := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	owner := users["owner"]

	id, ver := createDocViaAPI(t, a, owner, `{"title":"手填版本","content":"c","version":"v9.9.9"}`)
	if ver != "v1.0.0" {
		t.Fatalf("新建时手填 version 应被忽略，got %q, want v1.0.0", ver)
	}
	docID := itoa(int(id))
	if v := updateDocViaAPI(t, a, owner, docID, `{"content":"c2","version":"v8.8.8"}`); v != "v1.0.1" {
		t.Fatalf("更新时手填 version 应被忽略，got %q, want v1.0.1", v)
	}
}

// TestAutoVersionOnRollback 回滚是一次内容保存，版本号继续 +1 且内容还原到快照
func TestAutoVersionOnRollback(t *testing.T) {
	a, _ := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	owner := users["owner"]

	id, _ := createDocViaAPI(t, a, owner, `{"title":"回滚文档","content":"# v0"}`)
	docID := itoa(int(id))
	// 内容更新一次 → v1.0.1，同事务留下覆盖前快照（# v0）
	if v := updateDocViaAPI(t, a, owner, docID, `{"content":"# v1"}`); v != "v1.0.1" {
		t.Fatalf("内容更新后 version = %q, want v1.0.1", v)
	}
	var rev model.DocumentRevision
	if err := a.DB.Where("document_id = ?", id).First(&rev).Error; err != nil {
		t.Fatalf("load revision: %v", err)
	}
	rid := itoa(int(rev.ID))
	w, c := newCtx()
	c.Params = gin.Params{{Key: "id", Value: docID}, {Key: "rid", Value: rid}}
	asUser(c, owner, http.MethodPost, "/console/api/docs/"+docID+"/revisions/"+rid+"/rollback", "")
	a.RollbackRevision(c)
	assertCode(t, w, http.StatusOK)

	var doc model.Document
	if err := a.DB.First(&doc, id).Error; err != nil {
		t.Fatalf("reload doc: %v", err)
	}
	if doc.Version != "v1.0.2" {
		t.Errorf("回滚后 version = %q, want v1.0.2", doc.Version)
	}
	if doc.Content != "# v0" {
		t.Errorf("回滚后 content = %q, want # v0", doc.Content)
	}
}
