package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"doc-share/internal/i18n"

	"github.com/gin-gonic/gin"
)

// ContextLang 请求上下文中的当前语言键
const ContextLang = "ds_lang"

// Lang 取当前请求协商出的语言代码；未经中间件时回退默认语言
func Lang(c *gin.Context) string {
	if v, ok := c.Get(ContextLang); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return i18n.Default
}

// capturePrefixes 只缓冲这些前缀的响应（浏览器侧 JSON 接口），
// /openapi 是对外契约、/static 与 /uploads 是文件流，都不参与翻译
var capturePrefixes = []string{"/admin", "/login", "/register", "/s/"}

func shouldCapture(path string) bool {
	for _, p := range capturePrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// bodyCapture 把响应体先写进缓冲区，便于结束后整体翻译再落盘
type bodyCapture struct {
	gin.ResponseWriter
	buf bytes.Buffer
}

func (w *bodyCapture) Write(p []byte) (int, error)       { return w.buf.Write(p) }
func (w *bodyCapture) WriteString(s string) (int, error) { return w.buf.WriteString(s) }
func (w *bodyCapture) Written() bool                     { return w.buf.Len() > 0 }
func (w *bodyCapture) WriteHeaderNow()                   {} // 延后到恢复真实 writer 时再发状态码

// Release 交还真实 writer，由中间件统一 flush 状态码与（翻译后的）响应体
func (w *bodyCapture) Release() (*bytes.Buffer, gin.ResponseWriter) {
	rw := w.ResponseWriter
	w.ResponseWriter = nil
	return &w.buf, rw
}

// I18n 语言协商 + API 消息翻译。
// 语言优先级：cookie 偏好 > Accept-Language > 默认。默认语言零开销直通。
func I18n(b *i18n.Bundle) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, _ := c.Cookie(i18n.CookieName)
		lang := i18n.Resolve(cookie, c.GetHeader("Accept-Language"))
		c.Set(ContextLang, lang)

		if lang == i18n.Default || !shouldCapture(c.Request.URL.Path) {
			c.Next()
			return
		}

		bc := &bodyCapture{ResponseWriter: c.Writer}
		c.Writer = bc
		c.Next()

		buf, rw := bc.Release()
		c.Writer = rw
		body := buf.Bytes()
		if ct := rw.Header().Get("Content-Type"); strings.Contains(ct, "application/json") && len(body) > 0 {
			var m map[string]any
			if err := json.Unmarshal(body, &m); err == nil {
				if b.TranslateMap(lang, m) > 0 {
					if out, err := json.Marshal(m); err == nil {
						body = out
					}
				}
			}
		}
		rw.WriteHeaderNow()
		_, _ = rw.Write(body)
	}
}

// SetLangCookie 语言切换：写 cookie 后跳回来源页
func SetLangCookie(c *gin.Context) {
	lang := c.Query("lang")
	if !i18n.Valid(lang) {
		c.Redirect(http.StatusFound, "/")
		return
	}
	// 只允许站内相对路径，避免开放重定向
	next := c.Query("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/"
	}
	c.SetCookie(i18n.CookieName, lang, 60*60*24*365, "/", "", false, true)
	c.Redirect(http.StatusFound, next)
}
