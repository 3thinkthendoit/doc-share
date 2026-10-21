package handler

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"doc-share/internal/config"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"
	"gorm.io/gorm"
)

const (
	oauthStateCookie = "ds_oauth_state"
	oauthStateTTL    = 10 * time.Minute
	oauthStatePrefix = "oauth"
)

// oauthProfile 提供方返回的规范化用户资料
type oauthProfile struct {
	Subject       string
	Email         string
	EmailVerified bool
	Login         string // 建议用户名（GitHub login / Google 本地部分）
	Nickname      string
	Avatar        string
}

// oauthFetcher 拉取资料（测试可替换）
type oauthFetcher func(ctx context.Context, cfg *oauth2.Config, code string) (oauthProfile, error)

var (
	fetchGitHubUser = defaultFetchGitHubUser
	fetchGoogleUser = defaultFetchGoogleUser
)

// mintOAuthState 签发带过期时间的 state（HMAC，多实例可用）；配合 Cookie 双提交防 CSRF。
func (a *App) mintOAuthState(provider string) (string, error) {
	if a == nil || a.Signer == nil {
		return "", errors.New("signer missing")
	}
	nonce, err := randomHex(16)
	if err != nil {
		return "", err
	}
	exp := time.Now().Add(oauthStateTTL).Unix()
	payload := strings.Join([]string{oauthStatePrefix, provider, strconv.FormatInt(exp, 10), nonce}, "|")
	return a.Signer.Sign(payload), nil
}

// parseOAuthState 校验签名与过期，返回 provider。
func (a *App) parseOAuthState(state string) (provider string, ok bool) {
	if a == nil || a.Signer == nil || state == "" {
		return "", false
	}
	payload, valid := a.Signer.Verify(state)
	if !valid {
		return "", false
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 4 || parts[0] != oauthStatePrefix {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	p := parts[1]
	if p != model.OAuthProviderGitHub && p != model.OAuthProviderGoogle {
		return "", false
	}
	return p, true
}

// OAuthConfigured 系统设置或 config 中是否齐备 ClientID + Secret
func (a *App) OAuthConfigured(provider string) bool {
	pcfg, ok := a.oauthProviderCfg(provider)
	if !ok {
		return false
	}
	return strings.TrimSpace(pcfg.ClientID) != "" && strings.TrimSpace(pcfg.ClientSecret) != ""
}

// OAuthEnabled 凭据齐全且后台开关打开
func (a *App) OAuthEnabled(provider string) bool {
	if !a.OAuthConfigured(provider) {
		return false
	}
	s := a.Settings()
	switch provider {
	case model.OAuthProviderGitHub:
		return s.OAuthGitHubEnabled
	case model.OAuthProviderGoogle:
		return s.OAuthGoogleEnabled
	default:
		return false
	}
}

// oauthProviderCfg 合并凭据：系统设置非空优先，否则用 config.yaml / 环境变量。
func (a *App) oauthProviderCfg(provider string) (config.OAuthProviderConfig, bool) {
	if a == nil {
		return config.OAuthProviderConfig{}, false
	}
	var out config.OAuthProviderConfig
	if a.Cfg != nil {
		switch provider {
		case model.OAuthProviderGitHub:
			out = a.Cfg.Auth.GitHub
		case model.OAuthProviderGoogle:
			out = a.Cfg.Auth.Google
		default:
			return config.OAuthProviderConfig{}, false
		}
	} else if provider != model.OAuthProviderGitHub && provider != model.OAuthProviderGoogle {
		return config.OAuthProviderConfig{}, false
	}
	s := a.Settings()
	switch provider {
	case model.OAuthProviderGitHub:
		if v := strings.TrimSpace(s.OAuthGitHubClientID); v != "" {
			out.ClientID = v
		}
		if v := strings.TrimSpace(s.OAuthGitHubClientSecret); v != "" {
			out.ClientSecret = v
		}
		if v := strings.TrimSpace(s.OAuthGitHubRedirectURL); v != "" {
			out.RedirectURL = v
		}
	case model.OAuthProviderGoogle:
		if v := strings.TrimSpace(s.OAuthGoogleClientID); v != "" {
			out.ClientID = v
		}
		if v := strings.TrimSpace(s.OAuthGoogleClientSecret); v != "" {
			out.ClientSecret = v
		}
		if v := strings.TrimSpace(s.OAuthGoogleRedirectURL); v != "" {
			out.RedirectURL = v
		}
	}
	return out, true
}

// oauthRequestOrigin 当前请求的协议+主机（含端口），用于与 state Cookie 同源。
func oauthRequestOrigin(c *gin.Context) (scheme, host string) {
	scheme = "http"
	if c != nil && c.Request != nil {
		if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		host = c.Request.Host
	}
	return scheme, host
}

func (a *App) oauthRedirectURL(c *gin.Context, provider string, pcfg config.OAuthProviderConfig) string {
	scheme, host := oauthRequestOrigin(c)
	reqCallback := scheme + "://" + host + "/auth/" + provider + "/callback"
	if u := strings.TrimSpace(pcfg.RedirectURL); u != "" {
		parsed, err := url.Parse(u)
		// 显式回调与当前请求同 Host 但协议不一致（常见：设置里 https://localhost，浏览器却用 http）：
		// 必须跟浏览器 Origin，否则 state Cookie 跨协议丢失。
		if err == nil && parsed.Host != "" && host != "" &&
			strings.EqualFold(parsed.Host, host) &&
			parsed.Scheme != "" && !strings.EqualFold(parsed.Scheme, scheme) {
			return reqCallback
		}
		return u
	}
	// 未配置显式回调：始终用当前浏览器 Origin（不用站点域名，避免 http/https 混用）
	return reqCallback
}

func (a *App) oauth2Config(c *gin.Context, provider string) (*oauth2.Config, error) {
	pcfg, ok := a.oauthProviderCfg(provider)
	if !ok || strings.TrimSpace(pcfg.ClientID) == "" {
		return nil, fmt.Errorf("oauth provider not configured")
	}
	redirect := a.oauthRedirectURL(c, provider, pcfg)
	switch provider {
	case model.OAuthProviderGitHub:
		return &oauth2.Config{
			ClientID:     strings.TrimSpace(pcfg.ClientID),
			ClientSecret: strings.TrimSpace(pcfg.ClientSecret),
			RedirectURL:  redirect,
			Scopes:       []string{"read:user", "user:email"},
			Endpoint:     github.Endpoint,
		}, nil
	case model.OAuthProviderGoogle:
		return &oauth2.Config{
			ClientID:     strings.TrimSpace(pcfg.ClientID),
			ClientSecret: strings.TrimSpace(pcfg.ClientSecret),
			RedirectURL:  redirect,
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     google.Endpoint,
		}, nil
	default:
		return nil, fmt.Errorf("unknown provider")
	}
}

// OAuthStart GET /auth/:provider — 跳转授权页
func (a *App) OAuthStart(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if !a.OAuthEnabled(provider) {
		a.oauthFail(c, "第三方登录未启用或未配置")
		return
	}
	cfg, err := a.oauth2Config(c, provider)
	if err != nil {
		a.oauthFail(c, "第三方登录未配置")
		return
	}
	state, err := a.mintOAuthState(provider)
	if err != nil {
		a.oauthFail(c, "无法发起登录")
		return
	}
	a.setCookie(c, oauthStateCookie, state, int(oauthStateTTL.Seconds()))
	c.Redirect(http.StatusFound, cfg.AuthCodeURL(state, oauth2.AccessTypeOnline))
}

// OAuthCallback GET /auth/:provider/callback
func (a *App) OAuthCallback(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if provider != model.OAuthProviderGitHub && provider != model.OAuthProviderGoogle {
		a.oauthFail(c, "不支持的登录方式")
		return
	}
	if errMsg := strings.TrimSpace(c.Query("error")); errMsg != "" {
		a.oauthFail(c, "已取消授权")
		return
	}
	state := strings.TrimSpace(c.Query("state"))
	code := strings.TrimSpace(c.Query("code"))
	cookieState, _ := c.Cookie(oauthStateCookie)
	a.setCookie(c, oauthStateCookie, "", -1)
	wantProvider, ok := a.parseOAuthState(state)
	if !ok || state == "" || state != cookieState || wantProvider != provider {
		a.oauthFail(c, "登录状态无效或已过期，请重试")
		return
	}
	if code == "" {
		a.oauthFail(c, "授权码缺失")
		return
	}
	if !a.OAuthEnabled(provider) {
		a.oauthFail(c, "第三方登录未启用或未配置")
		return
	}
	cfg, err := a.oauth2Config(c, provider)
	if err != nil {
		a.oauthFail(c, "第三方登录未配置")
		return
	}

	var profile oauthProfile
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	switch provider {
	case model.OAuthProviderGitHub:
		profile, err = fetchGitHubUser(ctx, cfg, code)
	case model.OAuthProviderGoogle:
		profile, err = fetchGoogleUser(ctx, cfg, code)
	}
	if err != nil || strings.TrimSpace(profile.Subject) == "" {
		log.Printf("[oauth] %s fetch profile failed: err=%v subject=%q redirect=%q", provider, err, profile.Subject, cfg.RedirectURL)
		msg := "获取用户信息失败"
		if err != nil {
			es := err.Error()
			switch {
			case strings.Contains(es, "exchange") || strings.Contains(es, "cannot fetch token") || strings.Contains(es, "oauth2:"):
				msg = "第三方登录换取令牌失败，请核对 Client Secret 与回调地址"
			case strings.Contains(es, "github user"):
				msg = "GitHub 用户信息获取失败，请检查网络或稍后重试"
			case strings.Contains(es, "google userinfo") || strings.Contains(es, "id_token"):
				msg = "Google 用户信息获取失败，请检查网络或稍后重试"
			}
		}
		a.oauthFail(c, msg)
		return
	}

	user, err := a.resolveOAuthUser(provider, profile)
	if err != nil {
		a.oauthFail(c, err.Error())
		return
	}
	if user.Status != model.StatusEnabled {
		a.oauthFail(c, "账号已被禁用")
		return
	}
	a.expireLoginLock(user)
	if user.Locked() {
		a.oauthFail(c, msgLocked)
		return
	}
	if a.Settings().TOTPEnabled {
		a.beginTOTP(c, user)
		return
	}
	a.issueSession(c, user)
}

func (a *App) oauthFail(c *gin.Context, msg string) {
	// msg 用中文原文作 i18n key；一次性 flash，地址栏保持 /login
	a.setFlash(c, a.tr(c, msg))
	c.Redirect(http.StatusFound, "/login")
}

// resolveOAuthUser：已绑定 → 用绑定用户；已验证邮箱命中本地用户 → 合并绑定；否则新建。
func (a *App) resolveOAuthUser(provider string, profile oauthProfile) (*model.User, error) {
	var ident model.OAuthIdentity
	err := a.DB.Where("provider = ? AND subject = ?", provider, profile.Subject).First(&ident).Error
	if err == nil {
		var user model.User
		if e := a.DB.First(&user, ident.UserID).Error; e != nil {
			return nil, errors.New("绑定账号不存在")
		}
		// 回写：仅补空头像；邮箱须已验证且未被他人占用
		updates := map[string]any{}
		if email := strings.TrimSpace(strings.ToLower(profile.Email)); email != "" && profile.EmailVerified && user.Email == "" {
			var n int64
			if err := a.DB.Model(&model.User{}).Where("LOWER(email) = ? AND id <> ?", email, user.ID).Count(&n).Error; err == nil && n == 0 {
				updates["email"] = email
				updates["email_verified"] = true
			}
		}
		if av := strings.TrimSpace(profile.Avatar); av != "" && user.Avatar == "" && validAvatar(av) {
			updates["avatar"] = av
		}
		if len(updates) > 0 {
			_ = a.DB.Model(&user).Updates(updates).Error
			_ = a.DB.First(&user, user.ID)
		}
		return &user, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("查询绑定失败")
	}

	email := strings.TrimSpace(strings.ToLower(profile.Email))
	if email != "" && profile.EmailVerified {
		var existing model.User
		// 同邮箱唯一命中才合并（应用层约定空串可重复；非空应唯一）
		var n int64
		if err := a.DB.Model(&model.User{}).Where("LOWER(email) = ?", email).Count(&n).Error; err != nil {
			return nil, errors.New("查询用户失败")
		}
		if n > 1 {
			return nil, errors.New("该邮箱对应多个账号，无法自动合并，请联系管理员")
		}
		if n == 1 {
			if e := a.DB.Where("LOWER(email) = ?", email).First(&existing).Error; e == nil {
				if !existing.EmailVerified {
					// 本地邮箱未验证：拒绝合并与新建，避免抢注邮箱接管或重复邮箱
					return nil, errors.New("该邮箱已被占用且未完成验证，请使用密码登录或联系管理员")
				}
				if existing.Status != model.StatusEnabled {
					return nil, errors.New("账号已被禁用")
				}
				if err := a.linkOAuthIdentity(provider, profile, existing.ID); err != nil {
					return nil, err
				}
				if av := strings.TrimSpace(profile.Avatar); existing.Avatar == "" && av != "" && validAvatar(av) {
					_ = a.DB.Model(&existing).Update("avatar", av).Error
					existing.Avatar = av
				}
				return &existing, nil
			} else if !errors.Is(e, gorm.ErrRecordNotFound) {
				return nil, errors.New("查询用户失败")
			}
		}
	}

	return a.createAndLinkOAuthUser(provider, profile)
}

func (a *App) createAndLinkOAuthUser(provider string, profile oauthProfile) (*model.User, error) {
	var out *model.User
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		user, err := a.createOAuthUserTx(tx, provider, profile)
		if err != nil {
			return err
		}
		if err := linkOAuthIdentityTx(tx, provider, profile, user.ID); err != nil {
			return err
		}
		out = user
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) linkOAuthIdentity(provider string, profile oauthProfile, userID uint) error {
	return linkOAuthIdentityTx(a.DB, provider, profile, userID)
}

func linkOAuthIdentityTx(db *gorm.DB, provider string, profile oauthProfile, userID uint) error {
	ident := model.OAuthIdentity{
		Provider: provider,
		Subject:  profile.Subject,
		UserID:   userID,
		Email:    strings.TrimSpace(strings.ToLower(profile.Email)),
	}
	if err := db.Create(&ident).Error; err != nil {
		return errors.New("绑定第三方账号失败")
	}
	return nil
}

func (a *App) createOAuthUser(provider string, profile oauthProfile) (*model.User, error) {
	return a.createOAuthUserTx(a.DB, provider, profile)
}

func (a *App) createOAuthUserTx(db *gorm.DB, provider string, profile oauthProfile) (*model.User, error) {
	base := profile.Login
	if base == "" {
		base = strings.Split(profile.Email, "@")[0]
	}
	if base == "" {
		base = provider + "_user"
	}
	username, err := a.allocOAuthUsernameTx(db, base)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, errors.New("创建账号失败")
	}
	hash, err := bcrypt.GenerateFromPassword(raw, bcrypt.DefaultCost)
	if err != nil {
		return nil, errors.New("创建账号失败")
	}
	nick := strings.TrimSpace(profile.Nickname)
	if nick == "" {
		nick = username
	}
	email := ""
	emailVerified := false
	if profile.EmailVerified {
		email = strings.TrimSpace(strings.ToLower(profile.Email))
		emailVerified = email != ""
	}
	avatar := strings.TrimSpace(profile.Avatar)
	if !validAvatar(avatar) {
		avatar = ""
	}
	user := model.User{
		Username:      username,
		PasswordHash:  string(hash),
		Nickname:      nick,
		Email:         email,
		EmailVerified: emailVerified,
		Avatar:        avatar,
		Role:          model.RoleViewer,
		Status:        model.StatusEnabled,
	}
	if err := db.Create(&user).Error; err != nil {
		return nil, errors.New("创建账号失败")
	}
	return &user, nil
}

var oauthUsernameRe = regexp.MustCompile(`[^a-zA-Z0-9_\-\.]+`)

func sanitizeOAuthUsername(s string) string {
	s = strings.TrimSpace(s)
	s = oauthUsernameRe.ReplaceAllString(s, "")
	s = strings.Trim(s, ".-_")
	if s == "" {
		return "user"
	}
	// 必须以字母数字开头，便于与站点其它规则兼容
	r := []rune(s)
	if !unicode.IsLetter(r[0]) && !unicode.IsDigit(r[0]) {
		s = "u" + s
	}
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}

func (a *App) allocOAuthUsername(base string) (string, error) {
	return a.allocOAuthUsernameTx(a.DB, base)
}

func (a *App) allocOAuthUsernameTx(db *gorm.DB, base string) (string, error) {
	base = sanitizeOAuthUsername(base)
	candidate := base
	for i := 0; i < 50; i++ {
		var n int64
		if err := db.Model(&model.User{}).Where("username = ?", candidate).Count(&n).Error; err != nil {
			return "", errors.New("创建账号失败")
		}
		if n == 0 {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s_%d", base, i+2)
		if len(candidate) > 64 {
			candidate = candidate[:64]
		}
	}
	suf, err := randomHex(3)
	if err != nil {
		return "", errors.New("创建账号失败")
	}
	return sanitizeOAuthUsername(base+"_"+suf), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func setGitHubAPIHeaders(req *http.Request) {
	// GitHub 强制要求 User-Agent，缺省会 403
	req.Header.Set("User-Agent", "DocShare-OAuth")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

func defaultFetchGitHubUser(ctx context.Context, cfg *oauth2.Config, code string) (oauthProfile, error) {
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("github token exchange: %w", err)
	}
	client := cfg.Client(ctx, tok)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return oauthProfile{}, err
	}
	setGitHubAPIHeaders(req)
	res, err := client.Do(req)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("github user: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		log.Printf("[oauth] github /user status=%s body=%q", res.Status, snippet)
		return oauthProfile{}, fmt.Errorf("github user: %s", res.Status)
	}
	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.ID == 0 {
		return oauthProfile{}, fmt.Errorf("github user parse")
	}
	// /user.email 不保证已验证；一律以 /user/emails 的 verified 为准
	email, verified := fetchGitHubPrimaryEmail(ctx, client)
	nick := u.Name
	if nick == "" {
		nick = u.Login
	}
	return oauthProfile{
		Subject:       fmt.Sprintf("%d", u.ID),
		Email:         email,
		EmailVerified: verified,
		Login:         u.Login,
		Nickname:      nick,
		Avatar:        u.AvatarURL,
	}, nil
}

func fetchGitHubPrimaryEmail(ctx context.Context, client *http.Client) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user/emails", nil)
	if err != nil {
		return "", false
	}
	setGitHubAPIHeaders(req)
	res, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", false
	}
	var list []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if json.Unmarshal(body, &list) != nil {
		return "", false
	}
	var fallback string
	for _, e := range list {
		if !e.Verified || e.Email == "" {
			continue
		}
		if e.Primary {
			return e.Email, true
		}
		if fallback == "" {
			fallback = e.Email
		}
	}
	if fallback != "" {
		return fallback, true
	}
	return "", false
}

func defaultFetchGoogleUser(ctx context.Context, cfg *oauth2.Config, code string) (oauthProfile, error) {
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("google token exchange: %w", err)
	}
	// 优先用换票返回的 id_token（OIDC，验签后用），避免再请求 userinfo
	if raw, ok := tok.Extra("id_token").(string); ok && strings.TrimSpace(raw) != "" {
		if p, perr := parseGoogleIDToken(ctx, raw, cfg.ClientID); perr == nil {
			return p, nil
		} else {
			log.Printf("[oauth] google id_token parse: %v", perr)
		}
	}
	client := cfg.Client(ctx, tok)
	for _, endpoint := range []string{
		"https://openidconnect.googleapis.com/v1/userinfo",
		"https://www.googleapis.com/oauth2/v3/userinfo",
	} {
		p, uerr := fetchGoogleUserinfo(ctx, client, endpoint)
		if uerr == nil {
			return p, nil
		}
		log.Printf("[oauth] google userinfo %s: %v", endpoint, uerr)
	}
	return oauthProfile{}, fmt.Errorf("google userinfo unavailable")
}

// oauthFlexBool 兼容 Google 偶发把 email_verified 写成字符串 "true"/"false"
type oauthFlexBool bool

func (b *oauthFlexBool) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	switch {
	case s == "true" || s == `"true"` || s == "1":
		*b = true
	case s == "false" || s == `"false"` || s == "0" || s == "null" || s == "":
		*b = false
	default:
		var v bool
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*b = oauthFlexBool(v)
	}
	return nil
}

const googleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

var (
	googleJWKSMu   sync.Mutex
	googleJWKSKeys map[string]*rsa.PublicKey
	googleJWKSExp  time.Time
	// 测试可替换：按 kid 取公钥
	lookupGoogleJWK = defaultLookupGoogleJWK
)

func parseGoogleIDToken(ctx context.Context, raw, clientID string) (oauthProfile, error) {
	if err := verifyGoogleIDTokenRS256(ctx, raw); err != nil {
		return oauthProfile{}, err
	}
	return parseGoogleIDTokenClaims(raw, clientID)
}

func verifyGoogleIDTokenRS256(ctx context.Context, raw string) error {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return fmt.Errorf("id_token segments")
	}
	hdrJSON, err := b64URLDecode(parts[0])
	if err != nil {
		return fmt.Errorf("id_token header: %w", err)
	}
	var hdr struct {
		Kid string `json:"kid"`
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil || hdr.Kid == "" {
		return fmt.Errorf("id_token header kid")
	}
	if hdr.Alg != "" && hdr.Alg != "RS256" {
		return fmt.Errorf("id_token alg %q", hdr.Alg)
	}
	pub, err := lookupGoogleJWK(ctx, hdr.Kid)
	if err != nil {
		return fmt.Errorf("id_token jwk: %w", err)
	}
	sig, err := b64URLDecode(parts[2])
	if err != nil {
		return fmt.Errorf("id_token sig: %w", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("id_token signature")
	}
	return nil
}

func defaultLookupGoogleJWK(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	googleJWKSMu.Lock()
	if googleJWKSKeys != nil && time.Now().Before(googleJWKSExp) {
		if k := googleJWKSKeys[kid]; k != nil {
			googleJWKSMu.Unlock()
			return k, nil
		}
	}
	googleJWKSMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleJWKSURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks %s", res.Status)
	}
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kid == "" || k.Kty != "RSA" {
			continue
		}
		pub, err := rsaPublicFromJWK(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	ttl := 1 * time.Hour
	if cc := res.Header.Get("Cache-Control"); cc != "" {
		for _, p := range strings.Split(cc, ",") {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "max-age=") {
				if sec, err := strconv.Atoi(strings.TrimPrefix(p, "max-age=")); err == nil && sec > 60 {
					ttl = time.Duration(sec) * time.Second
				}
			}
		}
	}
	googleJWKSMu.Lock()
	googleJWKSKeys = keys
	googleJWKSExp = time.Now().Add(ttl)
	pub := keys[kid]
	googleJWKSMu.Unlock()
	if pub == nil {
		return nil, fmt.Errorf("kid %q not found", kid)
	}
	return pub, nil
}

func rsaPublicFromJWK(nB64, eB64 string) (*rsa.PublicKey, error) {
	nb, err := b64URLDecode(nB64)
	if err != nil || len(nb) == 0 {
		return nil, fmt.Errorf("jwk n")
	}
	eb, err := b64URLDecode(eB64)
	if err != nil || len(eb) == 0 {
		return nil, fmt.Errorf("jwk e")
	}
	var eInt int
	for _, b := range eb {
		eInt = eInt<<8 | int(b)
	}
	if eInt <= 0 {
		return nil, fmt.Errorf("jwk e value")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: eInt}, nil
}

func b64URLDecode(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

func parseGoogleIDTokenClaims(raw, clientID string) (oauthProfile, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return oauthProfile{}, fmt.Errorf("id_token segments")
	}
	payload, err := b64URLDecode(parts[1])
	if err != nil {
		return oauthProfile{}, fmt.Errorf("id_token payload: %w", err)
	}
	var claims struct {
		Iss           string          `json:"iss"`
		Aud           json.RawMessage `json:"aud"`
		Sub           string          `json:"sub"`
		Email         string          `json:"email"`
		EmailVerified oauthFlexBool   `json:"email_verified"`
		Name          string          `json:"name"`
		Picture       string          `json:"picture"`
		Exp           int64           `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Sub == "" {
		return oauthProfile{}, fmt.Errorf("id_token claims")
	}
	iss := strings.TrimSuffix(claims.Iss, "/")
	if iss != "https://accounts.google.com" && iss != "accounts.google.com" {
		return oauthProfile{}, fmt.Errorf("id_token iss %q", claims.Iss)
	}
	if claims.Exp <= 0 {
		return oauthProfile{}, fmt.Errorf("id_token missing exp")
	}
	if time.Now().Unix() > claims.Exp+60 {
		return oauthProfile{}, fmt.Errorf("id_token expired")
	}
	if !googleAudOK(claims.Aud, clientID) {
		return oauthProfile{}, fmt.Errorf("id_token aud mismatch")
	}
	return googleProfileFromClaims(claims.Sub, claims.Email, bool(claims.EmailVerified), claims.Name, claims.Picture), nil
}

func googleAudOK(raw json.RawMessage, clientID string) bool {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || len(raw) == 0 {
		return false
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == clientID
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == clientID {
				return true
			}
		}
	}
	return false
}

func fetchGoogleUserinfo(ctx context.Context, client *http.Client, endpoint string) (oauthProfile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return oauthProfile{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return oauthProfile{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return oauthProfile{}, fmt.Errorf("%s", res.Status)
	}
	var u struct {
		Sub           string        `json:"sub"`
		Email         string        `json:"email"`
		EmailVerified oauthFlexBool `json:"email_verified"`
		Name          string        `json:"name"`
		Picture       string        `json:"picture"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.Sub == "" {
		return oauthProfile{}, fmt.Errorf("parse")
	}
	return googleProfileFromClaims(u.Sub, u.Email, bool(u.EmailVerified), u.Name, u.Picture), nil
}

func googleProfileFromClaims(sub, email string, verified bool, name, picture string) oauthProfile {
	login := ""
	if at := strings.Index(email, "@"); at > 0 {
		login = email[:at]
	}
	return oauthProfile{
		Subject:       sub,
		Email:         email,
		EmailVerified: verified,
		Login:         login,
		Nickname:      name,
		Avatar:        picture,
	}
}

// oauthViewFlags 登录/注册页注入的按钮可见性
func (a *App) oauthViewFlags() gin.H {
	return gin.H{
		"oauthGitHub": a.OAuthEnabled(model.OAuthProviderGitHub),
		"oauthGoogle": a.OAuthEnabled(model.OAuthProviderGoogle),
	}
}

func (a *App) withOAuthFlags(data gin.H) gin.H {
	if data == nil {
		data = gin.H{}
	}
	for k, v := range a.oauthViewFlags() {
		data[k] = v
	}
	return data
}
