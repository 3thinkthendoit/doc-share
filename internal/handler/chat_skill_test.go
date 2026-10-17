package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doc-share/internal/llm"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newSkillApp(t *testing.T) (*App, *model.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.ChatSkill{}, &model.ChatSession{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	u := model.User{Username: "skilluser", Nickname: "技能用户", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return &App{DB: db}, &u
}

func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		name string
		rest string
	}{
		{"/create-skill", true, "create-skill", ""},
		{"/create-skill 写周报模板", true, "create-skill", "写周报模板"},
		{"/creare-skill hi", true, "creare-skill", "hi"},
		{"/weekly-report 总结 id=3", true, "weekly-report", "总结 id=3"},
		{"hello /create-skill", false, "", ""},
		{"/Bad_Name", false, "", ""},
		{"", false, "", ""},
	}
	for _, tc := range cases {
		hit, ok := parseSlashCommand(tc.in)
		if ok != tc.ok {
			t.Fatalf("%q: ok=%v want %v", tc.in, ok, tc.ok)
		}
		if !ok {
			continue
		}
		if hit.Name != tc.name || hit.Rest != tc.rest {
			t.Fatalf("%q: got %+v want name=%s rest=%s", tc.in, hit, tc.name, tc.rest)
		}
	}
}

func TestValidateSkillFields(t *testing.T) {
	if err := validateSkillFields("code-review", "Review PRs. Use when reviewing.", "# Instructions\nBe strict."); err != nil {
		t.Fatal(err)
	}
	if err := validateSkillFields("Bad", "d", "c"); err == nil {
		t.Fatal("expected invalid name")
	}
	if err := validateSkillFields("ok", "", "c"); err == nil {
		t.Fatal("expected empty description")
	}
	if !isCreateSkillCommand("create-skill") || !isCreateSkillCommand("creare-skill") {
		t.Fatal("builtin alias")
	}
}

func TestNextCreatingSkill(t *testing.T) {
	cases := []struct {
		prev, create, slash bool
		next, cont          bool
	}{
		{false, true, true, true, false},   // 本轮 /create-skill
		{true, false, false, true, true},   // 多轮续写
		{true, false, true, false, false},  // 其它斜杠结束
		{false, false, false, false, false},
		{true, true, true, true, false}, // 再次 /create-skill
	}
	for i, tc := range cases {
		next, cont := nextCreatingSkill(tc.prev, tc.create, tc.slash)
		if next != tc.next || cont != tc.cont {
			t.Fatalf("#%d: got next=%v cont=%v want %v/%v", i, next, cont, tc.next, tc.cont)
		}
	}
}

func TestApplySlashSkillsCreateAndUser(t *testing.T) {
	a, u := newSkillApp(t)
	sk := model.ChatSkill{
		UserID: u.ID, Name: "weekly-report", Description: "写周报", Content: "## Do weekly report",
		DisableModelInvocation: true,
	}
	if err := a.DB.Create(&sk).Error; err != nil {
		t.Fatal(err)
	}

	extra, createMode, rest, handled := a.applySlashSkills(u.ID, "/create-skill 帮我做一个周报 skill")
	if !handled || !createMode || len(extra) == 0 {
		t.Fatalf("create-skill: handled=%v create=%v extra=%d", handled, createMode, len(extra))
	}
	if rest != "帮我做一个周报 skill" {
		t.Fatalf("rest=%q", rest)
	}
	if !strings.Contains(extra[0], "save_skill") || !strings.Contains(extra[0], skillBoundary) {
		t.Fatalf("create note missing save_skill/boundary")
	}

	// 空 rest：intentRest 必须为空，避免合成句触发文档意图
	extra, createMode, rest, handled = a.applySlashSkills(u.ID, "/create-skill")
	if !handled || !createMode || rest != "" {
		t.Fatalf("empty create rest: handled=%v create=%v rest=%q", handled, createMode, rest)
	}

	extra, createMode, rest, handled = a.applySlashSkills(u.ID, "/weekly-report 总结本周")
	if !handled || createMode || len(extra) == 0 {
		t.Fatalf("user skill: handled=%v create=%v", handled, createMode)
	}
	if rest != "总结本周" {
		t.Fatalf("rest=%q", rest)
	}
	if !strings.Contains(extra[0], "## Do weekly report") || !strings.Contains(extra[0], skillBoundary) {
		t.Fatalf("skill body/boundary not injected")
	}

	extra, _, rest, handled = a.applySlashSkills(u.ID, "/no-such-skill")
	if !handled || len(extra) == 0 || !strings.Contains(extra[0], "未知斜杠指令") || rest != "" {
		t.Fatalf("unknown slash should warn with empty rest")
	}

	_, _, _, handled = a.applySlashSkills(u.ID, "普通问题")
	if handled {
		t.Fatal("plain text should not be handled")
	}
}

func TestChatSkillCRUD(t *testing.T) {
	a, u := newSkillApp(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	asUser(c, u, http.MethodPost, "/console/api/chat/skills", `{
		"name":"code-review",
		"description":"Review code for quality. Use when reviewing PRs.",
		"content":"# Code Review\n## Checklist\n- correctness"
	}`)
	a.CreateChatSkill(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Skill model.ChatSkill `json:"skill"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Skill.ID == 0 {
		t.Fatalf("create parse: %v body=%s", err, w.Body.String())
	}

	// 列表不含 content
	wList := httptest.NewRecorder()
	cList, _ := gin.CreateTestContext(wList)
	asUser(cList, u, http.MethodGet, "/console/api/chat/skills", "")
	a.ListChatSkills(cList)
	if wList.Code != http.StatusOK {
		t.Fatalf("list status=%d", wList.Code)
	}
	if strings.Contains(wList.Body.String(), "Checklist") {
		t.Fatalf("list must omit content: %s", wList.Body.String())
	}

	// 内置名冲突
	wBad := httptest.NewRecorder()
	cBad, _ := gin.CreateTestContext(wBad)
	asUser(cBad, u, http.MethodPost, "/console/api/chat/skills", `{
		"name":"create-skill",
		"description":"should fail",
		"content":"# no"
	}`)
	a.CreateChatSkill(cBad)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("builtin name conflict want 400 got %d", wBad.Code)
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	asUser(c2, u, http.MethodGet, "/console/api/chat/commands", "")
	a.ListChatCommands(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("commands status=%d", w2.Code)
	}
	var cmds struct {
		Commands []struct {
			Name    string `json:"name"`
			Builtin bool   `json:"builtin"`
		} `json:"commands"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &cmds)
	hasBuiltin, hasUser := false, false
	for _, c := range cmds.Commands {
		if c.Name == "create-skill" && c.Builtin {
			hasBuiltin = true
		}
		if c.Name == "code-review" && !c.Builtin {
			hasUser = true
		}
	}
	if !hasBuiltin || !hasUser {
		t.Fatalf("commands missing builtin/user: %+v", cmds.Commands)
	}

	_, exec, _ := a.chatTools(u)
	out, err := exec("save_skill", `{
		"name":"code-review",
		"description":"Review code. Use when reviewing pull requests.",
		"content":"# Updated\nBe nicer.",
		"disable_model_invocation":false
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已更新") {
		t.Fatalf("expected update: %s", out)
	}
	cat := a.skillCatalogPrompt(u.ID)
	if !strings.Contains(cat, "/code-review") || !strings.Contains(cat, skillBoundary) {
		t.Fatalf("auto skill catalog: %q", cat)
	}
}

func TestChatSkillIsolationAndQuota(t *testing.T) {
	a, u := newSkillApp(t)
	other := model.User{Username: "other", Role: model.RoleViewer, Status: model.StatusEnabled}
	if err := a.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	sk := model.ChatSkill{UserID: other.ID, Name: "secret", Description: "other's skill", Content: "# secret"}
	if err := a.DB.Create(&sk).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	asUser(c, u, http.MethodGet, "/console/api/chat/skills/"+itoa(int(sk.ID)), "")
	a.GetChatSkill(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-user get want 404 got %d", w.Code)
	}

	_, exec, _ := a.chatTools(u)
	if _, err := exec("get_skill", `{"name":"secret"}`); err == nil {
		t.Fatal("get_skill must not read other user's skill")
	}

	// 配额：塞满后拒绝新建
	for i := 0; i < skillMaxPerUser; i++ {
		s := model.ChatSkill{
			UserID: u.ID, Name: fmt.Sprintf("s-%d", i),
			Description: "d", Content: "c",
		}
		if err := a.DB.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
	}
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	asUser(c2, u, http.MethodPost, "/console/api/chat/skills", `{
		"name":"overflow",
		"description":"too many",
		"content":"# x"
	}`)
	a.CreateChatSkill(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("quota want 400 got %d body=%s", w2.Code, w2.Body.String())
	}
	if _, err := exec("save_skill", `{"name":"overflow","description":"too many","content":"# x"}`); err == nil {
		t.Fatal("save_skill should hit quota")
	}
	// 更新已有不占新配额
	if _, err := exec("save_skill", `{"name":"s-0","description":"updated d","content":"# updated"}`); err != nil {
		t.Fatalf("update existing should work: %v", err)
	}
}

func TestDeleteSkillNeedsApproval(t *testing.T) {
	tc := llm.ToolCallMsg{}
	tc.Function.Name = "delete_skill"
	tc.Function.Arguments = `{"name":"x"}`
	if !aiNeedsApproval(tc) {
		t.Fatal("delete_skill must require approval")
	}
}
