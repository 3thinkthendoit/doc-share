package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"os"

	"doc-share/internal/config"
	"doc-share/internal/database"
	"doc-share/internal/handler"
	"doc-share/internal/i18n"
	"doc-share/internal/router"
	"doc-share/internal/session"

	"github.com/gin-gonic/gin"
)

//go:embed web/templates
var templatesFS embed.FS

//go:embed web/static
var staticFS embed.FS

//go:embed web/locales
var localesFS embed.FS

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, issues, err := config.Load(*configPath)
	for _, is := range issues {
		log.Printf("[config] %s (${%s}): %s", is.Key, is.Env, is.Reason)
	}
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 确保上传图片存储目录存在
	if err := os.MkdirAll(cfg.Upload.Dir, 0o755); err != nil {
		log.Fatalf("创建上传目录失败: %v", err)
	}

	db, err := database.Init(cfg)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}

	tmplRoot, err := fs.Sub(templatesFS, "web/templates")
	if err != nil {
		log.Fatalf("模板目录错误: %v", err)
	}

	staticRoot, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		log.Fatalf("静态目录错误: %v", err)
	}

	// 开发模式：模板/静态资源直读磁盘，改前端只需刷新浏览器，无需重新编译重启
	if cfg.Server.Dev {
		log.Printf("[dev] 前端热载已开启：模板与静态资源直读磁盘 web/ 目录（相对运行目录）")
		tmplRoot = os.DirFS("web/templates")
		staticRoot = os.DirFS("web/static")
	}

	tmpl, err := handler.ParseTemplates(tmplRoot)
	if err != nil {
		log.Fatalf("解析模板失败: %v", err)
	}

	localesRoot, err := fs.Sub(localesFS, "web/locales")
	if err != nil {
		log.Fatalf("词典目录错误: %v", err)
	}
	bundle, err := i18n.Load(localesRoot)
	if err != nil {
		log.Fatalf("加载多语言词典失败: %v", err)
	}

	signer := session.NewSigner(cfg.Auth.SessionSecret)
	app := handler.NewApp(cfg, db, signer, tmpl, bundle)
	app.TmplRoot = tmplRoot // dev 热载用：render 时据此重新解析模板

	gin.SetMode(gin.ReleaseMode)
	engine := router.New(app, staticRoot)

	log.Printf("DocShare 服务已启动: http://localhost%s", cfg.Server.Port)
	if err := engine.Run(cfg.Server.Port); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}
