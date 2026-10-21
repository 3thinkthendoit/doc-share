package handler

import (
	"github.com/gin-gonic/gin"
)

// TermsPage 用户协议（公开静态页）
func (a *App) TermsPage(c *gin.Context) {
	a.render(c, "terms.html", gin.H{
		"title":     a.tr(c, "legal.termsTitle"),
		"titleSolo": true,
	})
}

// PrivacyPage 隐私政策（公开静态页）
func (a *App) PrivacyPage(c *gin.Context) {
	a.render(c, "privacy.html", gin.H{
		"title":     a.tr(c, "legal.privacyTitle"),
		"titleSolo": true,
	})
}
