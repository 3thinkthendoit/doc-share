package router

import (
	"io/fs"
	"net/http"

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

	// 上传的图片：本地磁盘目录映射到 /uploads
	r.Static("/uploads", app.Cfg.Upload.Dir)

	admin := r.Group("/admin", middleware.RequireAuth(app.DB, app.Signer))
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

		// 成员选择器（任意登录用户，仅暴露 id/用户名/昵称）
		admin.GET("/api/users/options", app.UserOptions)

		// 分类管理（个人归属，viewer 管自己的）
		admin.GET("/categories", app.CategoriesPage)
		admin.POST("/api/categories", app.CreateCategory)
		admin.PUT("/api/categories/:id", app.UpdateCategory)
		admin.DELETE("/api/categories/:id", app.DeleteCategory)

		// API 密钥管理（个人归属，viewer 管自己的）
		admin.GET("/apikeys", app.APIKeysPage)
		admin.GET("/apidoc", app.APIDocPage)
		admin.POST("/api/apikeys", app.CreateAPIKey)
		admin.PUT("/api/apikeys/:id/reset", app.ResetAPIKeySecret)
		admin.PUT("/api/apikeys/:id/status", app.UpdateAPIKeyStatus)
		admin.DELETE("/api/apikeys/:id", app.DeleteAPIKey)

		// 文档 API
		admin.POST("/api/docs", app.CreateDoc)
		admin.PUT("/api/docs/:id", app.UpdateDoc)
		admin.DELETE("/api/docs/:id", app.DeleteDoc)
		admin.POST("/api/docs/:id/editing", app.MarkEditing)
		admin.POST("/api/docs/:id/share", app.UpsertShare)
		admin.DELETE("/api/docs/:id/share", app.DeleteShare)
		admin.GET("/api/docs/:id/access-requests", app.ListAccessRequests)
		admin.POST("/api/docs/:id/access-requests/:rid", app.ReviewAccessRequest)
		admin.GET("/api/access-requests", app.ListPendingAccessRequests)

		// 文档评论（作者/管理员，登录身份）
		admin.GET("/api/docs/:id/comments", app.DocListComments)
		admin.POST("/api/docs/:id/comments", app.DocAddComment)
		admin.DELETE("/api/docs/:id/comments/:cid", app.DocDeleteComment)

		// 版本修订（分享编辑覆盖前自动快照，可回滚）
		admin.GET("/api/docs/:id/revisions", app.ListRevisions)
		admin.POST("/api/docs/:id/revisions/:rid/rollback", app.RollbackRevision)

		// 图片上传 / 异步删除（嵌入预览覆盖）
		admin.POST("/api/upload", app.Upload)
		admin.DELETE("/api/upload", app.DeleteUpload)

		// 文档转换：pdf / doc / docx → Markdown
		admin.POST("/api/convert", app.Convert)

		// 修改自己的密码（任意登录用户）
		admin.PUT("/api/password", app.ChangePassword)
		admin.PUT("/api/profile", app.UpdateProfile)
	}

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
		open.PUT("/docs/:id", app.UpdateDoc)
		open.DELETE("/docs/:id", app.DeleteDoc)
	}

	// 分享
	r.GET("/s/:token", app.ShareView)
	r.POST("/s/:token", app.ShareSubmit)
	r.POST("/s/:token/access-request", app.ShareAccessApply)
	// 分享页公开协作接口：评论（游客可发，限流）与登录用户的编辑保存
	r.GET("/s/:token/comments", app.ShareListComments)
	r.POST("/s/:token/comments", app.ShareAddComment)
	r.PUT("/s/:token/content", app.ShareSaveContent)

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
