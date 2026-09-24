package handler

import (
	"net/http"
	"strings"

	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SiteSettings 站点设置（内存缓存，保存时失效）
type SiteSettings struct {
	SiteName   string
	SiteLogo   string
	SiteDomain string
}

// 常用默认值
const defaultSiteName = "DocShare"

// Settings 读取站点设置：首次从 DB 加载后缓存，保存时由 UpdateSettings 失效
func (a *App) Settings() SiteSettings {
	a.setMu.RLock()
	if a.setCache != nil {
		s := *a.setCache
		a.setMu.RUnlock()
		return s
	}
	a.setMu.RUnlock()

	s := SiteSettings{SiteName: defaultSiteName}
	var rows []model.SystemSetting
	if err := a.DB.Where("`key` IN ?", []string{model.SettingSiteName, model.SettingSiteLogo, model.SettingSiteDomain}).Find(&rows).Error; err != nil {
		return s // 查询失败不写缓存，下次请求重试；本次降级为默认值
	}
	for _, r := range rows {
		switch r.Key {
		case model.SettingSiteName:
			if strings.TrimSpace(r.Value) != "" {
				s.SiteName = strings.TrimSpace(r.Value)
			}
		case model.SettingSiteLogo:
			s.SiteLogo = strings.TrimSpace(r.Value)
		case model.SettingSiteDomain:
			s.SiteDomain = strings.TrimSpace(r.Value)
		}
	}

	a.setMu.Lock()
	a.setCache = &s
	a.setMu.Unlock()
	return s
}

// SettingsPage 系统设置页（仅 admin）
func (a *App) SettingsPage(c *gin.Context) {
	s := a.Settings()
	a.render(c, "settings.html", gin.H{
		"title": a.tr(c, "set.title"),
		"set":   s,
	})
}

// UpdateSettings 保存站点设置（仅 admin）；logo 走 /admin/api/upload 得到 URL 后填入
func (a *App) UpdateSettings(c *gin.Context) {
	var req struct {
		SiteName   string `json:"site_name"`
		SiteLogo   string `json:"site_logo"`
		SiteDomain string `json:"site_domain"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	name := strings.TrimSpace(req.SiteName)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "set.errName")})
		return
	}
	if len(name) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "网站名称过长（≤64 字符）"})
		return
	}
	logo := strings.TrimSpace(req.SiteLogo)
	if logo != "" {
		if len(logo) > 500 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Logo 地址过长（≤500 字符）"})
			return
		}
		// 协议白名单：相对路径（/uploads/...）或 http(s)；防止存入 javascript: 等伪协议
		low := strings.ToLower(logo)
		if !strings.HasPrefix(low, "/") && !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Logo 地址仅支持站内路径或 http(s) 链接"})
			return
		}
	}
	domain := normalizeSiteURL(req.SiteDomain)

	values := map[string]string{
		model.SettingSiteName:   name,
		model.SettingSiteLogo:   logo,
		model.SettingSiteDomain: domain,
	}
	// 事务内逐条 upsert：先确保行存在，再显式写 value（显式列更新不受结构体零值跳过影响）
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range values {
			if err := tx.Where(model.SystemSetting{Key: k}).
				FirstOrCreate(&model.SystemSetting{Key: k}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.SystemSetting{Key: k}).UpdateColumn("value", v).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}

	a.setMu.Lock()
	a.setCache = nil
	a.setMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// normalizeSiteURL 归一域名：补 https:// 前缀、去尾部斜杠；空值返回空
func normalizeSiteURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	return strings.TrimRight(s, "/")
}

// siteBaseURL 分享链接等绝对地址的站点前缀：优先系统域名，留空取当前请求
func (a *App) siteBaseURL(c *gin.Context) string {
	if d := a.Settings().SiteDomain; d != "" {
		return d
	}
	return scheme(c) + "://" + c.Request.Host
}
