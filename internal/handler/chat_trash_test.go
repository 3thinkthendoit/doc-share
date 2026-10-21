package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newChatTrashTestApp(t *testing.T) (*App, *model.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := "file:chat_trash_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.User{}, &model.ChatSession{}, &model.ChatMessage{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "u1", PasswordHash: "x", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return &App{DB: db}, &user
}

func chatCtx(t *testing.T, a *App, user *model.User, method, path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	c.Request = r
	c.Set(middleware.ContextUser, user)
	return c, w
}

func TestChatSessionSoftDeleteRestorePurge(t *testing.T) {
	a, user := newChatTrashTestApp(t)
	s := model.ChatSession{UserID: user.ID, Title: "t1"}
	if err := a.DB.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	msg := model.ChatMessage{SessionID: s.ID, Role: "user", Content: "hi"}
	if err := a.DB.Create(&msg).Error; err != nil {
		t.Fatal(err)
	}

	c, w := chatCtx(t, a, user, http.MethodDelete, "/x/"+strconv.FormatUint(uint64(s.ID), 10), nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(s.ID), 10)}}
	a.DeleteChatSession(c)
	if w.Code != http.StatusOK {
		t.Fatalf("soft delete status=%d body=%s", w.Code, w.Body.String())
	}

	var active int64
	a.DB.Model(&model.ChatSession{}).Where("id = ?", s.ID).Count(&active)
	if active != 0 {
		t.Fatal("软删后普通查询仍应看不见会话")
	}
	var trashed model.ChatSession
	if err := a.DB.Unscoped().First(&trashed, s.ID).Error; err != nil || !trashed.DeletedAt.Valid {
		t.Fatalf("应在回收站: err=%v deleted=%v", err, trashed.DeletedAt.Valid)
	}
	var msgCount int64
	a.DB.Model(&model.ChatMessage{}).Where("session_id = ?", s.ID).Count(&msgCount)
	if msgCount != 1 {
		t.Fatalf("软删不得删消息 got=%d", msgCount)
	}

	c, w = chatCtx(t, a, user, http.MethodGet, "/x?trash=1", nil)
	a.ListChatSessions(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list trash status=%d", w.Code)
	}
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 {
		t.Fatalf("回收站应有 1 条 got=%d", len(list.Sessions))
	}

	c, w = chatCtx(t, a, user, http.MethodPost, "/x/"+strconv.FormatUint(uint64(s.ID), 10)+"/restore", nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(s.ID), 10)}}
	a.RestoreChatSession(c)
	if w.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", w.Code, w.Body.String())
	}
	a.DB.Model(&model.ChatSession{}).Where("id = ?", s.ID).Count(&active)
	if active != 1 {
		t.Fatal("还原后应出现在活跃列表")
	}

	// 再软删后彻底删除
	c, w = chatCtx(t, a, user, http.MethodDelete, "/x", nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(s.ID), 10)}}
	a.DeleteChatSession(c)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	c, w = chatCtx(t, a, user, http.MethodPost, "/x/purge", nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(s.ID), 10)}}
	a.PurgeChatSession(c)
	if w.Code != http.StatusOK {
		t.Fatalf("purge status=%d body=%s", w.Code, w.Body.String())
	}
	var nSess, nMsg int64
	a.DB.Unscoped().Model(&model.ChatSession{}).Where("id = ?", s.ID).Count(&nSess)
	a.DB.Model(&model.ChatMessage{}).Where("session_id = ?", s.ID).Count(&nMsg)
	if nSess != 0 || nMsg != 0 {
		t.Fatalf("彻底删除后应无残留 session=%d msg=%d", nSess, nMsg)
	}
}

func TestRenameChatSession(t *testing.T) {
	a, user := newChatTrashTestApp(t)
	s := model.ChatSession{UserID: user.ID, Title: "旧名"}
	if err := a.DB.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"title": "  新名称  "})
	c, w := chatCtx(t, a, user, http.MethodPatch, "/x", body)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(s.ID), 10)}}
	a.RenameChatSession(c)
	if w.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", w.Code, w.Body.String())
	}
	var got model.ChatSession
	if err := a.DB.First(&got, s.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Title != "新名称" {
		t.Fatalf("title=%q", got.Title)
	}

	empty, _ := json.Marshal(map[string]string{"title": "   "})
	c, w = chatCtx(t, a, user, http.MethodPatch, "/x", empty)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(s.ID), 10)}}
	a.RenameChatSession(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空标题应 400 got=%d", w.Code)
	}
}

func TestSweepChatTrashPurgesExpired(t *testing.T) {
	a, user := newChatTrashTestApp(t)
	old := model.ChatSession{UserID: user.ID, Title: "old"}
	fresh := model.ChatSession{UserID: user.ID, Title: "fresh"}
	if err := a.DB.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Delete(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Delete(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	expired := time.Now().AddDate(0, 0, -(chatTrashRetentionDays + 1))
	if err := a.DB.Unscoped().Model(&model.ChatSession{}).Where("id = ?", old.ID).
		Update("deleted_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	msg := model.ChatMessage{SessionID: old.ID, Role: "user", Content: "bye"}
	if err := a.DB.Create(&msg).Error; err != nil {
		t.Fatal(err)
	}

	a.sweepChatTrash()

	var nOld, nFresh, nMsg int64
	a.DB.Unscoped().Model(&model.ChatSession{}).Where("id = ?", old.ID).Count(&nOld)
	a.DB.Unscoped().Model(&model.ChatSession{}).Where("id = ?", fresh.ID).Count(&nFresh)
	a.DB.Model(&model.ChatMessage{}).Where("session_id = ?", old.ID).Count(&nMsg)
	if nOld != 0 {
		t.Fatal("超期会话应被清扫")
	}
	if nFresh != 1 {
		t.Fatal("未超期会话应保留在回收站")
	}
	if nMsg != 0 {
		t.Fatal("超期会话消息应一并销毁")
	}
}

func TestRenameChatSessionRejectsTrashed(t *testing.T) {
	a, user := newChatTrashTestApp(t)
	s := model.ChatSession{UserID: user.ID, Title: "t"}
	if err := a.DB.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Delete(&s).Error; err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"title": "x"})
	c, w := chatCtx(t, a, user, http.MethodPatch, "/x", body)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(s.ID), 10)}}
	a.RenameChatSession(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("回收站会话不可重命名 got=%d body=%s", w.Code, w.Body.String())
	}
}
