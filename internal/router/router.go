package router

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"doc-share/internal/handler"
	"doc-share/internal/middleware"

	"github.com/gin-gonic/gin"
)

// New 构建路由
func New(app *handler.App, staticFS fs.FS) *gin.Engine {
	r := gin.Default()

	// 多语言：协商语言并翻译后台 API 返回的提示文案（默认语言直通，零开销）
	r.Use(middleware.I18n(app.I18N))
	r.GET("/lang", middleware.SetLangCookie)

	// 静态资源：/static/* 映射到 embed FS（dev 模式下为磁盘 FS）
	// dev 模式加 no-store，避免浏览器缓存旧 JS/CSS 导致"改了没生效"
	fileServer := http.FileServer(http.FS(staticFS))
	if app.Cfg.Server.Dev {
		r.GET("/static/*filepath", gin.WrapH(http.StripPrefix("/static/", noCache(fileServer))))
	} else {
		r.GET("/static/*filepath", gin.WrapH(http.StripPrefix("/static/", fileServer)))
	}

	// 上传的图片：本地磁盘目录映射到 /uploads。
	// HTML 整站文件（html/ 前缀）不公开直出：一律经 /s/:token/raw 或 /console/raw 凭短时签名访问，
	// 否则 /uploads/html/{docID}/{rand}/... 会成为密码分享与未分享文档的公开旁路。
	uploadsFS := http.FileServer(http.Dir(app.Cfg.Upload.Dir))
	r.GET("/uploads/*filepath", func(c *gin.Context) {
		p := path.Clean(c.Param("filepath"))
		if p == "/html" || strings.HasPrefix(p, "/html/") {
			c.Status(http.StatusNotFound)
			return
		}
		http.StripPrefix("/uploads", uploadsFS).ServeHTTP(c.Writer, c.Request)
	})

	admin := r.Group("/console", middleware.RequireAuth(app.DB, app.Signer))
	{
		admin.GET("", app.Dashboard)
		admin.GET("/docs", app.DocsPage)
		admin.GET("/docs/new", app.NewDocPage)
		admin.GET("/docs/:id/edit", app.EditDocPage)
		admin.GET("/docs/:id/preview", app.PreviewDoc)

		// 用户管理仅 admin
		admin.GET("/users", middleware.RequireAdmin(), app.UsersPage)
		admin.POST("/api/users", middleware.RequireAdmin(), app.CreateUser)
		admin.PUT("/api/users/:id", middleware.RequireAdmin(), app.UpdateUser)
		admin.DELETE("/api/users/:id", middleware.RequireAdmin(), app.DeleteUser)

		// 系统设置仅 admin
		admin.GET("/settings", middleware.RequireAdmin(), app.SettingsPage)
		admin.PUT("/api/settings", middleware.RequireAdmin(), app.UpdateSettings)
		admin.POST("/api/settings/test-mail", middleware.RequireAdmin(), app.TestMail)
		admin.POST("/api/settings/test-rustfs", middleware.RequireAdmin(), app.TestRustFS)

		// 项目内文档列表（属主/管理员/成员）：项目弹窗用
		admin.GET("/api/projects/:id/docs", app.ListProjectDocs)

		// 项目管理（个人归属，viewer 管自己的）；成员管理仅属主
		admin.GET("/projects", app.ProjectsPage)
		admin.POST("/api/projects", app.CreateProject)
		admin.PUT("/api/projects/:id", app.UpdateProject)
		admin.DELETE("/api/projects/:id", app.DeleteProject)
		admin.GET("/api/projects/:id/members", app.ListProjectMembers)
		admin.POST("/api/projects/:id/members", app.AddProjectMember)
		admin.PUT("/api/projects/:id/members/:mid", app.UpdateProjectMember)
		admin.DELETE("/api/projects/:id/members/me", app.LeaveProject)
		admin.DELETE("/api/projects/:id/members/:mid", app.RemoveProjectMember)

		// 项目分享（属主/管理员）：整组文档对外分享，语义同文档分享
		admin.GET("/api/projects/:id/share", app.GetProjectShare)
		admin.POST("/api/projects/:id/share", app.UpsertProjectShare)
		admin.DELETE("/api/projects/:id/share", app.DeleteProjectShare)
		admin.GET("/api/projects/:id/access-requests", app.ListProjectAccessRequests)
		admin.POST("/api/projects/:id/access-requests/:rid", app.ReviewProjectAccessRequest)

		// 成员选择器（任意登录用户，仅暴露 id/用户名/昵称）
		admin.GET("/api/users/options", app.UserOptions)

		// 分类管理（个人归属，viewer 管自己的）
		admin.GET("/categories", app.CategoriesPage)
		admin.POST("/api/categories", app.CreateCategory)
		admin.PUT("/api/categories/:id", app.UpdateCategory)
		admin.DELETE("/api/categories/:id", app.DeleteCategory)

		// 文档模板（个人归属；绑定项目后项目成员可共用）
		admin.GET("/templates", app.TemplatesPage)
		admin.GET("/templates/new", app.TemplateNewPage)
		admin.GET("/templates/:id/edit", app.TemplateEditPage)
		admin.GET("/api/templates/options", app.TemplateOptions)
		admin.GET("/api/templates/:id/apply", app.TemplateApply)
		admin.POST("/api/templates", app.CreateTemplate)
		admin.PUT("/api/templates/:id", app.UpdateTemplate)
		admin.DELETE("/api/templates/:id", app.DeleteTemplate)

		// API 密钥管理（个人归属，viewer 管自己的）
		admin.GET("/apikeys", app.APIKeysPage)
		admin.POST("/api/apikeys", app.CreateAPIKey)
		admin.PUT("/api/apikeys/:id/reset", app.ResetAPIKeySecret)
		admin.PUT("/api/apikeys/:id/status", app.UpdateAPIKeyStatus)
		admin.DELETE("/api/apikeys/:id", app.DeleteAPIKey)

		// 文档 API
		admin.POST("/api/docs", app.CreateDoc)
		admin.PUT("/api/docs/:id", app.UpdateDoc)
		admin.DELETE("/api/docs/:id", app.DeleteDoc)
		admin.POST("/api/docs/html", app.CreateHTMLDoc)
		admin.PUT("/api/docs/:id/html", app.ReplaceHTMLDoc)
		admin.POST("/api/docs/:id/editing", app.MarkEditing)
		admin.POST("/api/docs/:id/share", app.UpsertShare)
		admin.POST("/api/docs/:id/duplicate", app.DuplicateDoc)
		admin.DELETE("/api/docs/:id/share", app.DeleteShare)
		admin.GET("/api/docs/:id/access-requests", app.ListAccessRequests)
		admin.POST("/api/docs/:id/access-requests/:rid", app.ReviewAccessRequest)
		admin.GET("/api/access-requests", app.ListPendingAccessRequests)

		// 站内消息
		admin.GET("/api/messages", app.ListMessages)
		admin.GET("/api/messages/unread-count", app.UnreadMessageCount)
		admin.POST("/api/messages/read-all", app.MarkAllMessagesRead)
		admin.POST("/api/messages/:id/read", app.MarkMessageRead)

		// 文档评论（作者/管理员，登录身份）
		admin.GET("/api/docs/:id/comments", app.DocListComments)
		admin.POST("/api/docs/:id/comments", app.DocAddComment)
		admin.DELETE("/api/docs/:id/comments/:cid", app.DocDeleteComment)

		// 版本修订（内容覆盖前自动快照，可回滚；支持作者给快照打版本标签如 v1.0.1）
		admin.GET("/api/docs/:id/revisions", app.ListRevisions)
		admin.POST("/api/docs/:id/revisions/:rid/rollback", app.RollbackRevision)
		admin.PUT("/api/docs/:id/revisions/:rid/label", app.LabelRevision)

		// 图片上传 / 异步删除（嵌入预览覆盖）
		admin.POST("/api/upload", app.Upload)
		admin.DELETE("/api/upload", app.DeleteUpload)

		// 文档转换：pdf / doc / docx → Markdown
		admin.POST("/api/convert", app.Convert)

		// 修改自己的密码（任意登录用户）
		admin.PUT("/api/password", app.ChangePassword)
		admin.PUT("/api/profile", app.UpdateProfile)
	}

	// 旧地址兼容：/admin 前缀已全量迁移至 /console。307 临时跳转保留原方法与请求体，
	// 老书签与浏览器缓存的旧前端 JS（POST /admin/api/...）均不破坏
	r.Any("/admin", func(c *gin.Context) {
		target := "/console"
		if c.Request.URL.RawQuery != "" {
			target += "?" + c.Request.URL.RawQuery
		}
		c.Redirect(http.StatusTemporaryRedirect, target)
	})
	r.Any("/admin/*path", func(c *gin.Context) {
		target := "/console" + c.Param("path")
		if c.Request.URL.RawQuery != "" {
			target += "?" + c.Request.URL.RawQuery
		}
		c.Redirect(http.StatusTemporaryRedirect, target)
	})

	// 对接文档（公开）：潜在用户未登录也可查看 API / MCP 接入方式
	r.GET("/console/apidoc", app.APIDocPage)
	r.GET("/console/mcpdoc", app.MCPDocPage)

	// 认证
	r.GET("/captcha", app.Captcha)
	r.GET("/login", app.LoginPage)
	r.POST("/login", app.Login)
	r.GET("/register", app.RegisterPage)
	r.POST("/register", app.Register)
	r.POST("/register/email-code", app.SendRegisterCode)
	r.GET("/logout", app.Logout)
	r.POST("/logout", app.Logout)

	// 开放平台：HMAC 签名认证，以密钥属主身份操作；写接口直接复用后台 handler，所有权校验天然生效
	open := r.Group("/openapi/v1", app.RequireAppKey())
	{
		open.GET("/categories", app.OpenListCategories)
		open.POST("/categories", app.CreateCategory)
		open.PUT("/categories/:id", app.UpdateCategory)
		open.DELETE("/categories/:id", app.DeleteCategory)

		open.GET("/projects", app.OpenListProjects)
		open.POST("/projects", app.CreateProject)
		open.PUT("/projects/:id", app.UpdateProject)
		open.DELETE("/projects/:id", app.DeleteProject)

		open.GET("/docs", app.OpenListDocs)
		open.GET("/docs/:id", app.OpenGetDoc)
		open.POST("/docs", app.CreateDoc)
		open.POST("/docs/html", app.OpenCreateHTMLDoc)
		open.PUT("/docs/:id", app.UpdateDoc)
		open.DELETE("/docs/:id", app.DeleteDoc)
		open.GET("/docs/:id/share", app.OpenGetShare)
		open.POST("/docs/:id/share", app.UpsertShare)
		open.DELETE("/docs/:id/share", app.DeleteShare)

		// 文档模板（可见范围与 Web 端一致；写接口直接复用后台 handler）
		open.GET("/templates", app.OpenListTemplates)
		open.GET("/templates/:id", app.OpenGetTemplate)
		open.POST("/templates", app.CreateTemplate)
		open.PUT("/templates/:id", app.UpdateTemplate)
		open.DELETE("/templates/:id", app.DeleteTemplate)
	}

	// 分享
	r.GET("/s/:token", app.ShareView)
	r.POST("/s/:token", app.ShareSubmit)
	r.POST("/s/:token/access-request", app.ShareAccessApply)
	// 分享页公开协作接口：评论（游客可发，限流）与登录用户的编辑保存
	r.GET("/s/:token/comments", app.ShareListComments)
	r.POST("/s/:token/comments", app.ShareAddComment)
	r.PUT("/s/:token/content", app.ShareSaveContent)
	// HTML 整站文件（分享侧）：/s/:token/raw/<sig>/<path>。
	// 不走 cookie 鉴权——sandbox iframe 子资源是跨站请求带不上 cookie，凭路径内短时签名。
	r.GET("/s/:token/raw/*path", app.ShareRaw)
	// HTML 整站文件（管理预览侧）：/console/raw/:id/<sig>/<path>，同为签名鉴权故不挂 RequireAuth
	r.GET("/console/raw/:id/*path", app.AdminRaw)

	// 项目分享：整组文档对外分享。/ps/:token 列表页，/ps/:token/d/:docId 查看/编辑
	r.GET("/ps/:token", app.ProjectShareView)
	r.POST("/ps/:token", app.ProjectShareSubmit)
	r.POST("/ps/:token/access-request", app.ProjectShareAccessApply)
	r.GET("/ps/:token/d/:docId", app.ProjectShareDocView)
	r.GET("/ps/:token/d/:docId/comments", app.ProjectShareListComments)
	r.POST("/ps/:token/d/:docId/comments", app.ProjectShareAddComment)
	r.PUT("/ps/:token/d/:docId/content", app.ProjectShareSaveContent)
	// HTML 整站文件（项目分享侧）：凭路径内短时签名，理由同 /s/:token/raw
	r.GET("/ps/:token/d/:docId/raw/*path", app.ProjectShareRaw)

	// 官网首页（公开）；可选认证注入登录态，供首页按用户状态切换 CTA
	r.GET("/", middleware.OptionalAuth(app.DB, app.Signer), app.Home)

	return r
}

// noCache 包一层禁用缓存的响应头（dev 模式静态资源用）
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
