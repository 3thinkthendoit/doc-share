package handler

import (
	"net/http"
	"strconv"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

// ListMessages 当前用户消息列表：GET /admin/api/messages?unread=1&limit=30
func (a *App) ListMessages(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	q := a.DB.Model(&model.Message{}).Where("user_id = ?", user.ID)
	if c.Query("unread") == "1" {
		q = q.Where("read_at IS NULL")
	}
	var items []model.Message
	q.Order("id desc").Limit(limit).Find(&items)
	if items == nil {
		items = []model.Message{}
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// UnreadMessageCount 未读数：GET /admin/api/messages/unread-count
func (a *App) UnreadMessageCount(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	var n int64
	a.DB.Model(&model.Message{}).Where("user_id = ? AND read_at IS NULL", user.ID).Count(&n)
	c.JSON(http.StatusOK, gin.H{"count": n})
}

// MarkMessageRead 标记单条已读：POST /admin/api/messages/:id/read
func (a *App) MarkMessageRead(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}
	now := time.Now()
	res := a.DB.Model(&model.Message{}).
		Where("id = ? AND user_id = ? AND read_at IS NULL", id, user.ID).
		Update("read_at", now)
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// MarkAllMessagesRead 全部已读：POST /admin/api/messages/read-all
func (a *App) MarkAllMessagesRead(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	now := time.Now()
	res := a.DB.Model(&model.Message{}).
		Where("user_id = ? AND read_at IS NULL", user.ID).
		Update("read_at", now)
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
