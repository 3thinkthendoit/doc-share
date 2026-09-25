package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// CategoriesPage 分类管理页：viewer 只看自己的，admin 看全部
func (a *App) CategoriesPage(c *gin.Context) {
	user := middleware.CurrentUser(c)
	q := strings.TrimSpace(c.Query("q"))
	pg := parseWebPage(c)
	tx := a.DB.Model(&model.Category{})
	if user != nil && !user.IsAdmin() {
		tx = tx.Where("owner_id = ?", user.ID)
	}
	if q != "" {
		tx = tx.Where("name LIKE ?", "%"+q+"%")
	}
	var total int64
	tx.Count(&total)
	pg = pg.withTotal(total)

	var categories []model.Category
	tx.Preload("Owner").Order("sort asc, id asc").Offset(pg.Offset).Limit(pg.Size).Find(&categories)

	// 回填当前页分类的文档数（与列表可见范围一致，viewer 只算自己的文档）
	pageIDs := make([]uint, len(categories))
	for i := range categories {
		pageIDs[i] = categories[i].ID
	}
	countMap := make(map[uint]int64, len(pageIDs))
	if len(pageIDs) > 0 {
		var counts []struct {
			K   uint
			Cnt int64
		}
		cntTx := a.DB.Model(&model.Document{}).Select("category_id AS k, COUNT(*) AS cnt").
			Where("category_id IN ?", pageIDs)
		if user != nil && !user.IsAdmin() {
			cntTx = cntTx.Where("owner_id = ?", user.ID)
		}
		cntTx.Group("category_id").Scan(&counts)
		for _, row := range counts {
			countMap[row.K] = row.Cnt
		}
	}
	for i := range categories {
		categories[i].DocCount = countMap[categories[i].ID]
	}

	extra := ""
	if q != "" {
		extra += "&q=" + url.QueryEscape(q)
	}
	data := gin.H{
		"title":      "分类管理",
		"categories": categories,
		"q":          q,
	}
	for k, v := range pagerFields(pg, "/admin/categories", extra) {
		data[k] = v
	}
	a.render(c, "categories.html", data)
}

type categoryReq struct {
	Name string `json:"name" form:"name"`
	Sort int    `json:"sort" form:"sort"`
}

// checkCategoryReq 分类名称长度校验（与 DB 列宽对齐）
func checkCategoryReq(name string) (string, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "分类名称不能为空"
	}
	if len([]rune(name)) > 50 {
		return "", "分类名称不能超过 50 字"
	}
	return name, ""
}

// loadCategory 按 id 加载分类并做所有权校验（与 loadProject 同构）
func (a *App) loadCategory(c *gin.Context) *model.Category {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCatIDBad})
		return nil
	}
	var category model.Category
	if err := a.DB.First(&category, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": errCatNotFound})
		return nil
	}
	if user := middleware.CurrentUser(c); user != nil && !user.IsAdmin() && category.OwnerID != user.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": errCatForbidden})
		return nil
	}
	return &category
}

// CreateCategory 新建分类（归属当前用户）
func (a *App) CreateCategory(c *gin.Context) {
	var req categoryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	name, errMsg := checkCategoryReq(req.Name)
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		return
	}
	user := middleware.CurrentUser(c)
	category := model.Category{Name: name, Sort: req.Sort, OwnerID: user.ID}
	if err := a.DB.Create(&category).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCatDupCreate})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": category})
}

// UpdateCategory 修改分类
func (a *App) UpdateCategory(c *gin.Context) {
	category := a.loadCategory(c)
	if category == nil {
		return
	}
	var req categoryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	name, errMsg := checkCategoryReq(req.Name)
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		return
	}
	category.Name = name
	category.Sort = req.Sort
	if err := a.DB.Save(category).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCatDupUpdate})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": category})
}

// DeleteCategory 删除分类；其下文档回到未分类
func (a *App) DeleteCategory(c *gin.Context) {
	category := a.loadCategory(c)
	if category == nil {
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Document{}).Where("category_id = ?", category.ID).
			UpdateColumn("category_id", 0).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Category{}, category.ID).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errDeleteFail})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
