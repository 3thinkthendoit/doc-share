package model

import "time"

// OAuth 身份提供方常量
const (
	OAuthProviderGitHub = "github"
	OAuthProviderGoogle = "google"
	OAuthProviderGitee  = "gitee"
	OAuthProviderGitLab = "gitlab"
	OAuthProviderQQ     = "qq"
	OAuthProviderTPad   = "tpad" // Think-TPAD（自建实例的 OAuth2 授权服务器）
)

// OAuthProviders 全部支持的提供方（展示顺序）
var OAuthProviders = []string{OAuthProviderGitHub, OAuthProviderGoogle, OAuthProviderGitee, OAuthProviderGitLab, OAuthProviderQQ, OAuthProviderTPad}

// IsOAuthProvider 是否为受支持的 OAuth 提供方
func IsOAuthProvider(p string) bool {
	for _, v := range OAuthProviders {
		if v == p {
			return true
		}
	}
	return false
}

// OAuthIdentity 外部登录身份与本地用户的绑定（provider+subject 唯一）
type OAuthIdentity struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Provider  string    `gorm:"size:32;not null;uniqueIndex:uk_oauth_provider_subject;index" json:"provider"`
	Subject   string    `gorm:"size:128;not null;uniqueIndex:uk_oauth_provider_subject" json:"subject"` // GitHub 数字 id / Google sub
	UserID    uint      `gorm:"not null;index" json:"user_id"`
	Email     string    `gorm:"size:255" json:"email"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
