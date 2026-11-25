package handler

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 第三方应用接入（OAuth App）管理：开发者自助注册 → 管理员审核 → 上线；
// 用户在同一页查看 / 撤销自己授权过的应用。参考 think-tpad oauth_server.go。

// MsgOAuthApp 站内消息类型：应用待审核 / 审核结果
const MsgOAuthApp = "oauth_app"

type oauthConsentRow struct {
	ClientID  string
	AppName   string
	Homepage  string
	Scopes    string
	GrantedAt time.Time
}

// OAuthAppsPage GET /console/oauth-apps
func (a *App) OAuthAppsPage(c *gin.Context) {
	user := middleware.CurrentUser(c)
	status := strings.TrimSpace(c.Query("status"))
	pg := parseWebPage(c)

	tx := a.DB.Model(&model.OAuthClient{})
	if !user.IsAdmin() {
		tx = tx.Where("owner_id = ?", user.ID)
	}
	switch status {
	case model.OAuthClientPending, model.OAuthClientApproved, model.OAuthClientRejected, model.OAuthClientDisabled:
		tx = tx.Where("status = ?", status)
	default:
		status = ""
	}
	var total int64
	tx.Count(&total)
	pg = pg.withTotal(total)
	var apps []model.OAuthClient
	tx.Preload("Owner").Order("id desc").Offset(pg.Offset).Limit(pg.Size).Find(&apps)

	// 我授权过的应用（批量取名，应用已删除时回退显示 client_id）
	var consents []model.OAuthConsent
	a.DB.Where("user_id = ? AND revoked_at IS NULL", user.ID).Order("id desc").Find(&consents)
	ids := make([]string, 0, len(consents))
	for _, cs := range consents {
		ids = append(ids, cs.ClientID)
	}
	clients := map[string]model.OAuthClient{}
	if len(ids) > 0 {
		var rows []model.OAuthClient
		a.DB.Select("client_id", "name", "homepage_url").Where("client_id IN ?", ids).Find(&rows)
		for _, r := range rows {
			clients[r.ClientID] = r
		}
	}
	consentRows := make([]oauthConsentRow, 0, len(consents))
	seen := map[string]bool{}
	for _, cs := range consents {
		if seen[cs.ClientID] {
			continue
		}
		seen[cs.ClientID] = true
		cl := clients[cs.ClientID]
		name := cl.Name
		if name == "" {
			name = cs.ClientID
		}
		consentRows = append(consentRows, oauthConsentRow{ClientID: cs.ClientID, AppName: name,
			Homepage: cl.HomepageURL, Scopes: cs.Scopes, GrantedAt: cs.GrantedAt})
	}

	var pending int64
	if user.IsAdmin() {
		a.DB.Model(&model.OAuthClient{}).Where("status = ?", model.OAuthClientPending).Count(&pending)
	}
	base := a.oidcIssuer(c)
	extra := ""
	if status != "" {
		extra = "&status=" + url.QueryEscape(status)
	}
	data := gin.H{
		"title":             a.tr(c, "oauthsrv.manage"),
		"apps":              apps,
		"consents":          consentRows,
		"status":            status,
		"pendingCount":      pending,
		"epAuthorize":       base + "/oauth/authorize",
		"epToken":           base + "/oauth/token",
		"epUserinfo":        base + "/oauth/userinfo",
		"epRevoke":          base + "/oauth/revoke",
		"epJWKS":            base + "/oauth/jwks",
		"epDiscovery":       base + "/.well-known/openid-configuration",
		"issuer":            base,
		"issuerFromSetting": strings.TrimSpace(a.Settings().SiteDomain) != "",
	}
	for k, v := range pagerFields(pg, "/console/oauth-apps", extra) {
		data[k] = v
	}
	a.render(c, "oauth_apps.html", data)
}

type oauthAppReq struct {
	Name         string `json:"name"`
	HomepageURL  string `json:"homepage_url"`
	RedirectURIs string `json:"redirect_uris"` // 换行分隔
	ClientType   string `json:"client_type"`   // 仅创建时生效：confidential（默认）/ public
}

// validate 返回规范化后的名称、主页、回调列表；errKey 非空表示校验失败
func (r oauthAppReq) validate() (name, homepage string, uris []string, errKey string) {
	name = strings.TrimSpace(r.Name)
	if name == "" || len([]rune(name)) > 64 {
		return "", "", nil, "oauthsrv.errAppName"
	}
	homepage = strings.TrimSpace(r.HomepageURL)
	if homepage != "" {
		p, err := url.Parse(homepage)
		if err != nil || p.Host == "" || (p.Scheme != "http" && p.Scheme != "https") || len(homepage) > 500 {
			return "", "", nil, "oauthsrv.errHomepage"
		}
	}
	uris, errKey = validateOAuthRedirectURIs(r.RedirectURIs)
	return name, homepage, uris, errKey
}

// loadOAuthApp 按 id 加载应用：管理员可管全部，其他人只能管自己的
func (a *App) loadOAuthApp(c *gin.Context) *model.OAuthClient {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "oauthsrv.errAppNotFound")})
		return nil
	}
	var app model.OAuthClient
	if err := a.DB.First(&app, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": a.tr(c, "oauthsrv.errAppNotFound")})
		return nil
	}
	if user := middleware.CurrentUser(c); !user.IsAdmin() && app.OwnerID != user.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": a.tr(c, "oauthsrv.errAppForbidden")})
		return nil
	}
	return &app
}

// revokeOAuthClientData 吊销某应用的全部用户授权、未用授权码与刷新令牌（已发 access_token 随之在 userinfo 失效）
func revokeOAuthClientData(tx *gorm.DB, clientID string) error {
	now := time.Now()
	if err := tx.Model(&model.OAuthConsent{}).Where("client_id = ? AND revoked_at IS NULL", clientID).
		Update("revoked_at", now).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.OAuthRefreshToken{}).Where("client_id = ? AND revoked_at IS NULL", clientID).
		Update("revoked_at", now).Error; err != nil {
		return err
	}
	return tx.Where("client_id = ? AND used_at IS NULL", clientID).Delete(&model.OAuthCode{}).Error
}

// CreateOAuthApp POST /console/api/oauth-apps
// 普通用户注册后待审核；管理员自己注册的直接通过。Secret 只在本次响应返回；公开客户端没有 Secret。
func (a *App) CreateOAuthApp(c *gin.Context) {
	var req oauthAppReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "oauthsrv.errParam")})
		return
	}
	name, homepage, uris, errKey := req.validate()
	if errKey != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, errKey)})
		return
	}
	clientType := strings.TrimSpace(req.ClientType)
	if clientType == "" {
		clientType = model.OAuthClientConfidential
	}
	if clientType != model.OAuthClientConfidential && clientType != model.OAuthClientPublic {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "oauthsrv.errClientType")})
		return
	}
	user := middleware.CurrentUser(c)
	clientID, err1 := newOAuthToken("ds_", 10)
	secret, err2 := newOAuthToken("dss_", 24)
	if err1 != nil || err2 != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": a.tr(c, "oauthsrv.errServer")})
		return
	}
	secretHash := hashOAuthSecret(secret)
	if clientType == model.OAuthClientPublic {
		secret, secretHash = "", ""
	}
	status := model.OAuthClientPending
	if user.IsAdmin() {
		status = model.OAuthClientApproved
	}
	app := model.OAuthClient{
		ClientID: clientID, ClientSecretHash: secretHash, ClientType: clientType, Name: name, HomepageURL: homepage,
		RedirectURIs: strings.Join(uris, "\n"), OwnerID: user.ID, Status: status,
	}
	if err := a.DB.Create(&app).Error; err != nil {
		log.Printf("[oauth-app] create: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": a.tr(c, "oauthsrv.errServer")})
		return
	}
	if status == model.OAuthClientPending {
		a.notifyOAuthAppPending(&app, user)
	}
	c.JSON(http.StatusOK, gin.H{"data": app, "client_secret": secret})
}

// UpdateOAuthApp PUT /console/api/oauth-apps/:id
// 非管理员改动回调白名单、或修改被驳回的应用后，回到待审核。
func (a *App) UpdateOAuthApp(c *gin.Context) {
	app := a.loadOAuthApp(c)
	if app == nil {
		return
	}
	var req oauthAppReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "oauthsrv.errParam")})
		return
	}
	name, homepage, uris, errKey := req.validate()
	if errKey != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, errKey)})
		return
	}
	user := middleware.CurrentUser(c)
	newURIs := strings.Join(uris, "\n")
	updates := map[string]any{"name": name, "homepage_url": homepage, "redirect_uris": newURIs}
	resubmit := false
	if !user.IsAdmin() && app.Status != model.OAuthClientDisabled &&
		(newURIs != app.RedirectURIs || app.Status == model.OAuthClientRejected) {
		updates["status"] = model.OAuthClientPending
		updates["reject_reason"] = ""
		resubmit = app.Status != model.OAuthClientPending
	}
	if err := a.DB.Model(app).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": a.tr(c, "oauthsrv.errServer")})
		return
	}
	if resubmit {
		a.notifyOAuthAppPending(app, user)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "resubmitted": resubmit})
}

// RotateOAuthAppSecret POST /console/api/oauth-apps/:id/rotate-secret：旧 Secret 立即失效
func (a *App) RotateOAuthAppSecret(c *gin.Context) {
	app := a.loadOAuthApp(c)
	if app == nil {
		return
	}
	if app.IsPublic() {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "oauthsrv.errPublicNoSecret")})
		return
	}
	secret, err := newOAuthToken("dss_", 24)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": a.tr(c, "oauthsrv.errServer")})
		return
	}
	if err := a.DB.Model(app).Update("client_secret_hash", hashOAuthSecret(secret)).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": a.tr(c, "oauthsrv.errServer")})
		return
	}
	c.JSON(http.StatusOK, gin.H{"client_id": app.ClientID, "client_secret": secret})
}

// DeleteOAuthApp DELETE /console/api/oauth-apps/:id：级联撤销全部授权
func (a *App) DeleteOAuthApp(c *gin.Context) {
	app := a.loadOAuthApp(c)
	if app == nil {
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := revokeOAuthClientData(tx, app.ClientID); err != nil {
			return err
		}
		return tx.Delete(app).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": a.tr(c, "oauthsrv.errServer")})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ReviewOAuthApp POST /console/api/oauth-apps/:id/review（仅管理员）：approve / reject / disable
func (a *App) ReviewOAuthApp(c *gin.Context) {
	app := a.loadOAuthApp(c)
	if app == nil {
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "oauthsrv.errParam")})
		return
	}
	reason := clipRunes(strings.TrimSpace(req.Reason), 500)
	var status string
	switch req.Action {
	case "approve":
		status = model.OAuthClientApproved
		reason = ""
	case "reject":
		status = model.OAuthClientRejected
	case "disable":
		status = model.OAuthClientDisabled
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "oauthsrv.errParam")})
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if status == model.OAuthClientDisabled {
			if err := revokeOAuthClientData(tx, app.ClientID); err != nil {
				return err
			}
		}
		return tx.Model(app).Updates(map[string]any{"status": status, "reject_reason": reason}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": a.tr(c, "oauthsrv.errServer")})
		return
	}
	if reviewer := middleware.CurrentUser(c); app.OwnerID != reviewer.ID {
		a.notifyOAuthAppReviewed(app, status, reason)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": status})
}

// RevokeOAuthConsent DELETE /console/api/oauth-consents/:clientId：用户撤销对某应用的授权
func (a *App) RevokeOAuthConsent(c *gin.Context) {
	clientID := strings.TrimSpace(c.Param("clientId"))
	if clientID == "" || len(clientID) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "oauthsrv.errParam")})
		return
	}
	user := middleware.CurrentUser(c)
	var affected int64
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.OAuthConsent{}).
			Where("user_id = ? AND client_id = ? AND revoked_at IS NULL", user.ID, clientID).
			Update("revoked_at", time.Now())
		if res.Error != nil {
			return res.Error
		}
		affected = res.RowsAffected
		if err := tx.Model(&model.OAuthRefreshToken{}).
			Where("user_id = ? AND client_id = ? AND revoked_at IS NULL", user.ID, clientID).
			Update("revoked_at", time.Now()).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ? AND client_id = ? AND used_at IS NULL", user.ID, clientID).
			Delete(&model.OAuthCode{}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": a.tr(c, "oauthsrv.errServer")})
		return
	}
	if affected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": a.tr(c, "oauthsrv.errConsentNotFound")})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// purgeUserOAuthServerData 销户时清理：本人的授权记录 / 授权码，及其名下应用（连带别人对这些应用的授权）
func purgeUserOAuthServerData(tx *gorm.DB, userID uint) error {
	var clientIDs []string
	if err := tx.Model(&model.OAuthClient{}).Where("owner_id = ?", userID).Pluck("client_id", &clientIDs).Error; err != nil {
		return err
	}
	if len(clientIDs) > 0 {
		for _, m := range []any{&model.OAuthConsent{}, &model.OAuthCode{}, &model.OAuthRefreshToken{}} {
			if err := tx.Where("client_id IN ?", clientIDs).Delete(m).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("owner_id = ?", userID).Delete(&model.OAuthClient{}).Error; err != nil {
			return err
		}
	}
	for _, m := range []any{&model.OAuthConsent{}, &model.OAuthCode{}, &model.OAuthRefreshToken{}} {
		if err := tx.Where("user_id = ?", userID).Delete(m).Error; err != nil {
			return err
		}
	}
	return nil
}

// ---------- 站内消息 ----------

func (a *App) notifyOAuthAppPending(app *model.OAuthClient, owner *model.User) {
	var admins []model.User
	a.DB.Select("id").Where("role = ? AND status = ?", model.RoleAdmin, model.StatusEnabled).Find(&admins)
	for _, ad := range admins {
		if ad.ID == owner.ID {
			continue
		}
		a.notify(notifyPayload{
			UserID: ad.ID, Kind: MsgOAuthApp,
			Title:   fmt.Sprintf("%s 提交了第三方应用「%s」，等待审核", owner.DisplayName(), app.Name),
			Link:    "/console/oauth-apps?status=" + model.OAuthClientPending,
			RefType: "oauth_app", RefID: app.ID,
		})
	}
}

func (a *App) notifyOAuthAppReviewed(app *model.OAuthClient, status, reason string) {
	title := ""
	switch status {
	case model.OAuthClientApproved:
		title = fmt.Sprintf("你的第三方应用「%s」已通过审核", app.Name)
	case model.OAuthClientRejected:
		title = fmt.Sprintf("你的第三方应用「%s」未通过审核", app.Name)
	case model.OAuthClientDisabled:
		title = fmt.Sprintf("你的第三方应用「%s」已被停用", app.Name)
	}
	a.notify(notifyPayload{
		UserID: app.OwnerID, Kind: MsgOAuthApp, Title: title, Body: reason,
		Link: "/console/oauth-apps", RefType: "oauth_app", RefID: app.ID,
	})
}
