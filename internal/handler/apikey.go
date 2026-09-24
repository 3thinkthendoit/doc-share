package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

// APIKeysPage API 密钥管理页：viewer 只看自己的，admin 看全部
func (a *App) APIKeysPage(c *gin.Context) {
	user := middleware.CurrentUser(c)
	tx := a.DB.Model(&model.ApiKey{})
	if user != nil && !user.IsAdmin() {
		tx = tx.Where("owner_id = ?", user.ID)
	}
	var keys []model.ApiKey
	tx.Preload("Owner").Order("created_at desc").Find(&keys)
	a.render(c, "apikeys.html", gin.H{
		"title": "API 密钥",
		"keys":  keys,
	})
}

// randHex 生成 n 字节随机数的 hex 串
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// loadApiKey 按 id 加载密钥并做所有权校验（与 loadCategory 同构）
func (a *App) loadApiKey(c *gin.Context) *model.ApiKey {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密钥 id 非法"})
		return nil
	}
	var key model.ApiKey
	if err := a.DB.First(&key, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "密钥不存在"})
		return nil
	}
	if user := middleware.CurrentUser(c); user != nil && !user.IsAdmin() && key.OwnerID != user.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权操作他人的密钥"})
		return nil
	}
	return &key
}

// CreateAPIKey 新建密钥；secret 仅在本次响应返回一次，之后不可再查
func (a *App) CreateAPIKey(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "名称不能为空且不超过 100 字"})
		return
	}
	user := middleware.CurrentUser(c)
	key := model.ApiKey{
		Name:    name,
		AppKey:  randHex(16),
		Secret:  randHex(32),
		OwnerID: user.ID,
		Status:  model.StatusEnabled,
	}
	if key.AppKey == "" || key.Secret == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成密钥失败"})
		return
	}
	if err := a.DB.Create(&key).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": key, "secret": key.Secret})
}

// ResetAPIKeySecret 重置密钥；新 secret 仅在本次响应返回一次
func (a *App) ResetAPIKeySecret(c *gin.Context) {
	key := a.loadApiKey(c)
	if key == nil {
		return
	}
	key.Secret = randHex(32)
	if key.Secret == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成密钥失败"})
		return
	}
	if err := a.DB.Model(key).UpdateColumn("secret", key.Secret).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "重置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"secret": key.Secret})
}

// UpdateAPIKeyStatus 启用/禁用密钥
func (a *App) UpdateAPIKeyStatus(c *gin.Context) {
	key := a.loadApiKey(c)
	if key == nil {
		return
	}
	var req struct {
		Status *int `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Status == nil ||
		(*req.Status != model.StatusEnabled && *req.Status != model.StatusDisabled) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status 需为 0 或 1"})
		return
	}
	if err := a.DB.Model(key).UpdateColumn("status", *req.Status).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DeleteAPIKey 删除密钥
func (a *App) DeleteAPIKey(c *gin.Context) {
	key := a.loadApiKey(c)
	if key == nil {
		return
	}
	if err := a.DB.Delete(key).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
