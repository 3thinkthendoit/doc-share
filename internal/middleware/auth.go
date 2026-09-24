package middleware

import (
	"net/http"
	"strings"
	"time"

	"doc-share/internal/model"
	"doc-share/internal/session"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	CookieSession = "ds_session"
	ContextUser   = "currentUser"
)

// RequireAuth 校验登录凭证，注入当前用户；未登录时页面跳转登录、API 返回 401
func RequireAuth(db *gorm.DB, signer *session.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(CookieSession)
		if err != nil || token == "" {
			unauthorized(c)
			return
		}
		uid, err := signer.ParseUserToken(token)
		if err != nil {
			unauthorized(c)
			return
		}
		var user model.User
		if err := db.First(&user, uid).Error; err != nil {
			unauthorized(c)
			return
		}
		if user.Status != model.StatusEnabled {
			unauthorized(c)
			return
		}
		// 刷新最近活跃时间：限流为最多 5 分钟一次，避免每个请求都写库
		if user.LastActiveAt == nil || time.Since(*user.LastActiveAt) > 5*time.Minute {
			now := time.Now()
			db.Model(&user).UpdateColumn("last_active_at", now)
			user.LastActiveAt = &now
		}
		c.Set(ContextUser, &user)
		c.Next()
	}
}

// RequireAdmin 在登录基础上要求 admin 角色
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := CurrentUser(c)
		if user == nil || !user.IsAdmin() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "需要管理员权限"})
			return
		}
		c.Next()
	}
}

// OptionalAuth 公开页可选认证：有有效会话则注入当前用户（按登录态切换 UI），未登录直接放行
func OptionalAuth(db *gorm.DB, signer *session.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(CookieSession)
		if err != nil || token == "" {
			c.Next()
			return
		}
		uid, err := signer.ParseUserToken(token)
		if err != nil {
			c.Next()
			return
		}
		var user model.User
		if err := db.First(&user, uid).Error; err != nil || user.Status != model.StatusEnabled {
			c.Next()
			return
		}
		c.Set(ContextUser, &user)
		c.Next()
	}
}

// CurrentUser 从上下文读取当前登录用户
func CurrentUser(c *gin.Context) *model.User {
	v, ok := c.Get(ContextUser)
	if !ok {
		return nil
	}
	u, _ := v.(*model.User)
	return u
}

// unauthorized 页面请求重定向登录，API 请求返回 401
func unauthorized(c *gin.Context) {
	if isAPIORAjax(c) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录或登录已过期"})
		return
	}
	c.Redirect(http.StatusFound, "/login")
	c.Abort()
}

func isAPIORAjax(c *gin.Context) bool {
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/api/") || strings.Contains(path, "/api/") {
		return true
	}
	return c.GetHeader("X-Requested-With") == "XMLHttpRequest" ||
		c.GetHeader("Accept") == "application/json"
}
