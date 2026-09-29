package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

// revItem 修订列表项（只取断言用字段）
type revItem struct {
	ID    uint   `json:"id"`
	Seq   int    `json:"seq"`
	Label string `json:"label"`
	Note  string `json:"note"`
}

type revListResp struct {
	Revisions []revItem `json:"revisions"`
}

// seedRevisions 按时间先后插入快照，返回 id 列表（旧 → 新）
func seedRevisions(t *testing.T, a *App, docID, editorID uint, contents ...string) []uint {
	t.Helper()
	ids := make([]uint, 0, len(contents))
	for i, body := range contents {
		rev := model.DocumentRevision{
			DocumentID: docID, EditorID: editorID, EditorName: "属主",
			Content: body,
			// 显式递增时间：同秒创建时 created_at 相同会让排序不确定，序号断言就不稳定
			CreatedAt: time.Date(2026, 9, 29, 10, i, 0, 0, time.UTC),
		}
		if err := a.DB.Create(&rev).Error; err != nil {
			t.Fatalf("seed revision %d: %v", i, err)
		}
		ids = append(ids, rev.ID)
	}
	return ids
}

func listRevisions(t *testing.T, a *App, u *model.User, docID string) revListResp {
	t.Helper()
	w, c := newCtx()
	c.Params = gin.Params{{Key: "id", Value: docID}}
	asUser(c, u, http.MethodGet, "/console/api/docs/"+docID+"/revisions", "")
	a.ListRevisions(c)
	assertCode(t, w, http.StatusOK)
	var resp revListResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode revisions: %v, body = %s", err, w.Body.String())
	}
	return resp
}

// putRevisionLabel 打标签并返回状态码（不断言，交由调用方决定期望值）
func putRevisionLabel(t *testing.T, a *App, u *model.User, docID, rid, body string) int {
	t.Helper()
	w, c := newCtx()
	c.Params = gin.Params{{Key: "id", Value: docID}, {Key: "rid", Value: rid}}
	asUser(c, u, http.MethodPut, "/console/api/docs/"+docID+"/revisions/"+rid+"/label", body)
	a.LabelRevision(c)
	return w.Code
}

// TestRevisionSeqNumbering 展示序号：Seq 不入库，由 ListRevisions 按时间正序编号
func TestRevisionSeqNumbering(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	docID := itoa(int(doc.ID))
	ids := seedRevisions(t, a, doc.ID, users["owner"].ID, "第一版", "第二版", "第三版")

	// Seq 标了 gorm:"-"，不应建列：否则存量快照全是 0，界面会显示一堆 #0
	if a.DB.Migrator().HasColumn(&model.DocumentRevision{}, "seq") {
		t.Error("seq 不应入库（gorm:\"-\" 未生效）")
	}

	resp := listRevisions(t, a, users["owner"], docID)
	if len(resp.Revisions) != 3 {
		t.Fatalf("修订数 = %d, want 3", len(resp.Revisions))
	}
	// 列表按时间倒序返回，序号按正序编号：最新一条 = 总条数
	wantSeq := map[uint]int{ids[0]: 1, ids[1]: 2, ids[2]: 3}
	for _, r := range resp.Revisions {
		if wantSeq[r.ID] != r.Seq {
			t.Errorf("修订 %d 的 seq = %d, want %d", r.ID, r.Seq, wantSeq[r.ID])
		}
	}
	if resp.Revisions[0].ID != ids[2] {
		t.Errorf("列表首条 = %d, want 最新修订 %d", resp.Revisions[0].ID, ids[2])
	}
}

// TestRevisionLabelUniqueness 版本标签：同文档内唯一、跨文档可重名、留空即取消
func TestRevisionLabelUniqueness(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	docID := itoa(int(doc.ID))
	ids := seedRevisions(t, a, doc.ID, users["owner"].ID, "第一版", "第二版")

	if code := putRevisionLabel(t, a, users["owner"], docID, itoa(int(ids[0])), `{"label":"v1.0.1","note":"首次发布"}`); code != http.StatusOK {
		t.Fatalf("打标签 status = %d, want 200", code)
	}
	// 同文档内重复版本号 → 409
	if code := putRevisionLabel(t, a, users["owner"], docID, itoa(int(ids[1])), `{"label":"v1.0.1"}`); code != http.StatusConflict {
		t.Errorf("重复标签 status = %d, want 409", code)
	}
	// 重打自己那条不算冲突（查重需排除自身）
	if code := putRevisionLabel(t, a, users["owner"], docID, itoa(int(ids[0])), `{"label":"v1.0.1","note":"补充说明"}`); code != http.StatusOK {
		t.Errorf("重打自身标签 status = %d, want 200", code)
	}

	// 标签与说明随列表回显
	byID := map[uint]revItem{}
	for _, r := range listRevisions(t, a, users["owner"], docID).Revisions {
		byID[r.ID] = r
	}
	if byID[ids[0]].Label != "v1.0.1" || byID[ids[0]].Note != "补充说明" {
		t.Errorf("回显 label=%q note=%q, want v1.0.1 / 补充说明", byID[ids[0]].Label, byID[ids[0]].Note)
	}
	if byID[ids[1]].Label != "" {
		t.Errorf("未打标签的修订 label=%q, want 空", byID[ids[1]].Label)
	}

	// 另一篇文档可复用同一版本号（唯一性按文档划分）
	other := model.Document{Title: "另一篇", Slug: "other123", Content: "x", OwnerID: users["owner"].ID}
	if err := a.DB.Create(&other).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	otherIDs := seedRevisions(t, a, other.ID, users["owner"].ID, "另一篇第一版")
	if code := putRevisionLabel(t, a, users["owner"], itoa(int(other.ID)), itoa(int(otherIDs[0])), `{"label":"v1.0.1"}`); code != http.StatusOK {
		t.Errorf("跨文档同名标签 status = %d, want 200", code)
	}

	// 留空取消标签；多条空标签可共存（空值不参与查重）
	if code := putRevisionLabel(t, a, users["owner"], docID, itoa(int(ids[0])), `{"label":"  ","note":""}`); code != http.StatusOK {
		t.Errorf("取消标签 status = %d, want 200", code)
	}
	if code := putRevisionLabel(t, a, users["owner"], docID, itoa(int(ids[1])), `{"label":""}`); code != http.StatusOK {
		t.Errorf("多条空标签 status = %d, want 200", code)
	}
	var got model.DocumentRevision
	if err := a.DB.First(&got, ids[0]).Error; err != nil {
		t.Fatalf("reload revision: %v", err)
	}
	if got.Label != "" || got.Note != "" {
		t.Errorf("取消标签后 label=%q note=%q, want 均为空", got.Label, got.Note)
	}
}

// TestRevisionLabelGuards 打标签的权限与入参守卫
func TestRevisionLabelGuards(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	docID := itoa(int(doc.ID))
	ids := seedRevisions(t, a, doc.ID, users["owner"].ID, "第一版")
	rid := itoa(int(ids[0]))

	// 打标签是写操作：只读成员不可，edit 角色项目成员可
	if code := putRevisionLabel(t, a, users["viewm"], docID, rid, `{"label":"v1.0.0"}`); code != http.StatusForbidden {
		t.Errorf("只读成员 status = %d, want 403", code)
	}
	if code := putRevisionLabel(t, a, users["editm"], docID, rid, `{"label":"v1.0.0"}`); code != http.StatusOK {
		t.Errorf("编辑成员 status = %d, want 200", code)
	}
	// 修订不属于该文档 → 404，防止借自己有权的文档给别家修订打标签
	if code := putRevisionLabel(t, a, users["owner"], docID, "999999", `{"label":"v9.9.9"}`); code != http.StatusNotFound {
		t.Errorf("不存在修订 status = %d, want 404", code)
	}
	// 版本号超长 → 400（与数据库 size:32、前端 maxlength 一致）
	if code := putRevisionLabel(t, a, users["owner"], docID, rid, `{"label":"`+strings.Repeat("x", 33)+`"}`); code != http.StatusBadRequest {
		t.Errorf("33 字符标签 status = %d, want 400", code)
	}
	// 按字符数而非字节数计：32 个中文（96 字节）不超长
	if code := putRevisionLabel(t, a, users["owner"], docID, rid, `{"label":"`+strings.Repeat("版", 32)+`"}`); code != http.StatusOK {
		t.Errorf("32 个中文字符 status = %d, want 200", code)
	}
	// 说明超长 → 400
	if code := putRevisionLabel(t, a, users["owner"], docID, rid, `{"note":"`+strings.Repeat("长", 501)+`"}`); code != http.StatusBadRequest {
		t.Errorf("501 字说明 status = %d, want 400", code)
	}
}
