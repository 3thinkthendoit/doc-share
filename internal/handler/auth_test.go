package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"doc-share/internal/config"
	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newAuthApp 搭建授权矩阵测试环境：内存 sqlite + 种子数据（属主/查看成员/编辑成员/admin）
func newAuthApp(t *testing.T) (*App, *model.Document) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.ProjectMember{}, &model.Document{}, &model.Share{}, &model.DocumentRevision{}, &model.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	owner := model.User{Username: "owner", Nickname: "属主", Role: model.RoleViewer, Status: model.StatusEnabled}
	viewer := model.User{Username: "viewm", Nickname: "只读", Role: model.RoleViewer, Status: model.StatusEnabled}
	editor := model.User{Username: "editm", Nickname: "编辑", Role: model.RoleViewer, Status: model.StatusEnabled}
	admin := model.User{Username: "boss", Nickname: "管理员", Role: model.RoleAdmin, Status: model.StatusEnabled}
	for _, u := range []*model.User{&owner, &viewer, &editor, &admin} {
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	project := model.Project{Name: "协作项目", OwnerID: owner.ID}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Create(&model.ProjectMember{ProjectID: project.ID, UserID: viewer.ID, Role: model.MemberRoleView}).Error; err != nil {
		t.Fatalf("seed view member: %v", err)
	}
	if err := db.Create(&model.ProjectMember{ProjectID: project.ID, UserID: editor.ID, Role: model.MemberRoleEdit}).Error; err != nil {
		t.Fatalf("seed edit member: %v", err)
	}
	doc := model.Document{Title: "共享文档", Slug: "abcdef12", Content: "hello", OwnerID: owner.ID, ProjectID: project.ID}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	return &App{DB: db}, &doc
}

// asUser 构造带登录态与 JSON 请求的 gin 上下文
func asUser(c *gin.Context, u *model.User, method, path, body string) {
	if body == "" {
		body = "{}"
	}
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	c.Set(middleware.ContextUser, u)
}

// TestProjectMemberAuthMatrix 授权矩阵：成员按角色访问挂项目文档
func TestProjectMemberAuthMatrix(t *testing.T) {
	a, doc := newAuthApp(t)
	docID := itoa(int(doc.ID))

	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}

	t.Run("view_member_cannot_update", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: docID}}
		asUser(c, users["viewm"], http.MethodPut, "/admin/api/docs/"+docID, `{"title":"x"}`)
		a.UpdateDoc(c)
		assertCode(t, w, http.StatusForbidden)
	})

	t.Run("edit_member_can_update", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: docID}}
		asUser(c, users["editm"], http.MethodPut, "/admin/api/docs/"+docID, `{"title":"新标题"}`)
		a.UpdateDoc(c)
		assertCode(t, w, http.StatusOK)
	})

	t.Run("edit_member_cannot_upsert_share", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: docID}}
		asUser(c, users["editm"], http.MethodPost, "/admin/api/docs/"+docID+"/share", `{"enabled":true}`)
		a.UpsertShare(c)
		assertCode(t, w, http.StatusForbidden)
	})

	t.Run("view_member_can_list_revisions", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: docID}}
		asUser(c, users["viewm"], http.MethodGet, "/admin/api/docs/"+docID+"/revisions", "")
		a.ListRevisions(c)
		assertCode(t, w, http.StatusOK)
	})

	t.Run("outsider_cannot_even_read", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: docID}}
		asUser(c, users["outsider"], http.MethodGet, "/admin/api/docs/"+docID+"/revisions", "")
		a.ListRevisions(c)
		assertCode(t, w, http.StatusForbidden)
	})

	t.Run("owner_can_upsert_share", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: docID}}
		asUser(c, users["owner"], http.MethodPost, "/admin/api/docs/"+docID+"/share", `{"enabled":true}`)
		a.UpsertShare(c)
		assertCode(t, w, http.StatusOK)
	})

	t.Run("admin_bypasses_everything", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: docID}}
		asUser(c, users["boss"], http.MethodPost, "/admin/api/docs/"+docID+"/share", `{"enabled":true}`)
		a.UpsertShare(c)
		assertCode(t, w, http.StatusOK)
	})

	t.Run("add_duplicate_member_reports_conflict", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: itoa(int(projectIDFromDoc(t, a, doc)))}}
		asUser(c, users["owner"], http.MethodPost, "/admin/api/projects/1/members", `{"user_id":2,"role":"view"}`)
		a.AddProjectMember(c)
		assertCode(t, w, http.StatusBadRequest)
	})
}

// TestProjectDocsVisibility 文档列表可见性：成员经「项目共享」筛选看到项目内他人文档，看不到局外人私有文档
func TestProjectDocsVisibility(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	// 局外人的私有文档（未挂项目）
	private := model.Document{Title: "局外人私有的文档", Slug: "private1", OwnerID: users["outsider"].ID}
	if err := a.DB.Create(&private).Error; err != nil {
		t.Fatalf("seed private doc: %v", err)
	}

	render := newRenderApp(t, a.DB)

	t.Run("view_member_shared_filter_sees_project_docs", func(t *testing.T) {
		w, c := newCtx()
		asUser(c, users["viewm"], http.MethodGet, "/admin/docs?origin=shared", "")
		render.DocsPage(c)
		assertCode(t, w, http.StatusOK)
		body := w.Body.String()
		if !strings.Contains(body, doc.Title) {
			t.Fatalf("应看到项目内他人文档 %q", doc.Title)
		}
	})

	t.Run("view_member_cannot_see_private_docs_of_others", func(t *testing.T) {
		w, c := newCtx()
		asUser(c, users["viewm"], http.MethodGet, "/admin/docs", "")
		render.DocsPage(c)
		assertCode(t, w, http.StatusOK)
		if strings.Contains(w.Body.String(), "局外人私有的文档") {
			t.Fatalf("不应看到局外人的私有文档")
		}
	})
}

// TestProjectMemberCRUD 成员管理闭环：添加 → 列表 → 改角色 → 移除 → 列表为空
func TestProjectMemberCRUD(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	pid := int(projectIDFromDoc(t, a, doc))
	pseg := itoa(pid)

	t.Run("add_member", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: pseg}}
		asUser(c, users["owner"], http.MethodPost, "/admin/api/projects/"+pseg+"/members",
			`{"user_id":`+itoa(int(users["outsider"].ID))+`,"role":"edit"}`)
		a.AddProjectMember(c)
		assertCode(t, w, http.StatusOK)
	})

	var mid string
	t.Run("list_members", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: pseg}}
		asUser(c, users["owner"], http.MethodGet, "/admin/api/projects/"+pseg+"/members", "")
		a.ListProjectMembers(c)
		assertCode(t, w, http.StatusOK)
		var res struct {
			Members []struct {
				ID       uint   `json:"id"`
				Username string `json:"username"`
				Role     string `json:"role"`
			} `json:"members"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		var found bool
		for _, m := range res.Members {
			if m.Username == "outsider" {
				found = true
				if m.Role != model.MemberRoleEdit {
					t.Fatalf("新成员角色 = %q, want edit", m.Role)
				}
				mid = itoa(int(m.ID))
			}
		}
		if !found {
			t.Fatalf("成员列表缺少 outsider: %s", w.Body.String())
		}
	})

	t.Run("non_owner_cannot_manage", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: pseg}, {Key: "mid", Value: mid}}
		asUser(c, users["editm"], http.MethodDelete, "/admin/api/projects/"+pseg+"/members/"+mid, "")
		a.RemoveProjectMember(c)
		assertCode(t, w, http.StatusForbidden)
	})

	t.Run("update_role", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: pseg}, {Key: "mid", Value: mid}}
		asUser(c, users["owner"], http.MethodPut, "/admin/api/projects/"+pseg+"/members/"+mid, `{"role":"view"}`)
		a.UpdateProjectMember(c)
		assertCode(t, w, http.StatusOK)
	})

	t.Run("remove_member", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: pseg}, {Key: "mid", Value: mid}}
		asUser(c, users["owner"], http.MethodDelete, "/admin/api/projects/"+pseg+"/members/"+mid, "")
		a.RemoveProjectMember(c)
		assertCode(t, w, http.StatusOK)
	})

	t.Run("removed_member_cannot_read_docs", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(doc.ID))}}
		asUser(c, users["outsider"], http.MethodGet, "/admin/api/docs/"+itoa(int(doc.ID))+"/revisions", "")
		a.ListRevisions(c)
		assertCode(t, w, http.StatusForbidden)
	})
}

// ---- 测试辅助 ----

// newRenderApp 带模板与词典的 App（供页面渲染类 handler 使用）
func newRenderApp(t *testing.T, db *gorm.DB) *App {
	t.Helper()
	bundle := newBundle(t)
	tmpl, err := ParseTemplates(os.DirFS("../../web/templates"))
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}
	return &App{DB: db, I18N: bundle, Tmpl: tmpl, Cfg: &config.Config{}}
}

func newCtx() (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	return w, c
}

func assertCode(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, want, w.Body.String())
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func loadTestUsers(a *App) (map[string]*model.User, error) {
	var us []model.User
	if err := a.DB.Order("id asc").Find(&us).Error; err != nil {
		return nil, err
	}
	// 再补一个未参与任何项目的局外人
	out := model.User{Username: "outsider", Nickname: "路人", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := a.DB.Create(&out).Error; err != nil {
		return nil, err
	}
	us = append(us, out)
	m := make(map[string]*model.User, len(us))
	for i := range us {
		m[us[i].Username] = &us[i]
	}
	return m, nil
}

func projectIDFromDoc(t *testing.T, a *App, doc *model.Document) uint {
	t.Helper()
	var d model.Document
	if err := a.DB.First(&d, doc.ID).Error; err != nil {
		t.Fatalf("reload doc: %v", err)
	}
	return d.ProjectID
}
