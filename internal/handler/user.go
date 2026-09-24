package handler

import (
	"net/http"
	"strconv"
	"strings"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// UsersPage 用户管理页（仅 admin）
func (a *App) UsersPage(c *gin.Context) {
	var users []model.User
	a.DB.Order("id asc").Find(&users)
	a.render(c, "users.html", gin.H{
		"title": "用户管理",
		"users": users,
	})
}

type userReq struct {
	Username string  `form:"username" json:"username"`
	Nickname string  `form:"nickname" json:"nickname"`
	Avatar   *string `form:"avatar" json:"avatar"` // 指针：区分“未传”与“传空清空”
	Role     string  `form:"role" json:"role"`
	Status   *int    `form:"status" json:"status"` // 指针：区分“未传”与“传 0”，避免只改密码时误禁用
	Password string  `form:"password" json:"password"`
}

// validAvatar 头像 URL 长度校验
func validAvatar(avatar string) bool {
	return len([]rune(strings.TrimSpace(avatar))) <= 500
}

// CreateUser 新建用户
func (a *App) CreateUser(c *gin.Context) {
	var req userReq
	if err := c.ShouldBind(&req); err != nil || req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名和密码不能为空"})
		return
	}
	if len(req.Password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码至少 6 位"})
		return
	}
	if req.Role != model.RoleAdmin && req.Role != model.RoleViewer {
		req.Role = model.RoleViewer
	}
	var exists int64
	a.DB.Model(&model.User{}).Where("username = ?", req.Username).Count(&exists)
	if exists > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名已存在"})
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	user := model.User{
		Username:     req.Username,
		Nickname:     req.Nickname,
		Role:         req.Role,
		Status:       model.StatusEnabled,
		PasswordHash: string(hash),
	}
	if req.Avatar != nil {
		if !validAvatar(*req.Avatar) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "头像地址过长"})
			return
		}
		user.Avatar = strings.TrimSpace(*req.Avatar)
	}
	if req.Status != nil && *req.Status == model.StatusDisabled {
		user.Status = model.StatusDisabled
	}
	if err := a.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": user})
}

// UpdateUser 修改用户资料 / 角色 / 状态 / 重置密码
func (a *App) UpdateUser(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var user model.User
	if err := a.DB.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	var req userReq
	_ = c.ShouldBind(&req)

	if req.Nickname != "" {
		user.Nickname = req.Nickname
	}
	if req.Avatar != nil {
		if !validAvatar(*req.Avatar) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "头像地址过长"})
			return
		}
		user.Avatar = strings.TrimSpace(*req.Avatar)
	}
	if req.Role == model.RoleAdmin || req.Role == model.RoleViewer {
		user.Role = req.Role
	}
	if req.Status != nil && (*req.Status == model.StatusEnabled || *req.Status == model.StatusDisabled) {
		user.Status = *req.Status
	}
	if req.Password != "" {
		if len(req.Password) < 6 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "密码至少 6 位"})
			return
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		user.PasswordHash = string(hash)
	}
	if err := a.DB.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": user})
}

// DeleteUser 删除用户（禁止删除自己）；事务内级联清理其文档、分享链接与项目，
// 避免孤儿分享 token 仍可匿名访问（M1）
func (a *App) DeleteUser(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	current := middleware.CurrentUser(c)
	if current != nil && current.ID == uint(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除当前登录账号"})
		return
	}
	var exists int64
	a.DB.Model(&model.User{}).Where("id = ?", id).Count(&exists)
	if exists == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("document_id IN (SELECT id FROM documents WHERE owner_id = ?)", id).
			Delete(&model.Share{}).Error; err != nil {
			return err
		}
		if err := tx.Where("owner_id = ?", id).Delete(&model.Document{}).Error; err != nil {
			return err
		}
		if err := tx.Where("owner_id = ?", id).Delete(&model.Project{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.User{}, id).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// UpdateProfile 个人信息自助修改：昵称 + 头像（仅本人，任意登录用户）
func (a *App) UpdateProfile(c *gin.Context) {
	var req struct {
		Nickname string `json:"nickname"`
		Avatar   string `json:"avatar"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	nickname := strings.TrimSpace(req.Nickname)
	avatar := strings.TrimSpace(req.Avatar)
	if nickname == "" || len([]rune(nickname)) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "昵称不能为空且不超过 64 字"})
		return
	}
	if !validAvatar(avatar) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "头像地址过长"})
		return
	}
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	if err := a.DB.Model(user).UpdateColumns(map[string]interface{}{
		"nickname": nickname,
		"avatar":   avatar,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
