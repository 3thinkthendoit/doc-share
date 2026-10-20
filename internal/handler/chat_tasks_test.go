package handler

import (
	"strconv"
	"strings"
	"testing"

	"doc-share/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMultiDocIntent(t *testing.T) {
	if !multiDocIntent("10个专题的文档 需要在补充完善一下") {
		t.Fatal("counted topics must be multi-doc")
	}
	if !multiDocIntent("这些文档都补充一下") {
		t.Fatal("这些文档 must be multi-doc")
	}
	if multiDocIntent("把标题补充一句说明") {
		t.Fatal("single sentence edit must not be multi-doc")
	}
	if multiDocQuery("10个专题的文档需要补充") != "专题" {
		t.Fatal("query should be 专题")
	}
	if multiDocIntent("第3篇补充一下") {
		t.Fatal("第N篇 is one document")
	}
	if multiDocIntent("改2个字") {
		t.Fatal("N个字 is not a document count")
	}
	if continueBoard("继续用这个格式写一段") {
		t.Fatal("继续 must be the whole message")
	}
	if !continueBoard("  继续") {
		t.Fatal("继续 should resume the board")
	}
	var b taskBoard
	b.items = []agentTask{
		{Title: "甲", State: taskPending, DocID: 41},
		{Title: "乙", State: taskDone, DocID: 42},
		{Title: "丙", State: taskPending, DocID: 43},
		{Title: "丁", State: taskPending, DocID: 44},
	}
	prompt := b.batchPrompt(2)
	if !strings.Contains(prompt, "id=41") || !strings.Contains(prompt, "id=43") || strings.Contains(prompt, "id=44") || strings.Contains(prompt, "id=42") {
		t.Fatalf("batch should be the first two unfinished: %s", prompt)
	}
	b.bindTurn(2)
	if !b.turnUnfinished() {
		t.Fatal("bound batch should be unfinished")
	}
	if !b.finishDoc(43, true) || b.items[2].State != taskDone || b.items[0].State != taskPending {
		t.Fatalf("success must land on the written doc: %+v", b.items)
	}
	if b.finishDoc(99, true) {
		t.Fatal("unknown doc must not complete another task")
	}
	b.finishDoc(41, true)
	if b.turnUnfinished() {
		t.Fatal("later items outside this turn must not keep the nudge going")
	}
	if b.items[3].State != taskPending {
		t.Fatal("doc 44 stays for the next continue")
	}
	titles := make([]string, 10)
	for i := range titles {
		titles[i] = "专题"
	}
	raw, _ := jsonMarshalTasks(titles)
	if _, err := parseDeclareTasks(raw); err != nil {
		t.Fatal(err)
	}
}

func jsonMarshalTasks(titles []string) (string, error) {
	buf := []byte(`{"tasks":[`)
	for i, t := range titles {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '"')
		buf = append(buf, t...)
		buf = append(buf, '"')
	}
	buf = append(buf, "]}"...)
	return string(buf), nil
}

func TestParseDeclareTasks(t *testing.T) {
	got, err := parseDeclareTasks(`{"tasks":[" 建文档 ","改一节"]}`)
	if err != nil || len(got) != 2 || got[0] != "建文档" {
		t.Fatalf("parse: %v %v", got, err)
	}
	if _, err := parseDeclareTasks(`{"tasks":["只有一条"]}`); err == nil {
		t.Fatal("single task must be rejected")
	}
	if _, err := parseDeclareTasks(`{"tasks":"nope"}`); err == nil {
		t.Fatal("non-array must be rejected")
	}
}

func TestTaskBoardAdvancesOnlyByResult(t *testing.T) {
	var b taskBoard
	titles, err := parseDeclareTasks(`{"tasks":["甲","乙"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.reset(titles); err != nil {
		t.Fatal(err)
	}
	b.markRunning()
	b.finishCurrent(false)
	if b.items[0].State != taskFailed || !b.unfinished() {
		t.Fatalf("failed write must stay unfinished: %+v", b.items)
	}
	b.markRunning()
	b.finishCurrent(true)
	if b.items[0].State != taskFailed || b.items[1].State != taskDone {
		t.Fatalf("next success must complete the pending item, not the failed one: %+v", b.items)
	}
	b.markRunning()
	b.finishCurrent(true)
	if b.items[0].State != taskDone || b.unfinished() || b.pendingSummary() != "" {
		t.Fatalf("retry after no pending left: %+v summary=%q", b.items, b.pendingSummary())
	}
	if taskTracksTool("set_share") || taskTracksTool("create_project") || taskTracksTool("add_comment") {
		t.Fatal("non-doc mutations must not advance the task list")
	}
	if !taskTracksTool("replace_section") || !taskTracksTool("create_document") ||
		!taskTracksTool("create_html_page") || !taskTracksTool("replace_html_page") {
		t.Fatal("doc writes must advance the task list")
	}
	if err := b.reset([]string{"重来", "再来"}); err == nil {
		t.Fatal("cannot redeclare after a task is done")
	}
}

func TestTaskWriteVerifiedReread(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Document{}); err != nil {
		t.Fatal(err)
	}
	doc := model.Document{Title: "稿", Slug: "slugtask1", Content: "旧别处 新段落", OwnerID: 1}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	app := &App{DB: db}
	id := strconv.FormatUint(uint64(doc.ID), 10)
	result := "已完成局部替换（id=" + id + "）。校验通过：新内容已写入。"
	args := `{"id":` + id + `,"old_text":"旧","new_text":"新段落"}`
	ok, note := app.taskWriteVerified("replace_section", args, result)
	if !ok || !strings.Contains(note, "校验注意") {
		t.Fatalf("remaining old_text elsewhere must still pass: %v %s", ok, note)
	}
	bad := `{"id":` + id + `,"old_text":"旧","new_text":"不在正文里"}`
	ok, note = app.taskWriteVerified("replace_section", bad, result)
	if ok || !strings.Contains(note, "校验失败") {
		t.Fatalf("reread fail: %v %s", ok, note)
	}
}
