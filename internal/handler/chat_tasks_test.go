package handler

import (
	"strconv"
	"strings"
	"testing"

	"doc-share/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

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
	if !taskTracksTool("replace_section") || !taskTracksTool("create_document") {
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
