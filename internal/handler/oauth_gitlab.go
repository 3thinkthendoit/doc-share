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

// defaultGitLabBaseURL 未配置自建实例时使用 gitlab.com
const defaultGitLabBaseURL = "https://gitlab.com"

// gitlabBase 归一 GitLab 实例地址（去尾斜杠，空 = gitlab.com）。
// config.yaml / 环境变量里可能漏写协议（如 git.example.com），按 https 补全；系统设置入口已强制校验。
func gitlabBase(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return defaultGitLabBaseURL
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	return base
}

// gitlabIsOfficial 是否为官方 gitlab.com（含 www.），自建实例返回 false
func gitlabIsOfficial(base string) bool {
	u, err := url.Parse(gitlabBase(base))
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "gitlab.com" || h == "www.gitlab.com"
}

// gitlabEndpoint 按实例地址生成 OAuth2 端点（参考 think-tpad gitlab_oauth.go，支持自建 GitLab）
func gitlabEndpoint(base string) oauth2.Endpoint {
	b := gitlabBase(base)
	return oauth2.Endpoint{
		AuthURL:   b + "/oauth/authorize",
		TokenURL:  b + "/oauth/token",
		AuthStyle: oauth2.AuthStyleInParams,
	}
}

// gitlabUserURLFromConfig 由 TokenURL 反推 /api/v4/user，保证与授权端点同一实例
func gitlabUserURLFromConfig(cfg *oauth2.Config) string {
	base := strings.TrimSuffix(cfg.Endpoint.TokenURL, "/oauth/token")
	return gitlabBase(base) + "/api/v4/user"
}

// defaultFetchGitLabUser 换票并拉取 GitLab 用户资料。
// 与 think-tpad 一致：GitLab（尤其自建实例）邮箱可由管理员直接设置，不视为已验证、不据此合并账号。
func defaultFetchGitLabUser(ctx context.Context, cfg *oauth2.Config, code string) (oauthProfile, error) {
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("gitlab token exchange: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gitlabUserURLFromConfig(cfg), nil)
	if err != nil {
		return oauthProfile{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "DocShare-OAuth")
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	res, err := oauthCtxHTTPClient(ctx).Do(req)
	if err != nil {
		return oauthProfile{}, fmt.Errorf("gitlab user: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		log.Printf("[oauth] gitlab /user status=%s body=%q", res.Status, snippet)
		return oauthProfile{}, fmt.Errorf("gitlab user: %s", res.Status)
	}
	var u struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
		State     string `json:"state"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.ID <= 0 {
		return oauthProfile{}, fmt.Errorf("gitlab user parse")
	}
	if u.State != "" && u.State != "active" {
		return oauthProfile{}, fmt.Errorf("gitlab account inactive: state %q", u.State)
	}
	nick := strings.TrimSpace(u.Name)
	if nick == "" {
		nick = strings.TrimSpace(u.Username)
	}
	return oauthProfile{
		Subject:       strconv.FormatInt(u.ID, 10),
		Email:         strings.TrimSpace(u.Email),
		EmailVerified: false,
		Login:         strings.TrimSpace(u.Username),
		Nickname:      nick,
		Avatar:        strings.TrimSpace(u.AvatarURL),
	}, nil
}
