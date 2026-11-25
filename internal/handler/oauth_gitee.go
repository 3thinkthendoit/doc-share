package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
)

// Gitee OAuth2 端点（参考 think-tpad gitee_oauth.go）。
// Gitee 换票要求 client_id/client_secret 放在表单参数中。
var giteeEndpoint = oauth2.Endpoint{
	AuthURL:   "https://gitee.com/oauth/authorize",
	TokenURL:  "https://gitee.com/oauth/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

// giteeUserURL 为变量便于测试替换
var giteeUserURL = "https://gitee.com/api/v5/user"

// defaultFetchGiteeUser 换票并拉取 Gitee 用户资料。
// Gitee /api/v5/user 的 email 不保证已验证（且常为空），与 think-tpad 一致：
// 不据此合并/回填本地账号，EmailVerified 恒为 false。
func defaultFetchGiteeUser(ctx context.Context, cfg *oauth2.Config, code string) (oauthProfile, error) {
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("gitee token exchange: %w", err)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return oauthProfile{}, fmt.Errorf("gitee token exchange: empty access_token")
	}
	// Gitee API v5 以 access_token 查询参数鉴权
	endpoint := giteeUserURL + "?access_token=" + url.QueryEscape(tok.AccessToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return oauthProfile{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "DocShare-OAuth")
	res, err := oauthCtxHTTPClient(ctx).Do(req)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("gitee user: %w", redactURLError(err))
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		log.Printf("[oauth] gitee /user status=%s body=%q", res.Status, snippet)
		return oauthProfile{}, fmt.Errorf("gitee user: %s", res.Status)
	}
	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.ID <= 0 {
		return oauthProfile{}, fmt.Errorf("gitee user parse")
	}
	nick := strings.TrimSpace(u.Name)
	if nick == "" {
		nick = strings.TrimSpace(u.Login)
	}
	return oauthProfile{
		Subject:       strconv.FormatInt(u.ID, 10),
		Email:         strings.TrimSpace(u.Email),
		EmailVerified: false,
		Login:         strings.TrimSpace(u.Login),
		Nickname:      nick,
		Avatar:        strings.TrimSpace(u.AvatarURL),
	}, nil
}
