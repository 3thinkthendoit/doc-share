package handler

// OAuth 2.0 / OpenID Connect 授权服务器：第三方应用「使用 DocShare 账号登录」（参考 think-tpad oauth_server*.go）。
//
//	GET  /oauth/authorize   授权同意页（未登录先去登录再回来；已授权且 scope 覆盖则直接回跳）
//	POST /oauth/authorize   用户确认 / 拒绝
//	POST /oauth/token       authorization_code（支持 PKCE）/ refresh_token（轮换 + 重放检测）
//	GET  /oauth/userinfo    凭 Bearer access_token 取用户资料（scope 控制字段可见性）
//	POST /oauth/revoke      撤销刷新令牌（RFC 7009）
//	OIDC：scope 含 openid 时额外返回 RS256 id_token，发现文档 / JWKS 见 oauth_oidc.go
//
// 安全边界：第三方 access_token 与站内登录会话完全隔离——独立派生密钥签名、aud=client_id、2 小时有效，
// 站内中间件不认它，它也不能调用站内任何接口；每次取资料都复查用户状态、授权与应用状态。

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	oauthSrvCodeTTL     = 10 * time.Minute
	oauthSrvTokenTTL    = 2 * time.Hour
	oauthSrvRefreshTTL  = 30 * 24 * time.Hour // 刷新令牌有效期；每次使用即轮换并重新计时
	oauthSrvConsentTTL  = 10 * time.Minute    // 同意页表单有效期
	oauthSrvIssuer      = "doc-share-oauth"   // access_token 内部签发者（与 OIDC issuer 无关）
	oauthSrvMaxRedirect = 10
	oauthSrvMaxState    = 512
	oauthSrvMaxNonce    = 256
	oauthSrvTokenRate   = 100 // 每应用每分钟换票上限

	oauthScopeBasic   = "basic"          // 本站原有：基本资料（始终隐含）
	oauthScopeEmail   = "email"          // 邮箱
	oauthScopeOpenID  = "openid"         // OIDC：返回 id_token
	oauthScopeProfile = "profile"        // OIDC 标准名，等同 basic
	oauthScopeOffline = "offline_access" // 签发刷新令牌

	// 登录后回跳：只接受 /oauth/authorize?...，杜绝开放重定向
	loginNextCookie = "ds_next"
	loginNextTTL    = 10 * time.Minute
)

var oauthSrvScopes = map[string]bool{
	oauthScopeBasic: true, oauthScopeEmail: true, oauthScopeOpenID: true, oauthScopeProfile: true, oauthScopeOffline: true,
}

var (
	pkceChallengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)       // base64url(SHA-256) 恰好 43 字符
	pkceVerifierRe  = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`) // RFC 7636 §4.1
)

// ---------- 通用工具 ----------

func hashOAuthSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newOAuthToken(prefix string, n int) (string, error) {
	h, err := randomHex(n)
	if err != nil {
		return "", err
	}
	return prefix + h, nil
}

// parseOAuthScopes 空格分隔；空 = basic。basic 始终隐含并排第一，其余按请求顺序去重。
func parseOAuthScopes(raw string) ([]string, bool) {
	out := []string{oauthScopeBasic}
	seen := map[string]bool{oauthScopeBasic: true}
	for _, p := range strings.Fields(raw) {
		if !oauthSrvScopes[p] {
			return nil, false
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, true
}

func oauthHasScope(scope, want string) bool {
	return strings.Contains(" "+scope+" ", " "+want+" ")
}

// consentCovers 既有授权是否覆盖本次请求的 scope
func consentCovers(granted, requested string) bool {
	set := map[string]bool{}
	for _, s := range strings.Fields(granted) {
		set[s] = true
	}
	for _, s := range strings.Fields(requested) {
		if !set[s] {
			return false
		}
	}
	return true
}

// validateOAuthRedirectURIs 回调白名单：绝对 http(s)、无 fragment、无用户信息；http 仅允许本机调试地址。
func validateOAuthRedirectURIs(raw string) ([]string, string) {
	seen := map[string]bool{}
	var uris []string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		u := strings.TrimSpace(line)
		if u == "" || seen[u] {
			continue
		}
		p, err := url.Parse(u)
		if err != nil || p.Host == "" || (p.Scheme != "http" && p.Scheme != "https") || p.Fragment != "" || p.User != nil || len(u) > 500 {
			return nil, "oauthsrv.errRedirectInvalid"
		}
		if p.Scheme == "http" && !isLoopbackHost(p.Hostname()) {
			return nil, "oauthsrv.errRedirectHTTP"
		}
		seen[u] = true
		uris = append(uris, u)
	}
	if len(uris) == 0 {
		return nil, "oauthsrv.errRedirectRequired"
	}
	if len(uris) > oauthSrvMaxRedirect {
		return nil, "oauthsrv.errRedirectTooMany"
	}
	return uris, ""
}

func isLoopbackHost(h string) bool {
	h = strings.ToLower(h)
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasSuffix(h, ".localhost")
}

// oauthRedirectAllowed 回调地址精确匹配（不做前缀 / 子串匹配）
func oauthRedirectAllowed(client *model.OAuthClient, redirectURI string) bool {
	redirectURI = strings.TrimSpace(redirectURI)
	if redirectURI == "" {
		return false
	}
	for _, line := range strings.Split(client.RedirectURIs, "\n") {
		if strings.TrimSpace(line) == redirectURI {
			return true
		}
	}
	return false
}

func appendOAuthQuery(redirectURI string, q url.Values) string {
	sep := "?"
	if strings.Contains(redirectURI, "?") {
		sep = "&"
	}
	return redirectURI + sep + q.Encode()
}

// loadApprovedOAuthClient 取已审核通过的应用；不存在 / 未通过返回 nil
func (a *App) loadApprovedOAuthClient(clientID string) *model.OAuthClient {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || len(clientID) > 64 {
		return nil
	}
	var cl model.OAuthClient
	if err := a.DB.Where("client_id = ?", clientID).First(&cl).Error; err != nil {
		return nil
	}
	if cl.Status != model.OAuthClientApproved {
		return nil
	}
	return &cl
}

// activeOAuthConsent 用户对应用的有效授权（未撤销）
func activeOAuthConsent(db *gorm.DB, userID uint, clientID string) *model.OAuthConsent {
	var cs model.OAuthConsent
	if err := db.Where("user_id = ? AND client_id = ? AND revoked_at IS NULL", userID, clientID).
		Order("id DESC").First(&cs).Error; err != nil {
		return nil
	}
	return &cs
}

// ---------- 登录后回跳（未登录访问授权页 → 登录 → 回到授权页） ----------

func safeLoginNext(next string) bool {
	if len(next) > 4096 || !strings.HasPrefix(next, "/oauth/authorize?") {
		return false
	}
	return !strings.ContainsAny(next, "\\\r\n\t ")
}

func (a *App) setLoginNext(c *gin.Context, next string) {
	if safeLoginNext(next) {
		a.setCookie(c, loginNextCookie, url.QueryEscape(next), int(loginNextTTL.Seconds()))
	}
}

// takeLoginNext 读取并清除回跳地址；非法值一律丢弃
func (a *App) takeLoginNext(c *gin.Context) string {
	raw, err := c.Cookie(loginNextCookie)
	if err != nil || raw == "" {
		return ""
	}
	a.setCookie(c, loginNextCookie, "", -1)
	next, err := url.QueryUnescape(raw)
	if err != nil || !safeLoginNext(next) {
		return ""
	}
	return next
}

// ---------- 授权同意页 ----------

type oauthAuthorizeReq struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	Scope               string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Nonce               string
	Prompt              string
}

func readOAuthAuthorizeReq(get func(string) string) oauthAuthorizeReq {
	return oauthAuthorizeReq{
		ResponseType:        strings.TrimSpace(get("response_type")),
		ClientID:            strings.TrimSpace(get("client_id")),
		RedirectURI:         strings.TrimSpace(get("redirect_uri")),
		Scope:               strings.TrimSpace(get("scope")),
		State:               get("state"),
		CodeChallenge:       strings.TrimSpace(get("code_challenge")),
		CodeChallengeMethod: strings.TrimSpace(get("code_challenge_method")),
		Nonce:               get("nonce"),
		Prompt:              strings.TrimSpace(get("prompt")),
	}
}

// authorizeURL 还原授权请求地址；dropLogin=true 去掉 prompt=login（重新登录回来后不再循环）
func (r oauthAuthorizeReq) authorizeURL(dropLogin bool) string {
	q := url.Values{"response_type": {r.ResponseType}, "client_id": {r.ClientID}, "redirect_uri": {r.RedirectURI}}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("scope", r.Scope)
	set("state", r.State)
	set("code_challenge", r.CodeChallenge)
	set("code_challenge_method", r.CodeChallengeMethod)
	set("nonce", r.Nonce)
	prompt := r.Prompt
	if dropLogin {
		var keep []string
		for _, p := range strings.Fields(prompt) {
			if p != "login" {
				keep = append(keep, p)
			}
		}
		prompt = strings.Join(keep, " ")
	}
	set("prompt", prompt)
	return "/oauth/authorize?" + q.Encode()
}

// oauthAuthorizeError 应用或回调地址不可信：不能回跳，只能在本站展示错误（RFC 6749 §4.1.2.1）
func (a *App) oauthAuthorizeError(c *gin.Context, status int, key string) {
	a.renderOAuthPage(c, status, gin.H{"title": a.tr(c, "oauthsrv.title"), "oauthError": a.tr(c, key)})
}

// oauthRedirectError 回调地址已校验：错误回跳给第三方，由其展示
func oauthRedirectError(c *gin.Context, redirectURI, code, desc, state string) {
	q := url.Values{}
	q.Set("error", code)
	if desc != "" {
		q.Set("error_description", desc)
	}
	if state != "" {
		q.Set("state", state)
	}
	c.Redirect(http.StatusFound, appendOAuthQuery(redirectURI, q))
}

// renderOAuthPage 授权页禁止被嵌入（防点击劫持诱导点「授权」）
func (a *App) renderOAuthPage(c *gin.Context, status int, data gin.H) {
	c.Header("X-Frame-Options", "DENY")
	c.Header("Content-Security-Policy", "frame-ancestors 'none'")
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	data["httpStatus"] = status
	a.render(c, "oauth_authorize.html", data)
}

type authorizeCtx struct {
	client  *model.OAuthClient
	scopes  []string
	prompts map[string]bool
}

// validateAuthorizeReq 校验到「可安全回跳」为止；ok=false 时已写响应
func (a *App) validateAuthorizeReq(c *gin.Context, r oauthAuthorizeReq) (authorizeCtx, bool) {
	var ac authorizeCtx
	client := a.loadApprovedOAuthClient(r.ClientID)
	if client == nil {
		a.oauthAuthorizeError(c, http.StatusBadRequest, "oauthsrv.errClient")
		return ac, false
	}
	if !oauthRedirectAllowed(client, r.RedirectURI) {
		a.oauthAuthorizeError(c, http.StatusBadRequest, "oauthsrv.errRedirectMismatch")
		return ac, false
	}
	// 以下错误回跳给第三方
	fail := func(code, desc string) (authorizeCtx, bool) {
		oauthRedirectError(c, r.RedirectURI, code, desc, r.State)
		return ac, false
	}
	if r.ResponseType != "code" {
		return fail("unsupported_response_type", "only response_type=code is supported")
	}
	if len(r.State) > oauthSrvMaxState {
		oauthRedirectError(c, r.RedirectURI, "invalid_request", "state too long", "")
		return ac, false
	}
	if len(r.Nonce) > oauthSrvMaxNonce {
		return fail("invalid_request", "nonce too long")
	}
	scopes, ok := parseOAuthScopes(r.Scope)
	if !ok {
		return fail("invalid_scope", "supported scopes: openid profile email offline_access basic")
	}
	// PKCE：只支持 S256（plain 无防护意义）；公开客户端必须使用
	switch {
	case r.CodeChallenge == "" && r.CodeChallengeMethod != "":
		return fail("invalid_request", "code_challenge is required with code_challenge_method")
	case r.CodeChallenge != "" && r.CodeChallengeMethod != "S256":
		return fail("invalid_request", "code_challenge_method must be S256")
	case r.CodeChallenge != "" && !pkceChallengeRe.MatchString(r.CodeChallenge):
		return fail("invalid_request", "malformed code_challenge")
	case r.CodeChallenge == "" && client.IsPublic():
		return fail("invalid_request", "PKCE (code_challenge, S256) is required for public clients")
	}
	prompts := map[string]bool{}
	for _, p := range strings.Fields(r.Prompt) {
		switch p {
		case "none", "login", "consent", "select_account":
			prompts[p] = true
		default:
			return fail("invalid_request", "unsupported prompt value: "+p)
		}
	}
	if prompts["none"] && len(prompts) > 1 {
		return fail("invalid_request", "prompt=none cannot be combined with other values")
	}
	return authorizeCtx{client: client, scopes: scopes, prompts: prompts}, true
}

// OAuthAuthorizePage GET /oauth/authorize
func (a *App) OAuthAuthorizePage(c *gin.Context) {
	r := readOAuthAuthorizeReq(c.Query)
	ac, ok := a.validateAuthorizeReq(c, r)
	if !ok {
		return
	}
	client, scopes := ac.client, ac.scopes
	scopeStr := strings.Join(scopes, " ")
	user := middleware.CurrentUser(c)
	covered := func() bool {
		cs := activeOAuthConsent(a.DB, user.ID, client.ClientID)
		return cs != nil && consentCovers(cs.Scopes, scopeStr)
	}

	// prompt=none：不得展示任何页面（静默续登），条件不满足直接回跳错误
	if ac.prompts["none"] {
		switch {
		case user == nil:
			oauthRedirectError(c, r.RedirectURI, "login_required", "user is not signed in", r.State)
		case !covered():
			oauthRedirectError(c, r.RedirectURI, "consent_required", "user consent is required", r.State)
		default:
			a.oauthIssueCodeRedirect(c, client, user, scopeStr, r, false)
		}
		return
	}
	// prompt=login：强制重新登录；登录后回到去掉 prompt=login 的同一请求，避免循环
	if ac.prompts["login"] {
		a.setCookie(c, middleware.CookieSession, "", -1)
		a.setLoginNext(c, r.authorizeURL(true))
		c.Redirect(http.StatusFound, "/login")
		return
	}
	if user == nil {
		a.setLoginNext(c, c.Request.URL.RequestURI())
		c.Redirect(http.StatusFound, "/login")
		return
	}
	// 已授权且覆盖本次 scope：直接签发 code 回跳（与 GitHub 行为一致）；prompt=consent 强制再确认
	if !ac.prompts["consent"] && covered() {
		a.oauthIssueCodeRedirect(c, client, user, scopeStr, r, false)
		return
	}
	var owner model.User
	_ = a.DB.Select("id", "username", "nickname").First(&owner, client.OwnerID).Error
	redirectHost := ""
	if p, err := url.Parse(r.RedirectURI); err == nil {
		redirectHost = p.Host
	}
	a.renderOAuthPage(c, http.StatusOK, gin.H{
		"title":        a.tr(c, "oauthsrv.title"),
		"client":       client,
		"clientOwner":  owner.DisplayName(),
		"scopes":       scopes,
		"hasEmail":     oauthHasScope(scopeStr, oauthScopeEmail),
		"hasOffline":   oauthHasScope(scopeStr, oauthScopeOffline),
		"req":          r,
		"scopeStr":     scopeStr,
		"csrf":         a.mintConsentToken(user.ID, client.ClientID),
		"redirectHost": redirectHost,
	})
}

// OAuthAuthorizeSubmit POST /oauth/authorize
func (a *App) OAuthAuthorizeSubmit(c *gin.Context) {
	r := readOAuthAuthorizeReq(c.PostForm)
	r.Prompt = "" // 同意页提交不再处理 prompt
	ac, ok := a.validateAuthorizeReq(c, r)
	if !ok {
		return
	}
	user := middleware.CurrentUser(c)
	if user == nil {
		// 停留在同意页期间会话过期：登录后回到授权页重新确认
		a.setLoginNext(c, r.authorizeURL(false))
		c.Redirect(http.StatusFound, "/login")
		return
	}
	if !a.verifyConsentToken(c.PostForm("csrf"), user.ID, ac.client.ClientID) {
		a.oauthAuthorizeError(c, http.StatusForbidden, "oauthsrv.errExpired")
		return
	}
	if c.PostForm("decision") != "allow" {
		oauthRedirectError(c, r.RedirectURI, "access_denied", "the user denied the request", r.State)
		return
	}
	a.oauthIssueCodeRedirect(c, ac.client, user, strings.Join(ac.scopes, " "), r, true)
}

// oauthIssueCodeRedirect 写 / 续授权记录、签发一次性 code 并回跳第三方
func (a *App) oauthIssueCodeRedirect(c *gin.Context, client *model.OAuthClient, user *model.User, scopeStr string, r oauthAuthorizeReq, writeConsent bool) {
	code, err := newOAuthToken("dsc_", 24)
	if err != nil {
		a.oauthAuthorizeError(c, http.StatusInternalServerError, "oauthsrv.errServer")
		return
	}
	now := time.Now()
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if writeConsent {
			res := tx.Model(&model.OAuthConsent{}).
				Where("user_id = ? AND client_id = ? AND revoked_at IS NULL", user.ID, client.ClientID).
				Updates(map[string]any{"scopes": scopeStr, "granted_at": now})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				if err := tx.Create(&model.OAuthConsent{UserID: user.ID, ClientID: client.ClientID,
					Scopes: scopeStr, GrantedAt: now}).Error; err != nil {
					return err
				}
			}
		}
		// 顺手清理一天前过期的授权码 / 刷新令牌，避免表无限增长
		if err := tx.Where("expires_at < ?", now.Add(-24*time.Hour)).Delete(&model.OAuthCode{}).Error; err != nil {
			return err
		}
		if err := tx.Where("expires_at < ?", now.Add(-24*time.Hour)).Delete(&model.OAuthRefreshToken{}).Error; err != nil {
			return err
		}
		return tx.Create(&model.OAuthCode{
			CodeHash: hashOAuthSecret(code), ClientID: client.ClientID, UserID: user.ID,
			Scope: scopeStr, RedirectURI: r.RedirectURI, ExpiresAt: now.Add(oauthSrvCodeTTL),
			CodeChallenge: r.CodeChallenge, CodeChallengeMethod: r.CodeChallengeMethod, Nonce: r.Nonce,
		}).Error
	})
	if err != nil {
		log.Printf("[oauth-server] issue code client=%s user=%d: %v", client.ClientID, user.ID, err)
		a.oauthAuthorizeError(c, http.StatusInternalServerError, "oauthsrv.errServer")
		return
	}
	q := url.Values{}
	q.Set("code", code)
	if r.State != "" {
		q.Set("state", r.State)
	}
	c.Redirect(http.StatusFound, appendOAuthQuery(r.RedirectURI, q))
}

// mintConsentToken 同意页表单令牌：绑定用户 + 应用 + 过期时间（防 CSRF 强行授权）
func (a *App) mintConsentToken(uid uint, clientID string) string {
	nonce, _ := randomHex(8)
	exp := time.Now().Add(oauthSrvConsentTTL).Unix()
	return a.Signer.Sign(strings.Join([]string{"oauth-consent", strconv.FormatUint(uint64(uid), 10), clientID,
		strconv.FormatInt(exp, 10), nonce}, "|"))
}

func (a *App) verifyConsentToken(tok string, uid uint, clientID string) bool {
	payload, ok := a.Signer.Verify(strings.TrimSpace(tok))
	if !ok {
		return false
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 5 || parts[0] != "oauth-consent" ||
		parts[1] != strconv.FormatUint(uint64(uid), 10) || parts[2] != clientID {
		return false
	}
	exp, err := strconv.ParseInt(parts[3], 10, 64)
	return err == nil && time.Now().Unix() <= exp
}

// ---------- token 端点 ----------

type oauthTokenErr struct {
	status int
	code   string
	desc   string
}

func (e *oauthTokenErr) Error() string { return e.code + ": " + e.desc }

func tokenErr(status int, code, desc string) *oauthTokenErr {
	return &oauthTokenErr{status: status, code: code, desc: desc}
}

// oauthClientCredentials client_secret_basic（RFC 6749 §2.3.1，值经 form-urlencoded）优先，其次表单 / JSON
func oauthClientCredentials(c *gin.Context, body map[string]string) (id, secret string, basic bool) {
	if u, p, ok := c.Request.BasicAuth(); ok {
		if du, err := url.QueryUnescape(u); err == nil {
			u = du
		}
		if dp, err := url.QueryUnescape(p); err == nil {
			p = dp
		}
		return strings.TrimSpace(u), strings.TrimSpace(p), true
	}
	return strings.TrimSpace(body["client_id"]), strings.TrimSpace(body["client_secret"]), false
}

var oauthTokenBodyKeys = []string{"grant_type", "code", "redirect_uri", "client_id", "client_secret",
	"code_verifier", "refresh_token", "scope", "token", "token_type_hint"}

func readOAuthTokenBody(c *gin.Context) map[string]string {
	out := map[string]string{}
	if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		var m map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)).Decode(&m); err == nil {
			for k, v := range m {
				if s, ok := v.(string); ok {
					out[k] = s
				}
			}
		}
		return out
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	for _, k := range oauthTokenBodyKeys {
		out[k] = c.PostForm(k)
	}
	return out
}

func writeOAuthTokenErr(c *gin.Context, err error, clientID string, basic bool, endpoint string) {
	var te *oauthTokenErr
	if !errors.As(err, &te) {
		log.Printf("[oauth-server] %s client=%s: %v", endpoint, clientID, err)
		te = tokenErr(http.StatusInternalServerError, "server_error", "internal error")
	} else {
		log.Printf("[oauth-server] %s rejected client=%s error=%s", endpoint, clientID, te.code)
	}
	if te.code == "invalid_client" && basic {
		c.Header("WWW-Authenticate", `Basic realm="oauth"`)
	}
	c.JSON(te.status, gin.H{"error": te.code, "error_description": te.desc})
}

// OAuthToken POST /oauth/token
func (a *App) OAuthToken(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	body := readOAuthTokenBody(c)
	clientID, secret, basic := oauthClientCredentials(c, body)
	grant := strings.TrimSpace(body["grant_type"])
	var (
		res gin.H
		err error
	)
	if grant != "authorization_code" && grant != "refresh_token" {
		err = tokenErr(http.StatusBadRequest, "unsupported_grant_type", "supported: authorization_code, refresh_token")
	} else {
		var client *model.OAuthClient
		if client, err = a.authenticateOAuthClient(clientID, secret); err == nil {
			if grant == "authorization_code" {
				res, err = a.grantAuthorizationCode(c, client, body)
			} else {
				res, err = a.grantRefreshToken(c, client, body)
			}
		}
	}
	if err != nil {
		writeOAuthTokenErr(c, err, clientID, basic, "token")
		return
	}
	c.JSON(http.StatusOK, res)
}

// authenticateOAuthClient 服务端应用校验 Secret；公开客户端只凭 client_id（安全性由 PKCE 保证）
func (a *App) authenticateOAuthClient(clientID, secret string) (*model.OAuthClient, error) {
	if clientID == "" || len(clientID) > 64 {
		return nil, tokenErr(http.StatusUnauthorized, "invalid_client", "client authentication required")
	}
	var client model.OAuthClient
	if err := a.DB.Where("client_id = ?", clientID).First(&client).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, tokenErr(http.StatusUnauthorized, "invalid_client", "unknown client")
		}
		return nil, err
	}
	if client.IsPublic() {
		if secret != "" {
			return nil, tokenErr(http.StatusUnauthorized, "invalid_client", "public clients must not use a client secret")
		}
	} else if len(secret) == 0 || client.ClientSecretHash == "" ||
		!hmac.Equal([]byte(client.ClientSecretHash), []byte(hashOAuthSecret(secret))) {
		return nil, tokenErr(http.StatusUnauthorized, "invalid_client", "client authentication failed")
	}
	if client.Status != model.OAuthClientApproved {
		return nil, tokenErr(http.StatusUnauthorized, "invalid_client", "client is not approved or disabled")
	}
	if !allowOAuthTokenRate(client.ID) {
		return nil, tokenErr(http.StatusTooManyRequests, "temporarily_unavailable", "rate limit exceeded")
	}
	return &client, nil
}

// pkceVerify RFC 7636 §4.6：BASE64URL(SHA256(code_verifier)) == code_challenge
func pkceVerify(verifier, challenge string) bool {
	if !pkceVerifierRe.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

func (a *App) grantAuthorizationCode(c *gin.Context, client *model.OAuthClient, body map[string]string) (gin.H, error) {
	code := strings.TrimSpace(body["code"])
	redirectURI := strings.TrimSpace(body["redirect_uri"])
	verifier := strings.TrimSpace(body["code_verifier"])
	if code == "" || redirectURI == "" {
		return nil, tokenErr(http.StatusBadRequest, "invalid_request", "code and redirect_uri are required")
	}
	var row model.OAuthCode
	if err := a.DB.Where("code_hash = ? AND client_id = ?", hashOAuthSecret(code), client.ClientID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "invalid authorization code")
		}
		return nil, err
	}
	if row.RedirectURI != redirectURI {
		return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
	}
	// PKCE 校验在消耗 code 之前：猜错 verifier 不能把合法用户的 code 烧掉
	switch {
	case row.CodeChallenge != "" && !pkceVerify(verifier, row.CodeChallenge):
		return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
	case row.CodeChallenge == "" && verifier != "":
		return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "code_verifier sent but no code_challenge was used")
	case row.CodeChallenge == "" && client.IsPublic():
		return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "PKCE is required for public clients")
	}
	now := time.Now()
	// 一次性：条件更新抢占，并发重放只有一个能成功
	upd := a.DB.Model(&model.OAuthCode{}).
		Where("id = ? AND used_at IS NULL AND expires_at > ?", row.ID, now).
		Update("used_at", now)
	if upd.Error != nil {
		return nil, upd.Error
	}
	if upd.RowsAffected == 0 {
		return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "authorization code expired or already used")
	}
	user, err := a.oauthGrantUser(row.UserID, client.ClientID)
	if err != nil {
		return nil, err
	}
	return a.issueOAuthTokens(c, client, user, row.Scope, row.Scope, row.Nonce, "", now)
}

// oauthGrantUser 签发前复查：授权未撤销、用户可用
func (a *App) oauthGrantUser(uid uint, clientID string) (*model.User, error) {
	if activeOAuthConsent(a.DB, uid, clientID) == nil {
		return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "consent revoked")
	}
	var user model.User
	if err := a.DB.First(&user, uid).Error; err != nil || user.Status != model.StatusEnabled {
		return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "user unavailable")
	}
	return &user, nil
}

// revokeRefreshFamily 作废一次授权派生的整条刷新令牌链
func revokeRefreshFamily(db *gorm.DB, familyID string) error {
	return db.Model(&model.OAuthRefreshToken{}).Where("family_id = ? AND revoked_at IS NULL", familyID).
		Update("revoked_at", time.Now()).Error
}

func (a *App) grantRefreshToken(c *gin.Context, client *model.OAuthClient, body map[string]string) (gin.H, error) {
	rt := strings.TrimSpace(body["refresh_token"])
	if rt == "" {
		return nil, tokenErr(http.StatusBadRequest, "invalid_request", "refresh_token is required")
	}
	var row model.OAuthRefreshToken
	if err := a.DB.Where("token_hash = ? AND client_id = ?", hashOAuthSecret(rt), client.ClientID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "invalid refresh token")
		}
		return nil, err
	}
	now := time.Now()
	reuse := func() (gin.H, error) {
		// 已轮换掉的旧令牌又被使用：很可能已泄露，整条链作废（RFC 6819 §5.2.2.3 / OAuth 2.1）
		if err := revokeRefreshFamily(a.DB, row.FamilyID); err != nil {
			return nil, err
		}
		log.Printf("[oauth-server] refresh token reuse detected client=%s user=%d family=%s", client.ClientID, row.UserID, row.FamilyID)
		return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "refresh token has been used or revoked")
	}
	switch {
	case row.RevokedAt != nil:
		return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "refresh token has been used or revoked")
	case row.UsedAt != nil:
		return reuse()
	case !now.Before(row.ExpiresAt):
		return nil, tokenErr(http.StatusBadRequest, "invalid_grant", "refresh token expired")
	}
	// 可选 scope：只能收窄，不能超出原授权
	scope := row.Scope
	if req := strings.TrimSpace(body["scope"]); req != "" {
		scopes, ok := parseOAuthScopes(req)
		if !ok || !consentCovers(row.Scope, strings.Join(scopes, " ")) {
			return nil, tokenErr(http.StatusBadRequest, "invalid_scope", "requested scope exceeds the original grant")
		}
		scope = strings.Join(scopes, " ")
	}
	upd := a.DB.Model(&model.OAuthRefreshToken{}).
		Where("id = ? AND used_at IS NULL AND revoked_at IS NULL", row.ID).Update("used_at", now)
	if upd.Error != nil {
		return nil, upd.Error
	}
	if upd.RowsAffected == 0 {
		return reuse() // 并发使用同一令牌
	}
	user, err := a.oauthGrantUser(row.UserID, client.ClientID)
	if err != nil {
		_ = revokeRefreshFamily(a.DB, row.FamilyID)
		return nil, err
	}
	return a.issueOAuthTokens(c, client, user, scope, row.Scope, "", row.FamilyID, now)
}

// issueOAuthTokens 签发 access_token；refreshScope 含 offline_access 时签发（轮换后的）刷新令牌；
// accessScope 含 openid 时附 id_token。familyID 为空表示新的授权链。
func (a *App) issueOAuthTokens(c *gin.Context, client *model.OAuthClient, user *model.User, accessScope, refreshScope, nonce, familyID string, now time.Time) (gin.H, error) {
	access := a.signOAuthAccessToken(user.ID, client.ClientID, accessScope, now)
	out := gin.H{
		"access_token": access,
		"token_type":   "Bearer",
		"expires_in":   int(oauthSrvTokenTTL.Seconds()),
		"scope":        accessScope,
	}
	if oauthHasScope(refreshScope, oauthScopeOffline) {
		rt, err := newOAuthToken("dsr_", 32)
		if err != nil {
			return nil, err
		}
		if familyID == "" {
			if familyID, err = randomHex(12); err != nil {
				return nil, err
			}
		}
		if err := a.DB.Create(&model.OAuthRefreshToken{
			TokenHash: hashOAuthSecret(rt), FamilyID: familyID, ClientID: client.ClientID, UserID: user.ID,
			Scope: refreshScope, ExpiresAt: now.Add(oauthSrvRefreshTTL),
		}).Error; err != nil {
			return nil, err
		}
		out["refresh_token"] = rt
	}
	if oauthHasScope(accessScope, oauthScopeOpenID) {
		idt, err := a.signIDToken(c, user, client.ClientID, accessScope, nonce, access, now)
		if err != nil {
			return nil, err
		}
		out["id_token"] = idt
	}
	return out, nil
}

// OAuthRevoke POST /oauth/revoke（RFC 7009）：撤销刷新令牌（连同整条链）。
// access_token 为 2 小时自包含令牌，不单独吊销；需要立即切断时用户可撤销授权。
// 按规范，令牌无效 / 不属于该应用时同样返回 200，避免被用来探测令牌。
func (a *App) OAuthRevoke(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	body := readOAuthTokenBody(c)
	clientID, secret, basic := oauthClientCredentials(c, body)
	client, err := a.authenticateOAuthClient(clientID, secret)
	if err != nil {
		writeOAuthTokenErr(c, err, clientID, basic, "revoke")
		return
	}
	tok := strings.TrimSpace(body["token"])
	if tok == "" {
		writeOAuthTokenErr(c, tokenErr(http.StatusBadRequest, "invalid_request", "token is required"), clientID, basic, "revoke")
		return
	}
	var row model.OAuthRefreshToken
	if err := a.DB.Where("token_hash = ? AND client_id = ?", hashOAuthSecret(tok), client.ClientID).First(&row).Error; err == nil {
		if err := revokeRefreshFamily(a.DB, row.FamilyID); err != nil {
			writeOAuthTokenErr(c, err, clientID, basic, "revoke")
			return
		}
	}
	c.Status(http.StatusOK)
}

// 每应用每分钟换票限流（进程内滑动窗口）
var (
	oauthTokenRateMu sync.Mutex
	oauthTokenHits   = map[uint][]time.Time{}
)

func allowOAuthTokenRate(clientID uint) bool {
	oauthTokenRateMu.Lock()
	defer oauthTokenRateMu.Unlock()
	now := time.Now()
	cut := now.Add(-time.Minute)
	kept := oauthTokenHits[clientID][:0]
	for _, t := range oauthTokenHits[clientID] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= oauthSrvTokenRate {
		oauthTokenHits[clientID] = kept
		return false
	}
	oauthTokenHits[clientID] = append(kept, now)
	return true
}

// ---------- access token（HS256 JWT，独立派生密钥） ----------

func (a *App) oauthTokenKey() []byte {
	return a.Signer.DeriveKey("oauth-server-access-token")
}

func (a *App) signOAuthAccessToken(uid uint, clientID, scope string, now time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{
		"iss": oauthSrvIssuer, "sub": strconv.FormatUint(uint64(uid), 10), "aud": clientID,
		"scope": scope, "iat": now.Unix(), "exp": now.Add(oauthSrvTokenTTL).Unix(),
	})
	signing := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, a.oauthTokenKey())
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *App) verifyOAuthAccessToken(tok string) (uid uint, clientID, scope string, err error) {
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if len(parts) != 3 {
		return 0, "", "", errors.New("malformed token")
	}
	hdr, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || string(hdr) != `{"alg":"HS256","typ":"JWT"}` {
		return 0, "", "", errors.New("bad header")
	}
	mac := hmac.New(sha256.New, a.oauthTokenKey())
	mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return 0, "", "", errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, "", "", errors.New("bad payload")
	}
	var cl struct {
		Iss   string `json:"iss"`
		Sub   string `json:"sub"`
		Aud   string `json:"aud"`
		Scope string `json:"scope"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &cl); err != nil || cl.Iss != oauthSrvIssuer || cl.Aud == "" {
		return 0, "", "", errors.New("bad claims")
	}
	if time.Now().Unix() >= cl.Exp {
		return 0, "", "", errors.New("token expired")
	}
	n, err := strconv.ParseUint(cl.Sub, 10, 64)
	if err != nil || n == 0 {
		return 0, "", "", errors.New("bad subject")
	}
	return uint(n), cl.Aud, cl.Scope, nil
}

// ---------- userinfo ----------

func bearerToken(c *gin.Context) string {
	h := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func oauthUserinfoErr(c *gin.Context, desc string) {
	// RFC 6750 §3：WWW-Authenticate: Bearer error="invalid_token", error_description="..."
	scheme := "Bear" + "er"
	c.Header("WWW-Authenticate", scheme+` error="invalid_token", error_description=`+strconv.Quote(desc))
	c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_token", "error_description": desc})
}

// OAuthUserInfo GET|POST /oauth/userinfo
func (a *App) OAuthUserInfo(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	tok := bearerToken(c)
	if tok == "" || len(tok) > 4096 {
		oauthUserinfoErr(c, "bearer token required")
		return
	}
	uid, clientID, scope, err := a.verifyOAuthAccessToken(tok)
	if err != nil {
		oauthUserinfoErr(c, err.Error())
		return
	}
	var user model.User
	if err := a.DB.First(&user, uid).Error; err != nil || user.Status != model.StatusEnabled {
		oauthUserinfoErr(c, "user unavailable")
		return
	}
	// 应用被停用 / 删除、用户撤销授权后，已签发的令牌立即失效
	if a.loadApprovedOAuthClient(clientID) == nil {
		oauthUserinfoErr(c, "client disabled")
		return
	}
	if activeOAuthConsent(a.DB, uid, clientID) == nil {
		oauthUserinfoErr(c, "consent revoked")
		return
	}
	c.JSON(http.StatusOK, a.userClaims(c, &user, scope))
}
