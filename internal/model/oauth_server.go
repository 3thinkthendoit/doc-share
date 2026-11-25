package model

import "time"

// ---------- OAuth2 / OIDC 授权服务器（第三方应用「使用 DocShare 账号登录」，参考 think-tpad） ----------

// OAuthClient 开发者自助注册的第三方应用。
// ClientSecretHash 为 SHA-256 摘要；明文 Secret 只在创建 / 轮换时返回一次。公开客户端没有 Secret。
type OAuthClient struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	ClientID         string    `gorm:"size:64;uniqueIndex;not null" json:"client_id"`
	ClientSecretHash string    `gorm:"size:64;not null" json:"-"`
	ClientType       string    `gorm:"size:16;not null;default:confidential" json:"client_type"` // confidential / public
	Name             string    `gorm:"size:128;not null" json:"name"`
	HomepageURL      string    `gorm:"size:512" json:"homepage_url"`
	RedirectURIs     string    `gorm:"type:text" json:"redirect_uris"` // 换行分隔的回调地址白名单（精确匹配）
	OwnerID          uint      `gorm:"index;not null" json:"owner_id"`
	Owner            User      `gorm:"foreignKey:OwnerID" json:"-"`
	Status           string    `gorm:"size:16;index;not null" json:"status"` // pending / approved / rejected / disabled
	RejectReason     string    `gorm:"size:512" json:"reject_reason"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

const (
	OAuthClientPending  = "pending"
	OAuthClientApproved = "approved"
	OAuthClientRejected = "rejected"
	OAuthClientDisabled = "disabled"

	// OAuthClientConfidential 服务端应用：持有 Client Secret，换票须认证
	OAuthClientConfidential = "confidential"
	// OAuthClientPublic 公开客户端（单页应用 / 手机 App）：无 Secret，必须使用 PKCE
	OAuthClientPublic = "public"
)

// IsPublic 是否公开客户端（旧数据 ClientType 为空按服务端应用处理）
func (c *OAuthClient) IsPublic() bool { return c != nil && c.ClientType == OAuthClientPublic }

// OAuthCode 授权码：一次性、10 分钟有效；只存 SHA-256 摘要。
type OAuthCode struct {
	ID                  uint      `gorm:"primaryKey"`
	CodeHash            string    `gorm:"size:64;uniqueIndex;not null"`
	ClientID            string    `gorm:"size:64;index;not null"`
	UserID              uint      `gorm:"index;not null"`
	Scope               string    `gorm:"size:128"`
	RedirectURI         string    `gorm:"size:512"`
	CodeChallenge       string    `gorm:"size:128"` // PKCE（RFC 7636），仅支持 S256
	CodeChallengeMethod string    `gorm:"size:16"`
	Nonce               string    `gorm:"size:512"` // OIDC：原样写入 id_token
	ExpiresAt           time.Time `gorm:"index"`
	UsedAt              *time.Time
	CreatedAt           time.Time
}

// OAuthConsent 用户对第三方应用的授权记录；撤销后该应用无法再换取 / 使用令牌。
type OAuthConsent struct {
	ID        uint   `gorm:"primaryKey"`
	UserID    uint   `gorm:"index:idx_oauth_consent_user_client;not null"`
	ClientID  string `gorm:"size:64;index:idx_oauth_consent_user_client;not null"`
	Scopes    string `gorm:"size:128"`
	GrantedAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// OAuthRefreshToken 刷新令牌（scope 含 offline_access 时签发）：只存摘要，每次使用即轮换；
// 同一 FamilyID 为一次授权派生的整条链，已用过的旧令牌被再次使用视为泄露，整条链作废。
type OAuthRefreshToken struct {
	ID        uint      `gorm:"primaryKey"`
	TokenHash string    `gorm:"size:64;uniqueIndex;not null"`
	FamilyID  string    `gorm:"size:32;index;not null"`
	ClientID  string    `gorm:"size:64;index;not null"`
	UserID    uint      `gorm:"index;not null"`
	Scope     string    `gorm:"size:128"`
	ExpiresAt time.Time `gorm:"index"`
	UsedAt    *time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}
