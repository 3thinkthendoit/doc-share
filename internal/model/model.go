package model

import (
	"time"
)

// User 系统用户（后台登录账号）
type User struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Username     string     `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string     `gorm:"size:255;not null" json:"-"`
	Nickname     string     `gorm:"size:64" json:"nickname"`
	Avatar       string     `gorm:"size:500" json:"avatar"`                      // 头像 URL（/uploads/... 或外链），空则显示首字圆底
	Role         string     `gorm:"size:16;not null;default:viewer" json:"role"` // admin / viewer
	Status       int        `gorm:"not null;default:1" json:"status"`            // 1 启用 0 禁用
	LastActiveAt *time.Time `json:"last_active_at"`                              // 最近活跃时间（登录期间限流刷新）
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"

	StatusEnabled  = 1
	StatusDisabled = 0
)

// IsAdmin 判断是否管理员
func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

// Project 项目（文档归属容器，个人所有）
type Project struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:100;not null" json:"name"`
	Description string    `gorm:"size:500" json:"description"`
	OwnerID     uint      `gorm:"index;not null" json:"owner_id"`
	Owner       User      `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`
	DocCount    int64     `gorm:"-" json:"doc_count"` // 子查询统计，非数据库列
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Category 分类（个人所有，与 Project 同构；admin 可见全部）
type Category struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"size:50;uniqueIndex:idx_cat_owner_name;not null" json:"name"` // 同一所有者内名称唯一
	Sort     int    `gorm:"not null;default:0" json:"sort"`                              // 数字越小越靠前
	OwnerID  uint   `gorm:"uniqueIndex:idx_cat_owner_name;index;not null;default:0" json:"owner_id"`
	Owner    User   `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`
	DocCount int64  `gorm:"-" json:"doc_count"` // 子查询统计，非数据库列
}

// ApiKey API 开放平台密钥：外部系统用 Secret 签名调用 /openapi（个人所有，与 Project 同构）
type ApiKey struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Name       string     `gorm:"size:100;not null" json:"name"`               // 备注名称
	AppKey     string     `gorm:"size:32;uniqueIndex;not null" json:"app_key"` // 身份标识（公开）
	Secret     string     `gorm:"size:64;not null" json:"-"`                   // 签名密钥（HMAC 需明文存储，仅创建/重置时返回一次）
	OwnerID    uint       `gorm:"index;not null" json:"owner_id"`
	Owner      User       `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`
	Status     int        `gorm:"not null;default:1" json:"status"` // 1 启用 0 禁用
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Document Markdown 文档
type Document struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Title      string    `gorm:"size:255;not null" json:"title"`
	Slug       string    `gorm:"size:32;uniqueIndex;not null" json:"slug"`
	Content    string    `gorm:"type:longtext" json:"content"`
	OwnerID    uint      `gorm:"index;not null" json:"owner_id"`
	Owner      User      `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`
	ProjectID  uint      `gorm:"index;not null;default:0" json:"project_id"` // 0 表示未分组
	Project    *Project  `gorm:"foreignKey:ProjectID" json:"project,omitempty"`
	CategoryID uint      `gorm:"index;not null;default:0" json:"category_id"` // 0 表示未分类
	Category   *Category `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
	IsShared   bool      `gorm:"not null;default:false" json:"is_shared"`
	ViewCount  int64     `gorm:"not null;default:0" json:"view_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`

	// Share 关联的分享配置（一对一）
	Share *Share `gorm:"foreignKey:DocumentID" json:"share,omitempty"`
}

// Share 文档分享配置（与 Document 一对一）
type Share struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	DocumentID uint       `gorm:"uniqueIndex;not null" json:"document_id"`
	ShareToken string     `gorm:"size:32;uniqueIndex;not null" json:"share_token"`
	Password   string     `gorm:"size:255" json:"-"` // bcrypt 哈希，空表示无密码
	ExpireAt   *time.Time `json:"expire_at"`         // nil 表示永不过期
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// HasPassword 是否设置了访问密码
func (s *Share) HasPassword() bool {
	return s != nil && s.Password != ""
}

// IsExpired 链接是否已过期
func (s *Share) IsExpired() bool {
	return s != nil && s.ExpireAt != nil && time.Now().After(*s.ExpireAt)
}

// 系统设置键名（管理员后台维护，站点级）
const (
	SettingSiteName   = "site_name"   // 网站名称
	SettingSiteLogo   = "site_logo"   // 系统 Logo 图片 URL
	SettingSiteDomain = "site_domain" // 系统域名（生成分享链接等绝对地址用，留空取访问域名）
)

// SystemSetting 站点级键值设置
type SystemSetting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}
