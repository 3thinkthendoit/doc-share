package handler

import (
	"bytes"
	"html/template"
	"log"
	"net/http"

	"doc-share/internal/i18n"
	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

// render 使用嵌入模板渲染页面，自动注入当前用户与多语言上下文
// H2：先缓冲执行模板，失败时返回干净的 500，避免先写 200 后报错产生半截页面
func (a *App) render(c *gin.Context, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	if _, ok := data["user"]; !ok {
		data["user"] = middleware.CurrentUser(c)
	}
	lang := middleware.Lang(c)
	// 模板里统一用 {{.T "key"}} / {{$.T "key"}}（View 的方法），前端脚本用 window.__L10N
	data["Lang"] = lang
	data["Langs"] = i18n.Supported
	data["Path"] = c.Request.URL.RequestURI() // 语言切换后跳回当前页
	data["Dict"] = template.JS(a.dictJSON(lang))
	data[viewBundleKey] = a.I18N
	// 站点设置：模板里 {{.SiteName}} / {{.SiteLogo}} / {{.SiteDomain}}
	s := a.Settings()
	data["SiteName"] = s.SiteName
	data["SiteLogo"] = s.SiteLogo
	data["SiteDomain"] = s.SiteDomain
	// 部分页面直接拿 handler 传入的中文标题 / 表单错误文案，这里统一反查词典；
	// rawTitle=true 表示 title 是用户内容（文档标题等），跳过反查避免与词典原文撞车被误译
	if _, raw := data["rawTitle"]; !raw {
		if s, ok := data["title"].(string); ok {
			data["title"] = a.i18nText(lang, s)
		}
	}
	if s, ok := data["error"].(string); ok {
		data["error"] = a.i18nText(lang, s)
	}
	// dev 模式：每次渲染从磁盘重新解析模板，改模板刷新浏览器即生效；
	// 解析失败沿用上次成功的模板集合（a.Tmpl 不被覆盖），页面仍可用。
	// 同时禁用页面缓存：模板改了之后浏览器不会拿旧 HTML
	if a.Cfg.Server.Dev {
		c.Header("Cache-Control", "no-store")
	}
	tmpl := a.Tmpl
	if a.Cfg.Server.Dev && a.TmplRoot != nil {
		if t, err := ParseTemplates(a.TmplRoot); err == nil {
			tmpl = t
		} else {
			log.Printf("[dev] 模板重新解析失败，沿用旧模板: %v", err)
		}
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, View(data)); err != nil {
		c.String(http.StatusInternalServerError, "render error: %v", err)
		return
	}
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_, _ = c.Writer.Write(buf.Bytes())
}

// Home 官网首页：公开访问，登录与否都可看
func (a *App) Home(c *gin.Context) {
	a.render(c, "home.html", gin.H{"user": middleware.CurrentUser(c)})
}

// Dashboard 仪表盘：统计信息
func (a *App) Dashboard(c *gin.Context) {
	user := middleware.CurrentUser(c)
	isAdmin := user != nil && user.IsAdmin()

	var docCount, userCount, shareCount, projectCount, pendingApplyCount int64
	recentTx := a.DB.Model(&model.Document{})
	if isAdmin {
		a.DB.Model(&model.Document{}).Count(&docCount)
		a.DB.Model(&model.User{}).Count(&userCount)
		a.DB.Model(&model.Share{}).Count(&shareCount)
		a.DB.Model(&model.ShareAccessRequest{}).Where("status = ?", model.AccessPending).Count(&pendingApplyCount)
	} else {
		// 非管理员：统计与最近列表仅针对自己的文档；用户总数不展示
		a.DB.Model(&model.Document{}).Where("owner_id = ?", user.ID).Count(&docCount)
		a.DB.Model(&model.Share{}).
			Where("document_id IN (SELECT id FROM documents WHERE owner_id = ?)", user.ID).Count(&shareCount)
		a.DB.Model(&model.ShareAccessRequest{}).
			Where("status = ? AND document_id IN (SELECT id FROM documents WHERE owner_id = ?)", model.AccessPending, user.ID).
			Count(&pendingApplyCount)
		recentTx = recentTx.Where("owner_id = ?", user.ID)
	}
	// 关联项目数：自己拥有的 + 作为成员参与的项目（去重）；
	// 管理员全量可管，无「关联」概念，不统计也不在仪表盘展示
	if !isAdmin {
		a.DB.Model(&model.Project{}).
			Where("owner_id = ? OR id IN (SELECT project_id FROM project_members WHERE user_id = ?)", user.ID, user.ID).
			Count(&projectCount)
	}

	var recent []model.Document
	recentTx.Preload("Owner").Preload("Share").Order("updated_at desc").Limit(5).Find(&recent)

	a.render(c, "dashboard.html", gin.H{
		"title":             "仪表盘",
		"isAdmin":           isAdmin,
		"docCount":          docCount,
		"userCount":         userCount,
		"shareCount":        shareCount,
		"projectCount":      projectCount,
		"pendingApplyCount": pendingApplyCount,
		"recent":            recent,
	})
}
