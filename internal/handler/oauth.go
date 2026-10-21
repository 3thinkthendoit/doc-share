package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
		a.oauthFail(c, "获取用户信息失败")
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
	// msg 用中文原文作 i18n key（与站点其它登录错误一致）
	c.Redirect(http.StatusFound, "/login?error="+url.QueryEscape(a.tr(c, msg)))
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

func defaultFetchGitHubUser(ctx context.Context, cfg *oauth2.Config, code string) (oauthProfile, error) {
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return oauthProfile{}, err
	}
	client := cfg.Client(ctx, tok)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return oauthProfile{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return oauthProfile{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
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
	req.Header.Set("Accept", "application/vnd.github+json")
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
		return oauthProfile{}, err
	}
	client := cfg.Client(ctx, tok)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
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
		return oauthProfile{}, fmt.Errorf("google userinfo: %s", res.Status)
	}
	var u struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.Sub == "" {
		return oauthProfile{}, fmt.Errorf("google userinfo parse")
	}
	login := ""
	if at := strings.Index(u.Email, "@"); at > 0 {
		login = u.Email[:at]
	}
	return oauthProfile{
		Subject:       u.Sub,
		Email:         u.Email,
		EmailVerified: u.EmailVerified,
		Login:         login,
		Nickname:      u.Name,
		Avatar:        u.Picture,
	}, nil
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
