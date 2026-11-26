package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
)

// Think-TPAD 登录：对接 think-tpad 的 OAuth2 授权服务器（think-tpad app/tpad/service/oauth_server*.go）。
//
//	授权页   {base}/oauth/authorize               前端路由（未登录先登录再回来）
//	换票     POST {base}/api/v1/tpad/oauth/token  表单携带 client_id / client_secret（不支持 HTTP Basic）
//	取资料   GET  {base}/api/v1/tpad/oauth/userinfo  Bearer
//
// base 为浏览器访问 Think-TPAD 的地址：生产环境前端由后端同源托管；前后端分开部署时前端反向代理 /api，
// 因此一个地址即可覆盖授权页与服务端接口。Think-TPAD 通常为自建内网服务，始终直连、不走出站代理。
const (
	tpadAuthorizePath = "/oauth/authorize"
	tpadTokenPath     = "/api/v1/tpad/oauth/token"
	tpadUserinfoPath  = "/api/v1/tpad/oauth/userinfo"
)

// tpadEndpoint 按 Think-TPAD 地址生成 OAuth2 端点；base 为空时返回空端点（视为未配置）
func tpadEndpoint(base string) oauth2.Endpoint {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return oauth2.Endpoint{}
	}
	return oauth2.Endpoint{
		AuthURL:   base + tpadAuthorizePath,
		TokenURL:  base + tpadTokenPath,
		AuthStyle: oauth2.AuthStyleInParams, // think-tpad 只从表单读 client_id / client_secret
	}
}

// tpadBaseFromConfig 由 TokenURL 反推实例地址，保证取资料与换票是同一实例
func tpadBaseFromConfig(cfg *oauth2.Config) string {
	return strings.TrimSuffix(cfg.Endpoint.TokenURL, tpadTokenPath)
}

// defaultFetchTPadUser 换票并拉取 Think-TPAD 用户资料。
// think-tpad 的 userinfo 不返回邮箱是否已验证（且邮箱可由管理员直接设置），与 Gitee / GitLab 一致：
// 不视为已验证、不据此合并本地账号。
func defaultFetchTPadUser(ctx context.Context, cfg *oauth2.Config, code string) (oauthProfile, error) {
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("tpad token exchange: %w", err)
	}
	base := tpadBaseFromConfig(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+tpadUserinfoPath, nil)
	if err != nil {
		return oauthProfile{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "DocShare-OAuth")
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	res, err := oauthCtxHTTPClient(ctx).Do(req)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("tpad user: %w", redactURLError(err))
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		log.Printf("[oauth] tpad /userinfo status=%s body=%q", res.Status, snippet)
		return oauthProfile{}, fmt.Errorf("tpad user: %s", res.Status)
	}
	var u struct {
		Sub      string `json:"sub"`
		Username string `json:"username"`
		Nickname string `json:"nickname"`
		Picture  string `json:"picture"`
		Email    string `json:"email"`
	}
	if err := json.Unmarshal(body, &u); err != nil || strings.TrimSpace(u.Sub) == "" {
		return oauthProfile{}, fmt.Errorf("tpad user parse")
	}
	nick := strings.TrimSpace(u.Nickname)
	if nick == "" {
		nick = strings.TrimSpace(u.Username)
	}
	avatar := strings.TrimSpace(u.Picture)
	if strings.HasPrefix(avatar, "/") && !strings.HasPrefix(avatar, "//") {
		avatar = base + avatar // 站内相对路径转绝对，doc-share 页面才能显示
	}
	return oauthProfile{
		Subject:       strings.TrimSpace(u.Sub),
		Email:         strings.TrimSpace(u.Email),
		EmailVerified: false,
		Login:         strings.TrimSpace(u.Username),
		Nickname:      nick,
		Avatar:        avatar,
	}, nil
}
