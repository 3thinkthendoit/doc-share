package handler

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"doc-share/internal/llm"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newExtraApp(t *testing.T) (*App, *model.User, *model.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Project{}, &model.Category{}, &model.Document{},
		&model.ProjectMember{}, &model.ProjectShare{}, &model.ShareAccessRequest{},
		&model.DocTemplate{}, &model.Comment{}, &model.DocumentRevision{},
		&model.DocumentVisitor{}, &model.Share{}, &model.Message{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	owner := model.User{Username: "owner2", Nickname: "属主", Role: model.RoleViewer, Status: model.StatusEnabled}
	other := model.User{Username: "other2", Nickname: "协作者", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := db.Create(&owner).Error; err != nil || db.Create(&other).Error != nil {
		t.Fatalf("seed: %v", err)
	}
	return &App{DB: db}, &owner, &other
}

func TestCreateCanvasDocAndTemplate(t *testing.T) {
	app, owner, _ := newExtraApp(t)
	_, exec, _ := app.chatTools(owner)
	out, err := exec("create_document", `{"title":"图","type":"mindmap","content":"{\"data\":{\"text\":\"中心\"},\"children\":[]}"}`)
	if err != nil {
		t.Fatal(err, out)
	}
	var doc model.Document
	if err := app.DB.Where("title = ?", "图").First(&doc).Error; err != nil || doc.Type != model.DocTypeMindmap {
		t.Fatalf("doc=%+v err=%v", doc, err)
	}
	tout, err := exec("create_template", `{"name":"画板范例","type":"board","content":"{\"type\":\"excalidraw\",\"version\":2,\"elements\":[],\"appState\":{},\"files\":{}}"}`)
	if err != nil {
		t.Fatal(err, tout)
	}
	var tpl model.DocTemplate
	if err := app.DB.Where("name = ?", "画板范例").First(&tpl).Error; err != nil || tpl.Type != model.DocTypeBoard {
		t.Fatalf("tpl=%+v err=%v", tpl, err)
	}
	aout, err := exec("apply_template", `{"template_id":`+strconv.FormatUint(uint64(tpl.ID), 10)+`}`)
	if err != nil {
		t.Fatal(err, aout)
	}
	var applied model.Document
	if err := app.DB.Where("title = ?", "画板范例").First(&applied).Error; err != nil || applied.Type != model.DocTypeBoard {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
}

func TestProjectMemberAndShare(t *testing.T) {
	app, owner, other := newExtraApp(t)
	id := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }
	_, exec, _ := app.chatTools(owner)
	if _, err := exec("create_project", `{"name":"协作"}`); err != nil {
		t.Fatal(err)
	}
	var p model.Project
	if err := app.DB.Where("name = ?", "协作").First(&p).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("add_project_member", `{"project_id":`+id(p.ID)+`,"username":"other2","role":"edit"}`); err != nil {
		t.Fatal(err)
	}
	listed, err := exec("list_project_members", `{"project_id":`+id(p.ID)+`}`)
	if err != nil || !strings.Contains(listed, "other2") && !strings.Contains(listed, "协作者") {
		t.Fatalf("list members: %v %s", err, listed)
	}
	if _, err := exec("set_project_share", `{"project_id":`+id(p.ID)+`,"enabled":true,"can_edit":false}`); err != nil {
		t.Fatal(err)
	}
	got, err := exec("get_project_share", `{"project_id":`+id(p.ID)+`}`)
	if err != nil || !strings.Contains(got, "{{pshare:") {
		t.Fatalf("share: %v %s", err, got)
	}
	req := model.ShareAccessRequest{ProjectID: p.ID, UserID: other.ID, GuestName: "访客", RequestToken: "tokentokentokentokentokentokentok1", Status: model.AccessPending}
	if err := app.DB.Create(&req).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("review_access_request", `{"project_id":`+id(p.ID)+`,"request_id":`+id(req.ID)+`,"action":"approve"}`); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.First(&req, req.ID).Error; err != nil || req.Status != model.AccessApproved {
		t.Fatalf("status=%d err=%v", req.Status, err)
	}
	var msgN int64
	app.DB.Model(&model.Message{}).Where("user_id = ? AND kind = ?", other.ID, model.MsgAccessApproved).Count(&msgN)
	if msgN != 1 {
		t.Fatalf("applicant was not notified, messages=%d", msgN)
	}
	// 未传 expire_days 不得清空已有过期时间
	exp := time.Now().Add(48 * time.Hour)
	if err := app.DB.Model(&model.ProjectShare{}).Where("project_id = ?", p.ID).Update("expire_at", exp).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("set_project_share", `{"project_id":`+id(p.ID)+`,"enabled":true,"can_edit":true}`); err != nil {
		t.Fatal(err)
	}
	var ps model.ProjectShare
	if err := app.DB.Where("project_id = ?", p.ID).First(&ps).Error; err != nil || ps.ExpireAt == nil {
		t.Fatalf("expire cleared: %+v %v", ps, err)
	}
	_, otherExec, _ := app.chatTools(other)
	if _, err := otherExec("remove_project_member", `{"project_id":`+id(p.ID)+`,"user_id":`+id(other.ID)+`}`); err == nil {
		t.Fatal("member must not remove others as owner")
	}
	if _, err := exec("remove_project_member", `{"project_id":`+id(p.ID)+`,"user_id":`+id(other.ID)+`}`); err != nil {
		t.Fatal(err)
	}
}

func TestCommentDuplicatePurgeAndApprovals(t *testing.T) {
	app, owner, _ := newExtraApp(t)
	id := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }
	_, exec, _ := app.chatTools(owner)
	if _, err := exec("create_document", `{"title":"原文","content":"hello"}`); err != nil {
		t.Fatal(err)
	}
	var doc model.Document
	if err := app.DB.Where("title = ?", "原文").First(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("add_comment", `{"id":`+id(doc.ID)+`,"content":"第一条"}`); err != nil {
		t.Fatal(err)
	}
	var cm model.Comment
	if err := app.DB.Where("document_id = ?", doc.ID).First(&cm).Error; err != nil {
		t.Fatal(err)
	}
	listed, err := exec("list_comments", `{"id":`+id(doc.ID)+`}`)
	if err != nil || !strings.Contains(listed, "第一条") {
		t.Fatalf("comments: %v %s", err, listed)
	}
	dout, err := exec("duplicate_document", `{"id":`+id(doc.ID)+`}`)
	if err != nil || !strings.Contains(dout, "副本") && !strings.Contains(dout, "{{doc:") {
		t.Fatalf("dup: %v %s", err, dout)
	}
	rev := model.DocumentRevision{DocumentID: doc.ID, Content: "old", EditorName: "x"}
	if err := app.DB.Create(&rev).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("label_revision", `{"id":`+id(doc.ID)+`,"revision_id":`+id(rev.ID)+`,"label":"v1.0.1","note":"发版"}`); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Delete(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("purge_document", `{"id":`+id(doc.ID)+`}`); err != nil {
		t.Fatal(err)
	}
	var n int64
	app.DB.Unscoped().Model(&model.Document{}).Where("id = ?", doc.ID).Count(&n)
	if n != 0 {
		t.Fatalf("still exists: %d", n)
	}
	for _, name := range []string{"remove_project_member", "delete_template", "delete_comment", "purge_document", "review_access_request"} {
		tc := llm.ToolCallMsg{}
		tc.Function.Name = name
		tc.Function.Arguments = `{}`
		if !aiNeedsApproval(tc) {
			t.Fatalf("%s must require approval", name)
		}
	}
	tc := llm.ToolCallMsg{}
	tc.Function.Name = "set_project_share"
	tc.Function.Arguments = `{"enabled":true}`
	if !aiNeedsApproval(tc) {
		t.Fatal("opening project share must require approval")
	}
	tc.Function.Arguments = `{"enabled":false}`
	if aiNeedsApproval(tc) {
		t.Fatal("closing project share should not require approval")
	}
	tc.Function.Name = "add_project_member"
	tc.Function.Arguments = `{"role":"edit"}`
	if !aiNeedsApproval(tc) {
		t.Fatal("adding an edit member must require approval")
	}
	tc.Function.Arguments = `{"role":"view"}`
	if aiNeedsApproval(tc) {
		t.Fatal("adding a view member should not require approval")
	}
	if _, err := exec("replace_section", `{"id":1,"old_text":"a","new_text":"b"}`); err == nil {
		t.Fatal("missing doc should fail")
	}
	canvas, err := exec("create_document", `{"title":"脑图","type":"mindmap","content":"{\"data\":{\"text\":\"根\"}}"}`)
	if err != nil {
		t.Fatal(err, canvas)
	}
	var md model.Document
	if err := app.DB.Where("title = ?", "脑图").First(&md).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("replace_section", `{"id":`+id(md.ID)+`,"old_text":"根","new_text":"叶"}`); err == nil {
		t.Fatal("replace_section must reject canvas docs")
	}
	if _, err := exec("update_document", `{"id":`+id(md.ID)+`,"content":"not json","base_version":0}`); err == nil {
		t.Fatal("canvas update must reject non-JSON")
	}
}

func TestAgentAtomicShareCommentAndGuards(t *testing.T) {
	app, owner, other := newExtraApp(t)
	id := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }
	_, exec, _ := app.chatTools(owner)

	if _, err := exec("create_project", `{"name":"归属"}`); err != nil {
		t.Fatal(err)
	}
	var proj model.Project
	if err := app.DB.Where("name = ?", "归属").First(&proj).Error; err != nil {
		t.Fatal(err)
	}
	cat := model.Category{Name: "属主分类", OwnerID: owner.ID}
	if err := app.DB.Create(&cat).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("create_template", `{"name":"项目模板","content":"正文","project_id":`+id(proj.ID)+`,"category_id":`+id(cat.ID)+`}`); err != nil {
		t.Fatal(err)
	}
	var tpl model.DocTemplate
	if err := app.DB.Where("name = ?", "项目模板").First(&tpl).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("add_project_member", `{"project_id":`+id(proj.ID)+`,"username":"other2","role":"view"}`); err != nil {
		t.Fatal(err)
	}
	_, memberExec, _ := app.chatTools(other)
	if _, err := memberExec("get_project_share", `{"project_id":`+id(proj.ID)+`}`); err == nil {
		t.Fatal("member must not read project share")
	}
	if _, err := memberExec("list_access_requests", `{"project_id":`+id(proj.ID)+`}`); err == nil {
		t.Fatal("member must not list access requests")
	}
	applied, err := memberExec("apply_template", `{"template_id":`+id(tpl.ID)+`}`)
	if err != nil {
		t.Fatal(err, applied)
	}
	if !strings.Contains(applied, "未分组") || !strings.Contains(applied, "未分类") {
		t.Fatalf("dropped binding not reported: %s", applied)
	}
	var dropped model.Document
	if err := app.DB.Where("owner_id = ? AND title = ?", other.ID, "项目模板").First(&dropped).Error; err != nil {
		t.Fatal(err)
	}
	if dropped.ProjectID != 0 || dropped.CategoryID != 0 {
		t.Fatalf("binding kept: project=%d category=%d", dropped.ProjectID, dropped.CategoryID)
	}

	if _, err := exec("create_document", `{"title":"可分享","content":"正文"}`); err != nil {
		t.Fatal(err)
	}
	var doc model.Document
	if err := app.DB.Where("title = ?", "可分享").First(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("set_share", `{"id":`+id(doc.ID)+`,"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.First(&doc, doc.ID).Error; err != nil || !doc.IsShared {
		t.Fatalf("share flag: %+v %v", doc, err)
	}
	var shareN int64
	app.DB.Model(&model.Share{}).Where("document_id = ?", doc.ID).Count(&shareN)
	if shareN != 1 {
		t.Fatalf("share rows=%d", shareN)
	}
	req := model.ShareAccessRequest{DocumentID: doc.ID, UserID: other.ID, GuestName: "访客", RequestToken: "tokentokentokentokentokentokentok2", Status: model.AccessPending}
	if err := app.DB.Create(&req).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("delete_document", `{"id":`+id(doc.ID)+`}`); err != nil {
		t.Fatal(err)
	}
	var trashed model.Document
	if err := app.DB.Unscoped().First(&trashed, doc.ID).Error; err != nil || !trashed.DeletedAt.Valid || trashed.IsShared {
		t.Fatalf("trash state: %+v %v", trashed, err)
	}
	app.DB.Model(&model.Share{}).Where("document_id = ?", doc.ID).Count(&shareN)
	var reqN int64
	app.DB.Model(&model.ShareAccessRequest{}).Where("document_id = ?", doc.ID).Count(&reqN)
	if shareN != 0 || reqN != 0 {
		t.Fatalf("share=%d requests=%d", shareN, reqN)
	}

	if _, err := exec("create_document", `{"title":"评论稿","content":"正文"}`); err != nil {
		t.Fatal(err)
	}
	var commented model.Document
	if err := app.DB.Where("title = ?", "评论稿").First(&commented).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("add_comment", `{"id":`+id(commented.ID)+`,"content":"楼主"}`); err != nil {
		t.Fatal(err)
	}
	var top model.Comment
	if err := app.DB.Where("document_id = ? AND parent_id = 0", commented.ID).First(&top).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("add_comment", `{"id":`+id(commented.ID)+`,"parent_id":`+id(top.ID)+`,"content":"自回复"}`); err == nil {
		t.Fatal("self reply must be rejected")
	}
	reply := model.Comment{DocumentID: commented.ID, ParentID: top.ID, UserID: other.ID, Content: "他人回复"}
	if err := app.DB.Create(&reply).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("delete_comment", `{"id":`+id(commented.ID)+`,"comment_id":`+id(top.ID)+`}`); err != nil {
		t.Fatal(err)
	}
	var cmN int64
	app.DB.Model(&model.Comment{}).Where("document_id = ?", commented.ID).Count(&cmN)
	if cmN != 0 {
		t.Fatalf("comments left: %d", cmN)
	}

	htmlDoc := model.Document{Title: "站点", Slug: "htmlsite01", Type: model.DocTypeHTML, Content: "<html></html>", OwnerID: owner.ID}
	if err := app.DB.Create(&htmlDoc).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("duplicate_document", `{"id":`+id(htmlDoc.ID)+`}`); err == nil {
		t.Fatal("html duplicate must be refused")
	}

	_, limited, _ := app.chatTools(owner)
	for i := 0; i < 3; i++ {
		if _, err := limited("create_document", `{"title":"配额`+strconv.Itoa(i)+`","content":"x"}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := limited("create_document", `{"title":"超额","content":"x"}`); err == nil {
		t.Fatal("4th create must hit per-request quota")
	}
	if _, err := limited("duplicate_document", `{"id":`+id(commented.ID)+`}`); err == nil {
		t.Fatal("duplicate must share the create quota")
	}
	if _, err := limited("apply_template", `{"template_id":`+id(tpl.ID)+`}`); err == nil {
		t.Fatal("apply_template must share the create quota")
	}

	if err := app.DB.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&model.SystemSetting{Key: keyAIMaxCreatesPerReq, Value: "1"}).Error; err != nil {
		t.Fatal(err)
	}
	app.setCache = nil
	_, tight, _ := app.chatTools(owner)
	if _, err := tight("create_document", `{"title":"配置后仍允许","content":"x"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := tight("create_document", `{"title":"配置后应拒绝","content":"x"}`); err == nil {
		t.Fatal("configured per-request quota must apply")
	}
}
