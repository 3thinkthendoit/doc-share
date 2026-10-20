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

// prepareCreatorRegion 阅读/预览前补齐并缩短展示用归属地。
// 空 region 且有 CreatorIP 时离线反查并落库；已存「中国 省 市」缩成省名后写回。
func (a *App) prepareCreatorRegion(doc *model.Document) {
	if doc == nil {
		return
	}
	if doc.CreatorRegion == "" {
		ip := strings.TrimSpace(doc.CreatorIP)
		if ip == "" {
			return
		}
		r := geoip.Lookup(ip)
		if r == "" {
			return
		}
		doc.CreatorRegion = r
		a.DB.Model(&model.Document{}).Where("id = ? AND creator_region = ?", doc.ID, "").
			UpdateColumn("creator_region", r)
		return
	}
	short := geoip.ShortRegion(doc.CreatorRegion)
	if short == "" || short == doc.CreatorRegion {
		return
	}
	prev := doc.CreatorRegion
	doc.CreatorRegion = short
	a.DB.Model(&model.Document{}).Where("id = ? AND creator_region = ?", doc.ID, prev).
		UpdateColumn("creator_region", short)
}
