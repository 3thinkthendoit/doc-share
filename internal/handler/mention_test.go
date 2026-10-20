package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestExtractMentionTokens(t *testing.T) {
	got := extractMentionTokens("嗨 @owner 看看 @DocShare 以及 @owner 重复")
	if len(got) != 2 {
		t.Fatalf("tokens=%v want 2 unique", got)
	}
	if !contentMentionsDocShare("请 @docshare 总结") {
		t.Fatal("expected DocShare mention case-insensitive")
	}
	if contentMentionsDocShare("普通评论 @owner") {
		t.Fatal("should not treat member as DocShare")
	}
}

func TestResolveMentionedUserIDs(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatal(err)
	}
	content := "请 @viewm 和 @属主 看一下，并抄送 @DocShare"
	ids := a.resolveMentionedUserIDs(doc, content, users["editm"].ID)
	seen := map[uint]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen[users["viewm"].ID] {
		t.Fatalf("expected viewm mentioned, got %v", ids)
	}
	if !seen[users["owner"].ID] {
		t.Fatalf("expected owner by nickname, got %v", ids)
	}
	if seen[users["editm"].ID] {
		t.Fatal("should not mention self")
	}
}

func TestDocMentionCandidates(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(doc.ID))}}
	c.Request = httptest.NewRequest(http.MethodGet, "/console/api/docs/"+strconv.Itoa(int(doc.ID))+"/mention-candidates", nil)
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	c.Set(middleware.ContextUser, users["owner"])
	a.DocMentionCandidates(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Candidates []mentionCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var hasBot, hasView bool
	for _, cnd := range resp.Candidates {
		if cnd.Kind == "bot" && cnd.Username == model.DocShareBotName {
			hasBot = true
		}
		if cnd.Username == "viewm" {
			hasView = true
		}
		if cnd.ID == users["owner"].ID {
			t.Fatal("owner should be excluded from own candidate list")
		}
	}
	if !hasBot {
		t.Fatal("missing DocShare bot candidate")
	}
	if !hasView {
		t.Fatal("missing project member candidate")
	}
}

func TestOpenDocChatSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.ProjectMember{}, &model.Document{},
		&model.ChatSession{}, &model.ChatMessage{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	owner := model.User{Username: "o1", Nickname: "O", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	doc := model.Document{Title: "绑定对话文档", Slug: "chatdoc01", Content: "body", OwnerID: owner.ID}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{DB: db}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(doc.ID))}}
	c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	c.Set(middleware.ContextUser, &owner)
	a.DocOpenChatSession(c)
	if w.Code != http.StatusOK {
		t.Fatalf("first open status=%d body=%s", w.Code, w.Body.String())
	}
	var resp1 struct {
		Session model.ChatSession `json:"session"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp1); err != nil {
		t.Fatal(err)
	}
	if resp1.Session.DocumentID != doc.ID || resp1.Session.UserID != owner.ID {
		t.Fatalf("session=%+v", resp1.Session)
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(doc.ID))}}
	c2.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
	c2.Request.Header.Set("Content-Type", "application/json")
	c2.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	c2.Set(middleware.ContextUser, &owner)
	a.DocOpenChatSession(c2)
	var resp2 struct {
		Session model.ChatSession `json:"session"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.Session.ID != resp1.Session.ID {
		t.Fatalf("expected reuse session %d got %d", resp1.Session.ID, resp2.Session.ID)
	}
}

func TestResolveMentionSkipsAmbiguousNickname(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatal(err)
	}
	// 让 viewm 与 editm 共用昵称「同名」
	if err := a.DB.Model(users["viewm"]).Update("nickname", "同名").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(users["editm"]).Update("nickname", "同名").Error; err != nil {
		t.Fatal(err)
	}
	ids := a.resolveMentionedUserIDs(doc, "请 @同名 看一下", users["owner"].ID)
	if len(ids) != 0 {
		t.Fatalf("ambiguous nick should notify nobody, got %v", ids)
	}
	// 用户名仍可精确命中
	ids = a.resolveMentionedUserIDs(doc, "请 @viewm 看一下", users["owner"].ID)
	if len(ids) != 1 || ids[0] != users["viewm"].ID {
		t.Fatalf("username resolve = %v", ids)
	}
}

func TestStripDocShareMentions(t *testing.T) {
	got := strings.TrimSpace(stripDocShareMentions("@DocShare 请总结第二节"))
	if strings.Contains(strings.ToLower(got), "docshare") {
		t.Fatalf("still has bot mention: %q", got)
	}
	if !strings.Contains(got, "请总结第二节") {
		t.Fatalf("lost task text: %q", got)
	}
}

func TestMentionQueryAt(t *testing.T) {
	q, ok := mentionQueryAt("hello @vi")
	if !ok || q != "vi" {
		t.Fatalf("got %q ok=%v", q, ok)
	}
	if _, ok := mentionQueryAt("email a@b.com"); ok {
		t.Fatal("email should not trigger")
	}
}
