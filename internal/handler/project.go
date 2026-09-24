package handler

import (
	"net/http"
	"strconv"
	"strings"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ProjectsPage 项目列表页：viewer 只看自己的，admin 看全部
func (a *App) ProjectsPage(c *gin.Context) {
	user := middleware.CurrentUser(c)
	tx := a.DB.Model(&model.Project{})
	if user != nil && !user.IsAdmin() {
		tx = tx.Where("owner_id = ?", user.ID)
	}
	var projects []model.Project
	tx.Preload("Owner").Order("updated_at desc").Find(&projects)

	// 回填每个项目的文档数（M-2：与列表可见范围一致，viewer 只算自己的文档）
	var counts []struct {
		K   uint
		Cnt int64
	}
	cntTx := a.DB.Model(&model.Document{}).Select("project_id AS k, COUNT(*) AS cnt")
	if user != nil && !user.IsAdmin() {
		cntTx = cntTx.Where("owner_id = ?", user.ID)
	}
	cntTx.Group("project_id").Scan(&counts)
	countMap := make(map[uint]int64, len(counts))
	for _, row := range counts {
		countMap[row.K] = row.Cnt
	}
	for i := range projects {
		projects[i].DocCount = countMap[projects[i].ID]
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目 id 非法"})
		return nil
	}
	var project model.Project
	if err := a.DB.First(&project, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return nil
	}
	if user := middleware.CurrentUser(c); user != nil && !user.IsAdmin() && project.OwnerID != user.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权操作他人的项目"})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": project})
}

// DeleteProject 删除项目；其下文档回到未分组（不级联删文档）
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
		return tx.Delete(&model.Project{}, project.ID).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
