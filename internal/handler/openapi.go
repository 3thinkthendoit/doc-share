package handler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

// ---------- 签名协议 ----------
// 请求头：X-App-Key / X-Timestamp(秒) / X-Nonce(8~64位随机串) / X-Signature
// 待签串 = appKey \n METHOD \n path \n timestamp \n nonce \n hex(sha256(body))
// 签名   = hex( HMAC-SHA256(secret, 待签串) )

const (
	openSigWindow   = 5 * 60 // 时间戳允许偏差（秒），防重放窗口
	openBodyLimit   = 2 << 20
	openNonceMin    = 8
	openNonceMax    = 64
	openNonceMaxLen = 10000 // nonce 缓存条数上限，超出触发清理
)

// nonceCache 内存去重（单实例部署；多实例需换 Redis）
var nonceCache = struct {
	sync.Mutex
	m map[string]time.Time
}{m: make(map[string]time.Time)}

// nonceSeen 记录并判断 nonce 是否已用过
func nonceSeen(key string) bool {
	nonceCache.Lock()
	defer nonceCache.Unlock()
	now := time.Now()
	if len(nonceCache.m) > openNonceMaxLen {
		for k, t := range nonceCache.m {
			if now.Sub(t) > openSigWindow*time.Second {
				delete(nonceCache.m, k)
			}
		}
	}
	if _, ok := nonceCache.m[key]; ok {
		return true
	}
	nonceCache.m[key] = now
	return false
}

func stringToSign(appKey, method, path, timestamp, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	return appKey + "\n" + method + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(sum[:])
}

func calcSignature(secret, sts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sts))
	return hex.EncodeToString(mac.Sum(nil))
}

func intAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func openAbort(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

// RequireAppKey openapi 签名认证中间件。
// 验证通过后将密钥属主注入 ContextUser，直接复用后台的分类/项目/文档 CRUD handler，
// 所有权校验逻辑天然生效（viewer 的密钥只能操作 viewer 自己的资源）
func (a *App) RequireAppKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		appKey := c.GetHeader("X-App-Key")
		ts := c.GetHeader("X-Timestamp")
		nonce := c.GetHeader("X-Nonce")
		sig := c.GetHeader("X-Signature")
		if appKey == "" || ts == "" || nonce == "" || sig == "" {
			openAbort(c, http.StatusUnauthorized, errMissingSigHeaders)
			return
		}
		tsInt, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || intAbs(int(time.Now().Unix())-int(tsInt)) > openSigWindow {
			openAbort(c, http.StatusUnauthorized, errTimestampBad)
			return
		}
		if len(nonce) < openNonceMin || len(nonce) > openNonceMax {
			openAbort(c, http.StatusUnauthorized, errNonceLen)
			return
		}
		var key model.ApiKey
		if err := a.DB.Where("app_key = ?", appKey).First(&key).Error; err != nil || key.Status != model.StatusEnabled {
			openAbort(c, http.StatusUnauthorized, errAppKeyBad)
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, openBodyLimit+1))
		if err != nil || len(body) > openBodyLimit {
			openAbort(c, http.StatusBadRequest, errBodyTooLarge)
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewBuffer(body))
		// 先验签再消费 nonce，避免伪造请求污染去重缓存
		sts := stringToSign(appKey, c.Request.Method, c.Request.URL.Path, ts, nonce, body)
		if !hmac.Equal([]byte(calcSignature(key.Secret, sts)), []byte(strings.ToLower(sig))) {
			openAbort(c, http.StatusUnauthorized, errSigBad)
			return
		}
		if nonceSeen(appKey + ":" + nonce) {
			openAbort(c, http.StatusUnauthorized, errNonceReplay)
			return
		}
		var owner model.User
		if err := a.DB.First(&owner, key.OwnerID).Error; err != nil || owner.Status != model.StatusEnabled {
			openAbort(c, http.StatusUnauthorized, errOwnerBad)
			return
		}
		a.DB.Model(&key).UpdateColumn("last_used_at", time.Now())
		c.Set(middleware.ContextUser, &owner)
		c.Next()
	}
}

// ---------- openapi 专用查询接口（写接口直接复用后台 handler） ----------

// OpenListCategories 分类列表（密钥属主自己的）
func (a *App) OpenListCategories(c *gin.Context) {
	user := middleware.CurrentUser(c)
	var items []model.Category
	a.DB.Where("owner_id = ?", user.ID).Order("sort asc, id asc").Find(&items)
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// OpenListProjects 项目列表（密钥属主自己的）
func (a *App) OpenListProjects(c *gin.Context) {
	user := middleware.CurrentUser(c)
	var items []model.Project
	a.DB.Where("owner_id = ?", user.ID).Order("updated_at desc").Find(&items)
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// OpenListDocs 文档分页列表；可按 project_id / category_id 过滤；不返回正文
func (a *App) OpenListDocs(c *gin.Context) {
	user := middleware.CurrentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	tx := a.DB.Model(&model.Document{})
	if !user.IsAdmin() {
		tx = tx.Where("owner_id = ?", user.ID)
	}
	if p := c.Query("project_id"); p != "" {
		if pid, err := strconv.Atoi(p); err == nil {
			tx = tx.Where("project_id = ?", pid)
		}
	}
	if ct := c.Query("category_id"); ct != "" {
		if cid, err := strconv.Atoi(ct); err == nil {
			tx = tx.Where("category_id = ?", cid)
		}
	}
	var total int64
	tx.Count(&total)
	var docs []model.Document
	tx.Select("id, title, slug, owner_id, project_id, category_id, is_shared, view_count, created_at, updated_at").
		Order("updated_at desc").Offset((page - 1) * size).Limit(size).Find(&docs)
	c.JSON(http.StatusOK, gin.H{"data": docs, "total": total, "page": page, "size": size})
}

// OpenGetDoc 文档详情（含正文）；loadDoc 自带所有权校验
func (a *App) OpenGetDoc(c *gin.Context) {
	doc := a.loadDoc(c)
	if doc == nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": doc})
}
