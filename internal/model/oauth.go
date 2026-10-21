package model

import "time"

// OAuth 身份提供方常量
const (
	OAuthProviderGitHub = "github"
	OAuthProviderGoogle = "google"
)

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
