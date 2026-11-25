package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/oauth2"
)

// QQ 互联端点（参考 think-tpad qq_oauth.go）。
// 授权地址走标准 OAuth2；换票、取 openid、取资料是 QQ 私有接口，下方手工实现。
var (
	qqEndpoint = oauth2.Endpoint{
		AuthURL:   "https://graph.qq.com/oauth2.0/authorize",
		TokenURL:  "https://graph.qq.com/oauth2.0/token",
		AuthStyle: oauth2.AuthStyleInParams,
	}
	qqOpenIDURL   = "https://graph.qq.com/oauth2.0/me"
	qqUserInfoURL = "https://graph.qq.com/user/get_user_info"
)

// qqErrCode QQ 的 error 字段有时是数字、有时是字符串，统一转成字符串
type qqErrCode string

func (e *qqErrCode) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "null" || s == "0" {
		s = ""
	}
	*e = qqErrCode(s)
	return nil
}

// qqJSONP 兼容未生效 fmt=json 时返回的 callback( {...} ); 包裹
var qqJSONP = regexp.MustCompile(`(?s)^\s*callback\(\s*(\{.*\})\s*\);?\s*$`)

func qqUnwrap(body []byte) []byte {
	if m := qqJSONP.FindSubmatch(body); m != nil {
		return m[1]
	}
	return body
}

func qqGet(ctx context.Context, endpoint string, q url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "DocShare-OAuth")
	res, err := oauthCtxHTTPClient(ctx).Do(req)
	if err != nil {
		return nil, redactURLError(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", res.Status)
	}
	return qqUnwrap(body), nil
}

// defaultFetchQQUser 换票 → 取 openid → 取昵称头像。
// openid 是用户在本 APP 下的唯一标识；QQ 不提供邮箱。资料接口失败不阻断登录。
func defaultFetchQQUser(ctx context.Context, cfg *oauth2.Config, code string) (oauthProfile, error) {
	// 1) 换票：GET + fmt=json
	tq := url.Values{}
	tq.Set("grant_type", "authorization_code")
	tq.Set("client_id", cfg.ClientID)
	tq.Set("client_secret", cfg.ClientSecret)
	tq.Set("code", code)
	tq.Set("redirect_uri", cfg.RedirectURL)
	tq.Set("fmt", "json")
	body, err := qqGet(ctx, cfg.Endpoint.TokenURL, tq)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("qq token exchange: %w", err)
	}
	var tok struct {
		AccessToken string    `json:"access_token"`
		Error       qqErrCode `json:"error"`
		ErrorDesc   string    `json:"error_description"`
	}
	if json.Unmarshal(body, &tok) != nil {
		// 兜底：老格式 access_token=...&expires_in=...
		if v, perr := url.ParseQuery(strings.TrimSpace(string(body))); perr == nil {
			tok.AccessToken = v.Get("access_token")
		}
	}
	if tok.Error != "" || strings.TrimSpace(tok.AccessToken) == "" {
		return oauthProfile{}, fmt.Errorf("qq token exchange: error=%s %s", tok.Error, tok.ErrorDesc)
	}

	// 2) 取 openid
	oq := url.Values{}
	oq.Set("access_token", tok.AccessToken)
	oq.Set("fmt", "json")
	body, err = qqGet(ctx, qqOpenIDURL, oq)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("qq openid: %w", err)
	}
	var me struct {
		ClientID  string    `json:"client_id"`
		OpenID    string    `json:"openid"`
		Error     qqErrCode `json:"error"`
		ErrorDesc string    `json:"error_description"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return oauthProfile{}, fmt.Errorf("qq openid parse")
	}
	openID := strings.TrimSpace(me.OpenID)
	if me.Error != "" || openID == "" {
		return oauthProfile{}, fmt.Errorf("qq openid: error=%s %s", me.Error, me.ErrorDesc)
	}
	// 防止令牌属于其他 APP（令牌替换攻击）
	if me.ClientID != "" && me.ClientID != cfg.ClientID {
		return oauthProfile{}, fmt.Errorf("qq openid: client_id mismatch")
	}

	// 3) 取资料：失败只记日志，昵称/头像可缺省
	var nick, avatar string
	uq := url.Values{}
	uq.Set("access_token", tok.AccessToken)
	uq.Set("oauth_consumer_key", cfg.ClientID)
	uq.Set("openid", openID)
	if body, err := qqGet(ctx, qqUserInfoURL, uq); err != nil {
		log.Printf("[oauth] qq get_user_info: %v", err)
	} else {
		var info struct {
			Ret       int    `json:"ret"`
			Msg       string `json:"msg"`
			Nickname  string `json:"nickname"`
			FigureQQ2 string `json:"figureurl_qq_2"`
			FigureQQ1 string `json:"figureurl_qq_1"`
		}
		if json.Unmarshal(body, &info) == nil && info.Ret == 0 {
			nick = strings.TrimSpace(info.Nickname)
			avatar = strings.TrimSpace(info.FigureQQ2)
			if avatar == "" {
				avatar = strings.TrimSpace(info.FigureQQ1)
			}
			// QQ 头像常返回 http，HTTPS 站点会被混合内容拦截；qlogo 支持 https
			if strings.HasPrefix(avatar, "http://") {
				avatar = "https://" + strings.TrimPrefix(avatar, "http://")
			}
		} else {
			log.Printf("[oauth] qq get_user_info ret=%d msg=%q", info.Ret, info.Msg)
		}
	}
	// QQ 无英文登录名，昵称多为中文会被用户名清洗掉；用 openid 前缀生成稳定可读的用户名
	login := "qq_" + openID
	if len(login) > 11 {
		login = login[:11]
	}
	return oauthProfile{
		Subject:       openID,
		EmailVerified: false,
		Login:         login,
		Nickname:      nick,
		Avatar:        avatar,
	}, nil
}
