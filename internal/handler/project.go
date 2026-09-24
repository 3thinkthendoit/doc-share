package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ProjectsPage 项目列表页：自己的项目 + 所参与的项目（带角色标签），admin 看全部
func (a *App) ProjectsPage(c *gin.Context) {
	user := middleware.CurrentUser(c)

	// 参与的项目及角色（admin 天然全可见，无需回填角色）
	roleByPid := make(map[uint]string)
	if user != nil && !user.IsAdmin() {
		var ms []model.ProjectMember
		a.DB.Where("user_id = ?", user.ID).Find(&ms)
		for _, m := range ms {
			roleByPid[m.ProjectID] = m.Role
		}
	}

	tx := a.DB.Model(&model.Project{})
	if user != nil && !user.IsAdmin() {
		if len(roleByPid) > 0 {
			pids := make([]uint, 0, len(roleByPid))
			for pid := range roleByPid {
				pids = append(pids, pid)
			}
			tx = tx.Where("owner_id = ? OR id IN ?", user.ID, pids)
		} else {
			tx = tx.Where("owner_id = ?", user.ID)
		}
	}
	var projects []model.Project
	tx.Preload("Owner").Order("updated_at desc").Find(&projects)

	// 回填每个项目的文档数与成员角色（与文档列表可见范围一致：自己的 ∪ 参与项目内的）
	var counts []struct {
		K   uint
		Cnt int64
	}
	cntTx := a.DB.Model(&model.Document{}).Select("project_id AS k, COUNT(*) AS cnt")
	if user != nil && !user.IsAdmin() {
		if len(roleByPid) > 0 {
			pids := make([]uint, 0, len(roleByPid))
			for pid := range roleByPid {
				pids = append(pids, pid)
			}
			cntTx = cntTx.Where("owner_id = ? OR project_id IN ?", user.ID, pids)
		} else {
			cntTx = cntTx.Where("owner_id = ?", user.ID)
		}
	}
	cntTx.Group("project_id").Scan(&counts)
	countMap := make(map[uint]int64, len(counts))
	for _, row := range counts {
		countMap[row.K] = row.Cnt
	}
	// 成员数统计
	var mcounts []struct {
		K   uint
		Cnt int64
	}
	a.DB.Model(&model.ProjectMember{}).Select("project_id AS k, COUNT(*) AS cnt").Group("project_id").Scan(&mcounts)
	memberMap := make(map[uint]int64, len(mcounts))
	for _, row := range mcounts {
		memberMap[row.K] = row.Cnt
	}
	for i := range projects {
		projects[i].DocCount = countMap[projects[i].ID]
		projects[i].MemberCount = memberMap[projects[i].ID]
		projects[i].Role = roleByPid[projects[i].ID]
	}

	a.render(c, "projects.html", gin.H{
		"title":    "项目管理",
		"projects": projects,
	})
}

type projectReq struct {
	Name        string `json:"name" form:"name"`
	Description string `json:"description" form:"description"`
}

// loadProject 按 id 加载项目并做所有权校验
func (a *App) loadProject(c *gin.Context) *model.Project {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": errProjIDBad})
		return nil
	}
	var project model.Project
	if err := a.DB.First(&project, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": errProjNotFound})
		return nil
	}
	if user := middleware.CurrentUser(c); user != nil && !user.IsAdmin() && project.OwnerID != user.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": errProjForbidden})
		return nil
	}
	return &project
}

// checkProjectReq 名称/描述长度校验（与 DB 列宽对齐，rune 计数）
func checkProjectReq(req *projectReq) (string, string, string) {
	name := strings.TrimSpace(req.Name)
	desc := strings.TrimSpace(req.Description)
	if name == "" {
		return "", "", "项目名称不能为空"
	}
	if len([]rune(name)) > 100 {
		return "", "", "项目名称不能超过 100 字"
	}
	if len([]rune(desc)) > 500 {
		return "", "", "项目描述不能超过 500 字"
	}
	return name, desc, ""
}

// CreateProject 新建项目
func (a *App) CreateProject(c *gin.Context) {
	var req projectReq
	name, desc, errMsg := "", "", ""
	if err := c.ShouldBindJSON(&req); err != nil {
		name, desc, errMsg = "", "", "参数错误"
	} else {
		name, desc, errMsg = checkProjectReq(&req)
	}
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		return
	}
	user := middleware.CurrentUser(c)
	project := model.Project{
		Name:        name,
		Description: desc,
		OwnerID:     user.ID,
	}
	if err := a.DB.Create(&project).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errCreateFail})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": project})
}

// UpdateProject 修改项目
func (a *App) UpdateProject(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
		return
	}
	var req projectReq
	name, desc, errMsg := "", "", ""
	if err := c.ShouldBindJSON(&req); err != nil {
		name, desc, errMsg = "", "", "参数错误"
	} else {
		name, desc, errMsg = checkProjectReq(&req)
	}
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		return
	}
	project.Name = name
	project.Description = desc
	if err := a.DB.Save(project).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errUpdateFail})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": project})
}

// DeleteProject 删除项目；其下文档回到未分组（不级联删文档），成员关系一并清理
func (a *App) DeleteProject(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Document{}).Where("project_id = ?", project.ID).
			UpdateColumn("project_id", 0).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id = ?", project.ID).Delete(&model.ProjectMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Project{}, project.ID).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errDeleteFail})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// UserOptions 成员选择器搜索：按用户名/昵称模糊匹配启用用户。
// 任意登录用户可用，仅暴露 id/用户名/昵称；必须带搜索词且一次最多 20 条，避免全量枚举账号
func (a *App) UserOptions(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	out := []gin.H{}
	if q != "" {
		like := "%" + q + "%"
		var us []model.User
		a.DB.Where("status = ? AND (username LIKE ? OR nickname LIKE ?)", model.StatusEnabled, like, like).
			Order("username asc").Limit(20).Find(&us)
		for _, u := range us {
			out = append(out, gin.H{"id": u.ID, "username": u.Username, "nickname": u.DisplayName()})
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// ---- 项目成员 ----

type memberView struct {
	ID       uint   `json:"id"` // 成员记录 ID
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Role     string `json:"role"`
}

// ListProjectMembers 成员列表（属主/管理员）
func (a *App) ListProjectMembers(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
		return
	}
	var ms []model.ProjectMember
	a.DB.Where("project_id = ?", project.ID).Order("created_at asc").Find(&ms)

	ids := make([]uint, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.UserID)
	}
	users := map[uint]model.User{}
	if len(ids) > 0 {
		var us []model.User
		a.DB.Where("id IN ?", ids).Find(&us)
		for _, u := range us {
			users[u.ID] = u
		}
	}
	views := make([]memberView, 0, len(ms))
	for _, m := range ms {
		v := memberView{ID: m.ID, UserID: m.UserID, Role: m.Role}
		if u, ok := users[m.UserID]; ok {
			v.Username, v.Nickname = u.Username, u.DisplayName()
		}
		views = append(views, v)
	}
	c.JSON(http.StatusOK, gin.H{"members": views})
}

// AddProjectMember 添加成员 {user_id, role}
func (a *App) AddProjectMember(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
		return
	}
	var req struct {
		UserID uint   `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	if req.Role != model.MemberRoleView && req.Role != model.MemberRoleEdit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "角色非法"})
		return
	}
	var u model.User
	if err := a.DB.Where("id = ? AND status = ?", req.UserID, model.StatusEnabled).First(&u).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户不存在或已禁用"})
		return
	}
	if u.ID == project.OwnerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目所有者无需添加为成员"})
		return
	}
	m := model.ProjectMember{ProjectID: project.ID, UserID: req.UserID, Role: req.Role}
	if err := a.DB.Create(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "该用户已是项目成员"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": errCreateFail})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": m.ID, "user_id": m.UserID, "username": u.Username, "nickname": u.DisplayName(), "role": m.Role}})
}

// loadMember 加载本项目下的成员记录，找不到写 404
func (a *App) loadMember(c *gin.Context, projectID uint) *model.ProjectMember {
	mid, _ := strconv.Atoi(c.Param("mid"))
	var m model.ProjectMember
	if mid <= 0 || a.DB.Where("id = ? AND project_id = ?", mid, projectID).First(&m).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "成员不存在"})
		return nil
	}
	return &m
}

// UpdateProjectMember 修改成员角色 {role}
func (a *App) UpdateProjectMember(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
		return
	}
	m := a.loadMember(c, project.ID)
	if m == nil {
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := c.ShouldBind(&req); err != nil || (req.Role != model.MemberRoleView && req.Role != model.MemberRoleEdit) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "角色非法"})
		return
	}
	m.Role = req.Role
	if err := a.DB.Model(m).Update("role", m.Role).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errUpdateFail})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RemoveProjectMember 移除成员
func (a *App) RemoveProjectMember(c *gin.Context) {
	project := a.loadProject(c)
	if project == nil {
		return
	}
	m := a.loadMember(c, project.ID)
	if m == nil {
		return
	}
	if err := a.DB.Delete(m).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errDeleteFail})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
