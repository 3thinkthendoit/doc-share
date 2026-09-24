package handler

import (
	"net/http"
	"time"

	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	CookieShare    = "ds_share"
	shareVerifyTTL = 24 * 3600 // 分享验证凭证有效期（秒）
)

// ShareView 分享阅读入口：GET /s/:token
func (a *App) ShareView(c *gin.Context) {
	token := c.Param("token")
	doc, share := a.loadShare(c, token)
	if doc == nil {
		return
	}

	// 无密码直接渲染
	if !share.HasPassword() {
		a.serveDoc(c, doc)
		return
	}

	// 有密码：校验已签发的免密凭证
	if cred, err := c.Cookie(CookieShare); err == nil && a.Signer.VerifyShareToken(cred, token) {
		a.serveDoc(c, doc)
		return
	}
	a.render(c, "share_password.html", gin.H{
		"title":    doc.Title,
		"rawTitle": true, // 用户文档标题，跳过词典反查避免误译
		"token":    token,
		"error":    "",
	})
}

// ShareSubmit 分享密码提交：POST /s/:token
func (a *App) ShareSubmit(c *gin.Context) {
	token := c.Param("token")
	doc, share := a.loadShare(c, token)
	if doc == nil {
		return
	}
	if !share.HasPassword() {
		a.serveDoc(c, doc)
		return
	}

	ip := c.ClientIP()
	if !a.Limiter.Allowed(ip, token) {
		a.render(c, "share_password.html", gin.H{
			"title":    doc.Title,
			"rawTitle": true,
			"token":    token,
			"error":    "尝试次数过多，请稍后再试",
		})
		return
	}

	password := c.PostForm("password")
	if bcrypt.CompareHashAndPassword([]byte(share.Password), []byte(password)) != nil {
		a.Limiter.Fail(ip, token)
		a.render(c, "share_password.html", gin.H{
			"title":    doc.Title,
			"rawTitle": true,
			"token":    token,
			"error":    "密码错误",
		})
		return
	}

	a.Limiter.Reset(ip, token)
	cred := a.Signer.MakeShareToken(token, shareVerifyTTL*time.Second)
	setCookie(c, CookieShare, cred, shareVerifyTTL)
	a.serveDoc(c, doc)
}

// loadShare 加载分享与文档，处理不存在/过期等情况并写响应
func (a *App) loadShare(c *gin.Context, token string) (*model.Document, *model.Share) {
	var share model.Share
	if err := a.DB.Where("share_token = ?", token).First(&share).Error; err != nil {
		c.String(http.StatusNotFound, "分享链接不存在或已取消")
		return nil, nil
	}
	if share.IsExpired() {
		c.String(http.StatusGone, "分享链接已过期")
		return nil, nil
	}
	var doc model.Document
	// Preload Owner：阅读页展示作者头像与昵称
	if err := a.DB.Preload("Owner").First(&doc, share.DocumentID).Error; err != nil {
		c.String(http.StatusNotFound, "文档不存在")
		return nil, nil
	}
	return &doc, &share
}

// serveDoc 渲染文档阅读页并累加浏览量
func (a *App) serveDoc(c *gin.Context, doc *model.Document) {
	a.DB.Model(&model.Document{}).Where("id = ?", doc.ID).
		UpdateColumn("view_count", gorm.Expr("view_count + 1"))
	doc.ViewCount++
	a.render(c, "share_view.html", gin.H{
		"title":    doc.Title,
		"rawTitle": true, // 用户文档标题，跳过词典反查避免误译
		"doc":      doc,
	})
}
