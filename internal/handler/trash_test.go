package handler

import (
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
