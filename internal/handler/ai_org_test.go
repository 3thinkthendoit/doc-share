package handler

import (
	"strconv"
	"strings"
	"testing"

	"doc-share/internal/llm"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newOrgApp(t *testing.T) (*App, *model.User, *model.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Project{}, &model.Category{}, &model.Document{},
		&model.ProjectMember{}, &model.ProjectShare{}, &model.ShareAccessRequest{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	owner := model.User{Username: "owner", Nickname: "属主", Role: model.RoleViewer, Status: model.StatusEnabled}
	other := model.User{Username: "other", Nickname: "他人", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := db.Create(&owner).Error; err != nil || db.Create(&other).Error != nil {
		t.Fatalf("seed: %v", err)
	}
	return &App{DB: db}, &owner, &other
}

func TestAgentProjectAndCategoryCRUD(t *testing.T) {
	app, owner, other := newOrgApp(t)
	_, exec, _ := app.chatTools(owner, "")

	id := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }

	out, err := exec("create_project", `{"name":"周报","description":"本周"}`)
	if err != nil || !strings.Contains(out, "id=") {
		t.Fatalf("create project: %v %s", err, out)
	}
	var p model.Project
	if err := app.DB.Where("name = ?", "周报").First(&p).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("update_project", `{"id":`+id(p.ID)+`,"name":"周报","description":"更新"}`); err != nil {
		t.Fatal(err)
	}

	catOut, err := exec("create_category", `{"name":"产品","sort":2}`)
	if err != nil || !strings.Contains(catOut, "id=") {
		t.Fatalf("create category: %v %s", err, catOut)
	}
	if _, err := exec("create_category", `{"name":"产品"}`); err == nil {
		t.Fatal("duplicate category should fail")
	}
	var cat model.Category
	if err := app.DB.Where("name = ?", "产品").First(&cat).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("update_category", `{"id":`+id(cat.ID)+`,"name":"产品文档"}`); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.First(&cat, cat.ID).Error; err != nil || cat.Sort != 2 || cat.Name != "产品文档" {
		t.Fatalf("sort should stay 2, got %+v err=%v", cat, err)
	}

	docOut, err := exec("create_document", `{"title":"一篇","content":"正文","project_id":`+id(p.ID)+`,"category_id":`+id(cat.ID)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	var doc model.Document
	if err := app.DB.Where("title = ?", "一篇").First(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if doc.ProjectID != p.ID || doc.CategoryID != cat.ID {
		t.Fatalf("bind project=%d category=%d doc=%+v out=%s", p.ID, cat.ID, doc, docOut)
	}
	if _, err := exec("update_document", `{"id":`+id(doc.ID)+`,"project_id":0,"category_id":0}`); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.First(&doc, doc.ID).Error; err != nil || doc.ProjectID != 0 || doc.CategoryID != 0 {
		t.Fatalf("clear assign: %+v %v", doc, err)
	}
	if err := app.DB.Model(&doc).Updates(map[string]any{"project_id": p.ID, "category_id": cat.ID}).Error; err != nil {
		t.Fatal(err)
	}

	_, otherExec, _ := app.chatTools(other, "")
	if _, err := otherExec("delete_project", `{"id":`+id(p.ID)+`}`); err == nil {
		t.Fatal("other user must not delete the project")
	}
	if _, err := exec("delete_project", `{"id":`+id(p.ID)+`}`); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.First(&doc, doc.ID).Error; err != nil || doc.ProjectID != 0 {
		t.Fatalf("docs should ungroup: %+v %v", doc, err)
	}
	if _, err := exec("delete_category", `{"id":`+id(cat.ID)+`}`); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.First(&doc, doc.ID).Error; err != nil || doc.CategoryID != 0 {
		t.Fatalf("docs should uncategorize: %+v %v", doc, err)
	}

	listed, err := exec("list_categories", `{}`)
	if err != nil || !strings.Contains(listed, "没有分类") {
		t.Fatalf("list categories: %v %s", err, listed)
	}
}

func TestDeleteOrgNeedsApproval(t *testing.T) {
	for _, name := range []string{"delete_project", "delete_category"} {
		tc := llm.ToolCallMsg{}
		tc.Function.Name = name
		tc.Function.Arguments = `{"id":1}`
		if !aiNeedsApproval(tc) {
			t.Fatalf("%s must require approval", name)
		}
	}
}

func TestAssignRequiresOwnerAndOwnTarget(t *testing.T) {
	app, owner, other := newOrgApp(t)
	id := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }
	_, ownerExec, _ := app.chatTools(owner, "")
	out, err := ownerExec("create_project", `{"name":"属主项目"}`)
	if err != nil {
		t.Fatal(err, out)
	}
	var p model.Project
	if err := app.DB.Where("owner_id = ?", owner.ID).First(&p).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&model.ProjectMember{ProjectID: p.ID, UserID: other.ID, Role: model.MemberRoleEdit}).Error; err != nil {
		t.Fatal(err)
	}
	doc := model.Document{Title: "共享", Slug: "slugorg1", Type: model.DocTypeMarkdown, Content: "正文", OwnerID: owner.ID, ProjectID: p.ID, Version: "v1.0.0"}
	if err := app.DB.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	_, otherExec, _ := app.chatTools(other, "")
	if _, err := otherExec("update_document", `{"id":`+id(doc.ID)+`,"project_id":0}`); err == nil {
		t.Fatal("edit member must not change project assignment")
	}
	if err := app.DB.First(&doc, doc.ID).Error; err != nil || doc.ProjectID != p.ID {
		t.Fatalf("assignment changed: %+v %v", doc, err)
	}
	foreign := model.Project{Name: "他人项目", OwnerID: other.ID}
	if err := app.DB.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ownerExec("create_document", `{"title":"越权","project_id":`+id(foreign.ID)+`}`); err == nil {
		t.Fatal("must not assign another user's project")
	}
}

func TestAdminListsOnlyOwnCategories(t *testing.T) {
	app, owner, other := newOrgApp(t)
	if err := app.DB.Model(owner).Update("role", model.RoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	owner.Role = model.RoleAdmin
	if err := app.DB.Create(&model.Category{Name: "他人分类", OwnerID: other.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&model.Category{Name: "自己的", OwnerID: owner.ID}).Error; err != nil {
		t.Fatal(err)
	}
	_, exec, _ := app.chatTools(owner, "")
	out, err := exec("list_categories", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "他人分类") || !strings.Contains(out, "自己的") {
		t.Fatalf("admin list leaked or missed: %s", out)
	}
}

func TestPurgeProjectClearsShareAndTrash(t *testing.T) {
	app, owner, _ := newOrgApp(t)
	id := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }
	_, exec, _ := app.chatTools(owner, "")
	if _, err := exec("create_project", `{"name":"待删"}`); err != nil {
		t.Fatal(err)
	}
	var p model.Project
	if err := app.DB.Where("name = ?", "待删").First(&p).Error; err != nil {
		t.Fatal(err)
	}
	doc := model.Document{Title: "垃圾桶", Slug: "slugorg2", Type: model.DocTypeMarkdown, OwnerID: owner.ID, ProjectID: p.ID, Version: "v1.0.0"}
	if err := app.DB.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Delete(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&model.ProjectShare{ProjectID: p.ID, ShareToken: "toktoktoktoktoktoktoktoktoktoktok1"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&model.ShareAccessRequest{ProjectID: p.ID, RequestToken: "reqreqreqreqreqreqreqreqreqreqreq1"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := exec("delete_project", `{"id":`+id(p.ID)+`}`); err != nil {
		t.Fatal(err)
	}
	var left model.Document
	if err := app.DB.Unscoped().First(&left, doc.ID).Error; err != nil || left.ProjectID != 0 {
		t.Fatalf("trashed doc still grouped: %+v %v", left, err)
	}
	var n int64
	app.DB.Model(&model.ProjectShare{}).Where("project_id = ?", p.ID).Count(&n)
	if n != 0 {
		t.Fatalf("share left: %d", n)
	}
	app.DB.Model(&model.ShareAccessRequest{}).Where("project_id = ?", p.ID).Count(&n)
	if n != 0 {
		t.Fatalf("access request left: %d", n)
	}
}

func TestReadWebpageRejectsLoopback(t *testing.T) {
	app, owner, _ := newOrgApp(t)
	_, exec, _ := app.chatTools(owner, "")
	if _, err := exec("read_webpage", `{"url":"http://127.0.0.1/secret"}`); err == nil {
		t.Fatal("loopback must be rejected")
	}
	if _, err := exec("import_document_from_url", `{"url":"http://127.0.0.1/secret"}`); err == nil {
		t.Fatal("import must reject loopback")
	}
}
