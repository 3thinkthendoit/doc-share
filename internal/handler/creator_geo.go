package handler

import (
	"strings"

	"doc-share/internal/geoip"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

// stampCreatorGeo 在文档创建时写入创建者 IP 与离线反查归属地（只应在 Create 前调用一次）。
func stampCreatorGeo(doc *model.Document, ip string) {
	if doc == nil {
		return
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return
	}
	doc.CreatorIP = ip
	if r := geoip.Lookup(ip); r != "" {
		doc.CreatorRegion = r
	}
}

// stampCreatorGeoFromCtx 从请求上下文取 ClientIP 并写入。
func stampCreatorGeoFromCtx(c *gin.Context, doc *model.Document) {
	if c == nil {
		return
	}
	stampCreatorGeo(doc, c.ClientIP())
}
