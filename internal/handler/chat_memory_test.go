package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"doc-share/internal/llm"
	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRollingSummaryMessagesEmpty(t *testing.T) {
	if rollingSummaryMessages("") != nil || rollingSummaryMessages("  ") != nil {
		t.Fatal("empty summary should yield nil")
	}
	msgs := rollingSummaryMessages("用户要改 id=3 周报")
	if len(msgs) != 1 || msgs[0].Role != "user" || !strings.Contains(msgs[0].Content, "[会话记忆]") {
		t.Fatalf("unexpected inject: %+v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "id=3") {
		t.Fatalf("missing summary body: %q", msgs[0].Content)
	}
}

func TestInjectRollingSummaryAfterSystem(t *testing.T) {
	base := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}
	got := injectRollingSummaryAfterSystem(base, "记一下")
	if len(got) != 3 || got[0].Role != "system" || !strings.Contains(got[1].Content, "[会话记忆]") || got[2].Content != "hi" {
		t.Fatalf("inject: %+v", got)
	}
	// 替换已有记忆，不叠两层
	got2 := injectRollingSummaryAfterSystem(got, "新记忆")
	if len(got2) != 3 || !strings.Contains(got2[1].Content, "新记忆") || strings.Count(got2[1].Content+got2[2].Content, "[会话记忆]") != 1 {
		t.Fatalf("replace: %+v", got2)
	}
	got3 := injectRollingSummaryAfterSystem(base, "")
	if len(got3) != 2 {
		t.Fatalf("empty summary should not inject: %+v", got3)
	}
}

func TestClipRollingSummary(t *testing.T) {
	if clipRollingSummary("  hi  ") != "hi" {
		t.Fatal("trim")
	}
	long := strings.Repeat("字", chatMemorySummaryMaxRunes+10)
	got := clipRollingSummary(long)
	if utf8.RuneCountInString(got) > chatMemorySummaryMaxRunes {
		t.Fatalf("len runes=%d want <=%d", utf8.RuneCountInString(got), chatMemorySummaryMaxRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("want ellipsis suffix, got %q", got)
	}
}

func TestFoldLineOmitsToolBlob(t *testing.T) {
	line := foldLineForSummary(model.ChatMessage{
		Role: "assistant", Content: "[工具查询结果]\n" + strings.Repeat("x", 2000),
	})
	if strings.Contains(line, "xxx") || !strings.Contains(line, "已省略") {
		t.Fatalf("tool blob should be omitted: %q", line)
	}
}

func TestLoadMemoryFold(t *testing.T) {
	a := newChatMemoryTestApp(t)
	s := model.ChatSession{UserID: 1, Title: "t"}
	if err := a.DB.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m := model.ChatMessage{SessionID: s.ID, Role: "user", Content: "m" + strconv.Itoa(i)}
		if err := a.DB.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	var all []model.ChatMessage
	a.DB.Where("session_id = ?", s.ID).Order("id asc").Find(&all)
	if len(all) != 5 {
		t.Fatalf("seed=%d", len(all))
	}
	// foldBefore = all[3].ID → 应折入 all[0],all[1],all[2]
	foldBefore := all[3].ID
	fold := a.loadMemoryFold(s.ID, 0, foldBefore)
	if len(fold) != 3 {
		t.Fatalf("fold=%d want 3", len(fold))
	}
	if fold[0].ID != all[0].ID || fold[2].ID != all[2].ID {
		t.Fatalf("fold ids unexpected: %+v", fold)
	}
	// 已摘要到 all[1] → 只剩 all[2]
	fold2 := a.loadMemoryFold(s.ID, all[1].ID, foldBefore)
	if len(fold2) != 1 || fold2[0].ID != all[2].ID {
		t.Fatalf("partial fold: %+v", fold2)
	}
	// 已覆盖到 foldBefore 左侧 → 空
	if got := a.loadMemoryFold(s.ID, all[2].ID, foldBefore); len(got) != 0 {
		t.Fatalf("expect empty, got %+v", got)
	}
}

// 字符预算裁掉的消息：foldBefore 取「实际保留的最旧 id」，更旧 id 应进入 fold。
func TestTrimHistoryWithImagesOldestKeptForBudgetDrop(t *testing.T) {
	a := newChatMemoryTestApp(t)
	// 最新一条短，其后多条顶满单条上限，使总量超过 chatBudgetMaxChars
	fat := strings.Repeat("x", chatMsgMaxChars)
	n := chatBudgetMaxChars/chatMsgMaxChars + 3
	history := make([]model.ChatMessage, n)
	for i := 0; i < n; i++ {
		history[i] = model.ChatMessage{
			ID: uint(n - i), Role: "assistant", Content: fat,
		}
	}
	history[0] = model.ChatMessage{ID: uint(n), Role: "user", Content: "最新问题"}

	msgs, foldBefore := a.trimHistoryWithImages(history)
	if foldBefore == 0 || foldBefore == 1 {
		t.Fatalf("foldBefore=%d；应保留若干新消息且丢掉最旧", foldBefore)
	}
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "最新问题") {
		t.Fatalf("kept msgs missing 最新: len=%d", len(msgs))
	}
	// 被裁掉的最旧几条 id < foldBefore
	var dropped int
	for _, m := range history {
		if m.ID < foldBefore {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("expected some history ids below foldBefore (budget drops)")
	}

	s := model.ChatSession{UserID: 1, Title: "t"}
	if err := a.DB.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		content := fat
		role := "assistant"
		if i == n {
			content, role = "最新问题", "user"
		}
		m := model.ChatMessage{SessionID: s.ID, Role: role, Content: content}
		if err := a.DB.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	var all []model.ChatMessage
	a.DB.Where("session_id = ?", s.ID).Order("id desc").Find(&all)
	_, fb := a.trimHistoryWithImages(all)
	got := a.loadMemoryFold(s.ID, 0, fb)
	if len(got) == 0 {
		t.Fatalf("budget-dropped msgs should fold; fb=%d total=%d", fb, len(all))
	}
	for _, m := range got {
		if m.ID >= fb {
			t.Fatalf("fold msg id=%d should be < foldBefore=%d", m.ID, fb)
		}
	}
}

func TestMaybeRefreshRollingSummary(t *testing.T) {
	a := newChatMemoryTestApp(t)
	var saw sync.WaitGroup
	saw.Add(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer saw.Done()
		w.Header().Set("Content-Type", "text/event-stream")
		delta, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{"content": "用户约定改 id=9 周报；未完成：补充结论。"},
			}},
		})
		_, _ = w.Write([]byte("data: " + string(delta) + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	s := model.ChatSession{UserID: 1, Title: "mem"}
	if err := a.DB.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m := model.ChatMessage{SessionID: s.ID, Role: "user", Content: "旧消息" + strconv.Itoa(i)}
		if err := a.DB.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	var all []model.ChatMessage
	a.DB.Where("session_id = ?", s.ID).Order("id asc").Find(&all)
	windowOldest := all[3].ID

	cfg := llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	a.maybeRefreshRollingSummary(context.Background(), cfg, &s, windowOldest)

	done := make(chan struct{})
	go func() {
		saw.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("summary LLM not called")
	}

	if !strings.Contains(s.RollingSummary, "id=9") {
		t.Fatalf("in-memory summary: %q", s.RollingSummary)
	}
	if s.SummaryUptoID != all[2].ID {
		t.Fatalf("SummaryUptoID=%d want %d", s.SummaryUptoID, all[2].ID)
	}
	var row model.ChatSession
	if err := a.DB.First(&row, s.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.RollingSummary != s.RollingSummary || row.SummaryUptoID != s.SummaryUptoID {
		t.Fatalf("db mismatch: %+v vs inmem summary=%q upto=%d", row, s.RollingSummary, s.SummaryUptoID)
	}

	// 再刷一次：无新 fold，不应再调上游
	a.maybeRefreshRollingSummary(context.Background(), cfg, &s, windowOldest)
}

func TestMaybeRefreshRollingSummaryKeepsOldOnFail(t *testing.T) {
	a := newChatMemoryTestApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := model.ChatSession{UserID: 1, Title: "mem", RollingSummary: "旧摘要保留", SummaryUptoID: 0}
	if err := a.DB.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		m := model.ChatMessage{SessionID: s.ID, Role: "user", Content: "x"}
		if err := a.DB.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	var all []model.ChatMessage
	a.DB.Where("session_id = ?", s.ID).Order("id asc").Find(&all)

	cfg := llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	a.maybeRefreshRollingSummary(context.Background(), cfg, &s, all[2].ID)

	if s.RollingSummary != "旧摘要保留" || s.SummaryUptoID != 0 {
		t.Fatalf("must keep old on fail: summary=%q upto=%d", s.RollingSummary, s.SummaryUptoID)
	}
	var row model.ChatSession
	a.DB.First(&row, s.ID)
	if row.RollingSummary != "旧摘要保留" || row.SummaryUptoID != 0 {
		t.Fatalf("db changed on fail: %+v", row)
	}
}

func newChatMemoryTestApp(t *testing.T) *App {
	t.Helper()
	dsn := "file:chat_mem_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.User{}, &model.ChatSession{}, &model.ChatMessage{}, &model.LLMConfig{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	return &App{DB: db, Limiter: middleware.NewShareLimiter(5, time.Minute)}
}
