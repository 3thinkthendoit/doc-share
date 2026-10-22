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

// TestTrashRestorePurge 回收站闭环：删除=软删（分享配置保留、常规列表不可见），
// 回收站视图可见且带还原/彻底删除操作；还原后回常规列表；彻底删除执行销毁链（分享配置一并清除）
func TestTrashRestorePurge(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	owner := users["owner"]
	// 挂分享配置：验证入站「延迟销毁」与彻底删除「链式销毁」
	if err := a.DB.Create(&model.Share{DocumentID: doc.ID, ShareToken: "trashtok1"}).Error; err != nil {
		t.Fatalf("seed share: %v", err)
	}
	render := newRenderApp(t, a.DB)
	id := itoa(int(doc.ID))

	list := func(extra string) string {
		w, c := newCtx()
		asUser(c, owner, http.MethodGet, "/console/docs"+extra, "")
		render.DocsPage(c)
		assertCode(t, w, http.StatusOK)
		return w.Body.String()
	}

	// 1) 删除 = 移入回收站：行仍在且 deleted_at 置位，分享配置不销毁
	w, c := newCtx()
	c.Params = gin.Params{{Key: "id", Value: id}}
	asUser(c, owner, http.MethodDelete, "/console/api/docs/"+id, "")
	render.DeleteDoc(c)
	assertCode(t, w, http.StatusOK)
	var got model.Document
	if err := a.DB.Unscoped().First(&got, doc.ID).Error; err != nil {
		t.Fatalf("软删除后文档行应仍存在: %v", err)
	}
	if !got.DeletedAt.Valid {
		t.Fatalf("deleted_at 应置位")
	}
	var shares int64
	a.DB.Model(&model.Share{}).Where("document_id = ?", doc.ID).Count(&shares)
	if shares != 1 {
		t.Fatalf("入站不应销毁分享配置, got %d", shares)
	}

	// 2) 常规列表不可见；回收站视图可见且带还原操作
	if body := list(""); strings.Contains(body, doc.Title) {
		t.Fatalf("回收站文档不应出现在常规列表")
	}
	if body := list("?trash=1"); !strings.Contains(body, doc.Title) || !strings.Contains(body, `data-trash-op="restore"`) {
		t.Fatalf("回收站视图应列出文档并提供还原操作")
	}

	// 3) 还原：deleted_at 清空、回常规列表
	w, c = newCtx()
	c.Params = gin.Params{{Key: "id", Value: id}}
	asUser(c, owner, http.MethodPost, "/console/api/docs/"+id+"/restore", "")
	render.RestoreDoc(c)
	assertCode(t, w, http.StatusOK)
	if err := a.DB.First(&got, doc.ID).Error; err != nil {
		t.Fatalf("还原后常规查询应可见: %v", err)
	}
	if body := list(""); !strings.Contains(body, doc.Title) {
		t.Fatalf("还原后应出现在常规列表")
	}

	// 4) 彻底删除：文档行与分享配置物理销毁
	w, c = newCtx()
	c.Params = gin.Params{{Key: "id", Value: id}}
	asUser(c, owner, http.MethodDelete, "/console/api/docs/"+id, "")
	render.DeleteDoc(c)
	assertCode(t, w, http.StatusOK)
	w, c = newCtx()
	c.Params = gin.Params{{Key: "id", Value: id}}
	asUser(c, owner, http.MethodPost, "/console/api/docs/"+id+"/purge", "")
	render.PurgeDoc(c)
	assertCode(t, w, http.StatusOK)
	var n int64
	a.DB.Unscoped().Model(&model.Document{}).Where("id = ?", doc.ID).Count(&n)
	if n != 0 {
		t.Fatalf("彻底删除后文档行应消失")
	}
	a.DB.Model(&model.Share{}).Where("document_id = ?", doc.ID).Count(&shares)
	if shares != 0 {
		t.Fatalf("彻底删除应连带销毁分享配置")
	}
}

// TestEmptyTrash 清空回收站：属主可清自己的；他人回收站文档不受影响；回收站页带清空按钮
func TestEmptyTrash(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	owner := users["owner"]
	outsider := users["outsider"]
	render := newRenderApp(t, a.DB)

	other := model.Document{Title: "他人文档", Slug: "othertrash", Content: "y", OwnerID: outsider.ID}
	if err := a.DB.Create(&other).Error; err != nil {
		t.Fatalf("seed other: %v", err)
	}
	if err := a.DB.Delete(&model.Document{}, doc.ID).Error; err != nil {
		t.Fatalf("trash own: %v", err)
	}
	if err := a.DB.Delete(&model.Document{}, other.ID).Error; err != nil {
		t.Fatalf("trash other: %v", err)
	}

	// 回收站页应出现清空按钮
	w, c := newCtx()
	asUser(c, owner, http.MethodGet, "/console/docs?trash=1", "")
	render.DocsPage(c)
	assertCode(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `id="emptyTrashBtn"`) {
		t.Fatalf("回收站视图应提供清空按钮")
	}

	w, c = newCtx()
	asUser(c, owner, http.MethodPost, "/console/api/docs/trash/empty", "")
	render.EmptyTrash(c)
	assertCode(t, w, http.StatusOK)
	var resp struct {
		OK     bool `json:"ok"`
		Purged int  `json:"purged"`
		Failed int  `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.OK || resp.Purged != 1 || resp.Failed != 0 {
		t.Fatalf("empty own trash: body=%s err=%v", w.Body.String(), err)
	}
	var n int64
	a.DB.Unscoped().Model(&model.Document{}).Where("id = ?", doc.ID).Count(&n)
	if n != 0 {
		t.Fatalf("属主文档应被清空")
	}
	a.DB.Unscoped().Model(&model.Document{}).Where("id = ?", other.ID).Count(&n)
	if n != 1 {
		t.Fatalf("他人回收站文档不应被属主清空")
	}

	// 局外人清空：不能动属主已清完的、也不能误伤自己以外（此处 outsider 清自己那篇）
	w, c = newCtx()
	asUser(c, outsider, http.MethodPost, "/console/api/docs/trash/empty", "")
	render.EmptyTrash(c)
	assertCode(t, w, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.OK || resp.Purged != 1 {
		t.Fatalf("outsider empty: body=%s err=%v", w.Body.String(), err)
	}
	a.DB.Unscoped().Model(&model.Document{}).Where("id = ?", other.ID).Count(&n)
	if n != 0 {
		t.Fatalf("outsider 应清空自己的回收站文档")
	}
}

// TestEmptyTrashBatch 多篇一次清空：属主两篇均硬删，关联分享一并清除
func TestEmptyTrashBatch(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	owner := users["owner"]
	render := newRenderApp(t, a.DB)

	doc2 := model.Document{Title: "第二篇", Slug: "batchtrash2", Content: "b", OwnerID: owner.ID}
	if err := a.DB.Create(&doc2).Error; err != nil {
		t.Fatalf("seed doc2: %v", err)
	}
	if err := a.DB.Create(&model.Share{DocumentID: doc.ID, ShareToken: "batchtok1"}).Error; err != nil {
		t.Fatalf("seed share1: %v", err)
	}
	if err := a.DB.Create(&model.Share{DocumentID: doc2.ID, ShareToken: "batchtok2"}).Error; err != nil {
		t.Fatalf("seed share2: %v", err)
	}
	if err := a.DB.Delete(&model.Document{}, doc.ID).Error; err != nil {
		t.Fatalf("trash1: %v", err)
	}
	if err := a.DB.Delete(&model.Document{}, doc2.ID).Error; err != nil {
		t.Fatalf("trash2: %v", err)
	}

	w, c := newCtx()
	asUser(c, owner, http.MethodPost, "/console/api/docs/trash/empty", "")
	render.EmptyTrash(c)
	assertCode(t, w, http.StatusOK)
	var resp struct {
		OK     bool `json:"ok"`
		Purged int  `json:"purged"`
		Failed int  `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.OK || resp.Purged != 2 || resp.Failed != 0 {
		t.Fatalf("batch empty: body=%s err=%v", w.Body.String(), err)
	}
	var n int64
	a.DB.Unscoped().Model(&model.Document{}).Where("id IN ?", []uint{doc.ID, doc2.ID}).Count(&n)
	if n != 0 {
		t.Fatalf("两篇均应硬删, left=%d", n)
	}
	a.DB.Model(&model.Share{}).Where("document_id IN ?", []uint{doc.ID, doc2.ID}).Count(&n)
	if n != 0 {
		t.Fatalf("关联分享应一并清除, left=%d", n)
	}
}

// TestEmptyTrashEditorAndAdmin 项目 edit 成员可清项目内他人回收站文档；view 不可；管理员可清全站
func TestEmptyTrashEditorAndAdmin(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	editor := users["editm"]
	viewer := users["viewm"]
	admin := users["boss"]
	outsider := users["outsider"]
	render := newRenderApp(t, a.DB)

	other := model.Document{Title: "局外回收站", Slug: "outtrash2", Content: "z", OwnerID: outsider.ID}
	if err := a.DB.Create(&other).Error; err != nil {
		t.Fatalf("seed other: %v", err)
	}
	if err := a.DB.Delete(&model.Document{}, doc.ID).Error; err != nil {
		t.Fatalf("trash project doc: %v", err)
	}
	if err := a.DB.Delete(&model.Document{}, other.ID).Error; err != nil {
		t.Fatalf("trash other: %v", err)
	}

	decode := func(t *testing.T, body []byte) (purged, failed int) {
		t.Helper()
		var resp struct {
			OK     bool `json:"ok"`
			Purged int  `json:"purged"`
			Failed int  `json:"failed"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || !resp.OK {
			t.Fatalf("decode: body=%s err=%v", body, err)
		}
		return resp.Purged, resp.Failed
	}
	alive := func(id uint) bool {
		var n int64
		a.DB.Unscoped().Model(&model.Document{}).Where("id = ?", id).Count(&n)
		return n == 1
	}

	// view 成员清空：项目文档与局外文档均不动
	w, c := newCtx()
	asUser(c, viewer, http.MethodPost, "/console/api/docs/trash/empty", "")
	render.EmptyTrash(c)
	assertCode(t, w, http.StatusOK)
	if p, f := decode(t, w.Body.Bytes()); p != 0 || f != 0 {
		t.Fatalf("view empty want 0/0 got purged=%d failed=%d", p, f)
	}
	if !alive(doc.ID) || !alive(other.ID) {
		t.Fatalf("view 不应清空任何回收站文档")
	}

	// edit 成员清空：仅项目内文档
	w, c = newCtx()
	asUser(c, editor, http.MethodPost, "/console/api/docs/trash/empty", "")
	render.EmptyTrash(c)
	assertCode(t, w, http.StatusOK)
	if p, f := decode(t, w.Body.Bytes()); p != 1 || f != 0 {
		t.Fatalf("edit empty want purged=1 failed=0 got %d/%d", p, f)
	}
	if alive(doc.ID) {
		t.Fatalf("edit 应清空项目内回收站文档")
	}
	if !alive(other.ID) {
		t.Fatalf("edit 不应清空局外文档")
	}

	// 管理员清空：剩余全站回收站
	w, c = newCtx()
	asUser(c, admin, http.MethodPost, "/console/api/docs/trash/empty", "")
	render.EmptyTrash(c)
	assertCode(t, w, http.StatusOK)
	if p, f := decode(t, w.Body.Bytes()); p != 1 || f != 0 {
		t.Fatalf("admin empty want purged=1 failed=0 got %d/%d", p, f)
	}
	if alive(other.ID) {
		t.Fatalf("管理员应清空全站回收站")
	}
}

// TestTrashSweep30Days 到期清扫：入站超 30 天的文档被自动彻底删除，未到期者保留
func TestTrashSweep30Days(t *testing.T) {
	a, doc := newAuthApp(t)
	if err := a.DB.Delete(&model.Document{}, doc.ID).Error; err != nil {
		t.Fatalf("trash: %v", err)
	}
	// 把 deleted_at 回拨 31 天模拟到期
	old := time.Now().AddDate(0, 0, -31)
	if err := a.DB.Unscoped().Model(&model.Document{}).Where("id = ?", doc.ID).Update("deleted_at", old).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}
	// 刚入站的文档应保留
	fresh := model.Document{Title: "刚删除", Slug: "fresh123", Content: "x", OwnerID: doc.OwnerID}
	if err := a.DB.Create(&fresh).Error; err != nil {
		t.Fatalf("seed fresh: %v", err)
	}
	if err := a.DB.Delete(&model.Document{}, fresh.ID).Error; err != nil {
		t.Fatalf("trash fresh: %v", err)
	}

	a.sweepTrash()

	var n int64
	a.DB.Unscoped().Model(&model.Document{}).Where("id = ?", doc.ID).Count(&n)
	if n != 0 {
		t.Fatalf("入站 31 天的文档应被清扫")
	}
	a.DB.Unscoped().Model(&model.Document{}).Where("id = ?", fresh.ID).Count(&n)
	if n != 1 {
		t.Fatalf("刚入站的文档不应被清扫")
	}
}
