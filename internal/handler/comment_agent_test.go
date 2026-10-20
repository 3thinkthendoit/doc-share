package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newCommentAgentApp(t *testing.T) (*App, *model.User, *model.Document) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// 独立 shared 内存库 + 单连接，避免并发 goroutine 落到空库
	dsn := "file:comment_agent_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Document{}, &model.Comment{}, &model.Message{},
		&model.LLMConfig{}, &model.SystemSetting{}, &model.Share{}, &model.Project{}, &model.ProjectMember{},
	); err != nil {
		t.Fatal(err)
	}
	owner := model.User{Username: "agentowner", Nickname: "属主", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	doc := model.Document{
		Title: "Agent Doc", Slug: "agentdoc1", Content: "# Hello\n\nbody for agent",
		OwnerID: owner.ID, ContentVersion: 1,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{DB: db, Limiter: middleware.NewShareLimiter(5, time.Minute)}
	return a, &owner, &doc
}

func TestRunDocShareCommentAgentPostsBotReply(t *testing.T) {
	a, owner, doc := newCommentAgentApp(t)

	var sawChat sync.WaitGroup
	sawChat.Add(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer sawChat.Done()
		w.Header().Set("Content-Type", "text/event-stream")
		delta, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{"content": "摘要：这是一篇 Hello 文档。"},
			}},
		})
		_, _ = w.Write([]byte("data: " + string(delta) + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	mark := 1
	if err := a.DB.Create(&model.LLMConfig{
		Name: "mock", BaseURL: srv.URL, APIKey: "k", Model: "m",
		IsDefault: true, DefaultMark: &mark,
	}).Error; err != nil {
		t.Fatal(err)
	}

	parent := model.Comment{
		DocumentID: doc.ID, UserID: owner.ID,
		Content: "@DocShare 请总结本文",
	}
	if err := a.DB.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}

	a.runDocShareCommentAgent(doc.ID, parent.ID, owner.ID, "请总结本文")

	done := make(chan struct{})
	go func() {
		sawChat.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("LLM mock was not called")
	}

	var bots []model.Comment
	a.DB.Where("document_id = ? AND is_bot = ?", doc.ID, true).Find(&bots)
	if len(bots) != 1 {
		t.Fatalf("bot replies=%d want 1", len(bots))
	}
	if bots[0].ParentID != parent.ID {
		t.Fatalf("parent_id=%d want %d", bots[0].ParentID, parent.ID)
	}
	if bots[0].GuestName != model.DocShareBotName || !bots[0].IsBot {
		t.Fatalf("bot identity: %+v", bots[0])
	}
	if !strings.Contains(bots[0].Content, "Hello") && !strings.Contains(bots[0].Content, "摘要") {
		t.Fatalf("unexpected reply content: %q", bots[0].Content)
	}

	var msgs []model.Message
	a.DB.Where("user_id = ? AND kind = ?", owner.ID, model.MsgCommentReply).Find(&msgs)
	if len(msgs) == 0 {
		t.Fatal("expected notify reply message to actor")
	}
}

func TestMaybeEnqueueDocShareAgentRateLimit(t *testing.T) {
	a, owner, doc := newCommentAgentApp(t)
	uid := strconv.FormatUint(uint64(owner.ID), 10)
	commentAgentLimiter.Reset(uid, "docshare")
	t.Cleanup(func() { commentAgentLimiter.Reset(uid, "docshare") })

	// 不挂 LLM：直接把配额打满，再断言超额走限流回复（同步路径，不依赖异步 LLM）
	for i := 0; i < commentAgentMaxPerWindow; i++ {
		if !commentAgentLimiter.Allowed(uid, "docshare") {
			t.Fatalf("slot %d should be allowed", i)
		}
		commentAgentLimiter.Fail(uid, "docshare")
	}
	if commentAgentLimiter.Allowed(uid, "docshare") {
		t.Fatal("quota should be exhausted")
	}

	over := model.Comment{DocumentID: doc.ID, UserID: owner.ID, Content: "@DocShare 超额"}
	if err := a.DB.Create(&over).Error; err != nil {
		t.Fatal(err)
	}
	a.maybeEnqueueDocShareAgent(doc, &over, owner)

	deadline := time.Now().Add(2 * time.Second)
	var rateMsg string
	for time.Now().Before(deadline) {
		var bots []model.Comment
		a.DB.Where("document_id = ? AND is_bot = ?", doc.ID, true).Find(&bots)
		for _, b := range bots {
			if strings.Contains(b.Content, "过于频繁") {
				rateMsg = b.Content
				break
			}
		}
		if rateMsg != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rateMsg == "" {
		t.Fatal("expected rate-limit bot reply")
	}
}

func TestDocAddCommentRejectsGuestDocShare(t *testing.T) {
	a, _, doc := newCommentAgentApp(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(doc.ID))}}
	c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"content":"@DocShare 总结"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	// 无登录态；DocAddComment 还要求 edit 权限，这里直接测 addComment
	a.addComment(c, doc, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s want 401", w.Code, w.Body.String())
	}
}

func TestIsDocShareBotComment(t *testing.T) {
	if !isDocShareBotComment(&model.Comment{IsBot: true, GuestName: "x"}) {
		t.Fatal("IsBot flag")
	}
	if !isDocShareBotComment(&model.Comment{UserID: 0, GuestName: "DocShare"}) {
		t.Fatal("legacy GuestName")
	}
	if isDocShareBotComment(&model.Comment{UserID: 0, GuestName: "游客abcd"}) {
		t.Fatal("normal guest")
	}
}
