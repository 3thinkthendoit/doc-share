package handler

import (
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"

	"doc-share/internal/config"
	"doc-share/internal/i18n"
	"doc-share/internal/middleware"
	"doc-share/internal/session"

	"github.com/gin-gonic/gin"
	"github.com/mojocn/base64Captcha"
	"gorm.io/gorm"
)

// App 汇总各 handler 共享的依赖
type App struct {
	Cfg        *config.Config
	DB         *gorm.DB
	Signer     *session.Signer
	Tmpl       *template.Template
	TmplRoot   fs.FS // 模板根目录；dev 模式下每次渲染据此重新解析，实现改模板不重启
	I18N       *i18n.Bundle
	Limiter    *middleware.ShareLimiter
	CaptchaMgr *base64Captcha.Captcha

	setMu    sync.RWMutex   // 站点设置缓存锁
	setCache *SiteSettings  // nil 表示未加载
}

// NewApp 构造 App
func NewApp(cfg *config.Config, db *gorm.DB, signer *session.Signer, tmpl *template.Template, bundle *i18n.Bundle) *App {
	// 数字图形验证码：80x240、4 位、内存存储（5 分钟过期，一次性）
	driver := base64Captcha.NewDriverDigit(80, 240, 4, 0.7, 80)
	return &App{
		Cfg:        cfg,
		DB:         db,
		Signer:     signer,
		Tmpl:       tmpl,
		I18N:       bundle,
		Limiter:    middleware.NewShareLimiter(5, 10*time.Minute),
		CaptchaMgr: base64Captcha.NewCaptcha(driver, base64Captcha.DefaultMemStore),
	}
}

// View 页面模板数据。仍是 map（模板里 {{.user}} / {{range .docs}} 等按键取值不变），
// 但翻译函数必须以方法形式暴露：html/template 不允许把 map 值或结构体字段
// 当函数带参调用（会报 “T is not a method but has arguments”），只有方法可以。
// 因此模板里写 {{.T "key"}} / {{$.T "key"}}，lang 与词典从 map 里取。
type View map[string]any

const viewBundleKey = "_bundle" // 仅内部使用，模板不渲染该键

// T 翻译页面文案：接受 i18n key 或中文原文，未收录时原样输出
func (v View) T(source string, args ...any) string {
	b, _ := v[viewBundleKey].(*i18n.Bundle)
	if b == nil {
		return i18n.Format(source, args...)
	}
	lang, _ := v["Lang"].(string)
	return b.TZ(lang, source, args...)
}

// TH 与 T 相同但不做 HTML 转义——只用于词典中含标签的整段文案（如 API 对接说明）
func (v View) TH(source string, args ...any) template.HTML {
	return template.HTML(v.T(source, args...))
}

// tr 按请求协商出的语言翻译 i18n key（供 handler 直接构造消息）
func (a *App) tr(c *gin.Context, key string, args ...any) string {
	if a.I18N == nil {
		return key
	}
	return a.I18N.T(middleware.Lang(c), key, args...)
}

// dictJSON “中文原文 → 译文”映射，供前端注入 window.__L10N（UI.t 直接按字面量查表）
func (a *App) dictJSON(lang string) string {
	if a.I18N == nil {
		return "{}"
	}
	return a.I18N.SourceJSON(lang)
}

// i18nText 将 handler 传入的中文文案（页面标题、表单错误）按原文反查词典
func (a *App) i18nText(lang, text string) string {
	if a.I18N == nil {
		return text
	}
	return a.I18N.TZ(lang, text)
}

// funcMap 供模板使用的辅助函数
func funcMap() template.FuncMap {
	return template.FuncMap{
		"now": time.Now,
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		// avatar 返回首个非空字符串的首字符（按 rune 切，中文安全），全空时返回 "U"
		"avatar": func(names ...string) string {
			for _, n := range names {
				if r := []rune(strings.TrimSpace(n)); len(r) > 0 {
					return string(r[0])
				}
			}
			return "U"
		},
	}
}

// ParseTemplates 从嵌入的模板 FS 解析所有页面模板到一个模板集合。
// templatesFS 的根目录应直接包含 *.html 文件。
func ParseTemplates(templatesFS fs.FS) (*template.Template, error) {
	t := template.New("").Funcs(funcMap())
	entries, err := fs.ReadDir(templatesFS, ".")
	if err != nil {
		return nil, err
	}
	patterns := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && path.Ext(e.Name()) == ".html" {
			patterns = append(patterns, e.Name())
		}
	}
	if len(patterns) == 0 {
		return nil, fmt.Errorf("no templates found")
	}
	return t.ParseFS(templatesFS, patterns...)
}

// setCookie 统一设置 Cookie
func setCookie(c *gin.Context, name, value string, maxAge int) {
	c.SetCookie(name, value, maxAge, "/", "", false, true)
}
