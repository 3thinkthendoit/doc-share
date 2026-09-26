package model

import (
	"strings"
	"time"
)

// User 系统用户（后台登录账号）
type User struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Username     string     `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string     `gorm:"size:255;not null" json:"-"`
	Nickname     string     `gorm:"size:64" json:"nickname"`
	Email        string     `gorm:"size:255;index" json:"email"`                 // 邮箱（唯一性应用层校验，空串允许多个）
	Phone        string     `gorm:"size:32;index" json:"phone"`                  // 手机号（同上）
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

	MemberRoleView = "view" // 项目成员：只读
	MemberRoleEdit = "edit" // 项目成员：可增删改项目内文档与评论
)

// IsAdmin 判断是否管理员
func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

// DisplayName 展示名：昵称为空时回退用户名
func (u *User) DisplayName() string {
	if u == nil {
		return ""
	}
	if strings.TrimSpace(u.Nickname) != "" {
		return u.Nickname
	}
	return u.Username
}

// MaskedEmail 脱敏邮箱：保留首字符与域名，如 a***@163.com
func (u *User) MaskedEmail() string {
	if u == nil || u.Email == "" {
		return ""
	}
	at := strings.Index(u.Email, "@")
	if at <= 0 {
		return "***"
	}
	return u.Email[:1] + "***" + u.Email[at:]
}

// MaskedPhone 脱敏手机号：保留前 3 后 4，如 159****5028
func (u *User) MaskedPhone() string {
	if u == nil || u.Phone == "" {
		return ""
	}
	if len(u.Phone) != 11 {
		return "***"
	}
	return u.Phone[:3] + "****" + u.Phone[7:]
}

// Project 项目（文档归属容器，属主私有；可添加成员按角色协作）
type Project struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:100;not null" json:"name"`
	Description string    `gorm:"size:500" json:"description"`
	OwnerID     uint      `gorm:"index;not null" json:"owner_id"`
	Owner       User      `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`
	DocCount    int64     `gorm:"-" json:"doc_count"`            // 子查询统计，非数据库列
	MemberCount int64     `gorm:"-" json:"member_count"`         // 成员数统计，非数据库列
	Role        string    `gorm:"-" json:"role,omitempty"`       // 当前用户在该项目中的成员角色（列表回填，空=属主/无关联）
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ProjectMember 项目成员（挂项目的文档对成员按角色可见）；成员管理仅项目属主
type ProjectMember struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ProjectID uint      `gorm:"uniqueIndex:idx_member_project_user;not null" json:"project_id"`
	UserID    uint      `gorm:"uniqueIndex:idx_member_project_user;not null" json:"user_id"`
	Role      string    `gorm:"size:16;not null;default:view" json:"role"` // view / edit
	CreatedAt time.Time `json:"created_at"`
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
	CanEdit    bool       `gorm:"not null;default:false" json:"can_edit"`   // 登录用户可编辑
	AllowApply bool       `gorm:"not null;default:true" json:"-"`           // 已废弃：改由站点设置 allow_share_apply 控制
	Password   string     `gorm:"size:255" json:"-"`                        // bcrypt 哈希，空表示无密码
	ExpireAt   *time.Time `json:"expire_at"`                                  // nil 表示永不过期
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

// 分享访问申请状态
const (
	AccessPending  = 0 // 待审批
	AccessApproved = 1 // 已通过（可跳过密码）
	AccessRejected = 2 // 已拒绝
)

// ShareAccessRequest 有密码分享下的「申请查看」：登录用户与游客均可提交，属主审批后凭 cookie 放行
type ShareAccessRequest struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	DocumentID   uint       `gorm:"index;not null" json:"document_id"`
	UserID       uint       `gorm:"index;not null;default:0" json:"user_id"` // 0=游客
	GuestName    string     `gorm:"size:64" json:"guest_name"`               // 称呼（游客必填；登录可预填）
	GuestContact string     `gorm:"size:128" json:"guest_contact"`           // 可选联系方式
	Message      string     `gorm:"size:500" json:"message"`                 // 可选留言
	ClientIP     string     `gorm:"size:64;index" json:"-"`                  // 游客防刷：同 IP 复用 pending
	Status       int        `gorm:"not null;default:0;index" json:"status"`
	RequestToken string     `gorm:"size:32;uniqueIndex;not null" json:"-"` // 浏览器 cookie 凭证
	CreatedAt    time.Time  `json:"created_at"`
	ReviewedAt   *time.Time `json:"reviewed_at"`
	ReviewerID   uint       `gorm:"not null;default:0" json:"reviewer_id"`
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

// DocumentRevision 文档内容修订快照：分享编辑等覆盖保存前自动创建，支持回滚
type DocumentRevision struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	DocumentID uint      `gorm:"index;not null" json:"document_id"`
	EditorID   uint      `gorm:"not null;default:0" json:"editor_id"` // 0=系统
	EditorName string    `gorm:"size:64" json:"editor_name"`
	Content    string    `gorm:"type:longtext" json:"content"`
	CreatedAt  time.Time `json:"created_at"`
}

// Comment 文档评论：分享页/阅读页可发，游客以游客身份（UserID=0，GuestName）发表
type Comment struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	DocumentID uint      `gorm:"index;not null" json:"document_id"`
	ParentID   uint      `gorm:"index;not null;default:0" json:"parent_id"` // 0=顶层，否则为被回复的评论 ID（仅一级回复）
	UserID     uint      `gorm:"index;not null;default:0" json:"user_id"`   // 0=游客
	GuestName  string    `gorm:"size:64" json:"guest_name"`                 // 游客昵称（UserID=0 时有效）
	Content    string    `gorm:"size:1000;not null" json:"content"`
	Images     string    `gorm:"type:text" json:"-"` // JSON 数组，最多 3 个本站上传图 URL
	CreatedAt  time.Time `json:"created_at"`
}

// 站内消息类型（持久化到 Message.Kind）
const (
	MsgAccessApply     = "access_apply"     // 有人申请查看文档
	MsgAccessApproved  = "access_approved"  // 申请已通过
	MsgAccessRejected  = "access_rejected"  // 申请已拒绝
	MsgComment         = "comment"          // 文档新评论
	MsgCommentReply    = "comment_reply"    // 评论被回复
	MsgProjectAdded    = "project_added"    // 被加入项目
	MsgProjectRole     = "project_role"     // 项目角色变更
	MsgProjectRemoved  = "project_removed"  // 被移出项目
	MsgShareEdited     = "share_edited"     // 分享页协作编辑保存
	MsgDocUpdated      = "doc_updated"      // 他人更新了你的文档
)

// Message 站内消息：投递给登录用户；邮件为可选旁路（有邮箱且 SMTP 已配置时异步发送）
type Message struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	UserID    uint       `gorm:"index;not null" json:"user_id"` // 收件人
	Kind      string     `gorm:"size:32;index;not null" json:"kind"`
	Title     string     `gorm:"size:200;not null" json:"title"`
	Body      string     `gorm:"size:1000" json:"body"`
	Link      string     `gorm:"size:500" json:"link"` // 站内相对路径，如 /admin/docs/1/edit
	RefType   string     `gorm:"size:32" json:"ref_type"`
	RefID     uint       `gorm:"index;not null;default:0" json:"ref_id"`
	ReadAt    *time.Time `json:"read_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// DocumentVisitor 文档历史访客（按 文档+身份 去重，同一访客只留一条并累计次数）
type DocumentVisitor struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	DocumentID  uint      `gorm:"uniqueIndex:idx_visitor_doc_identity;not null" json:"document_id"`
	Identity    string    `gorm:"size:64;uniqueIndex:idx_visitor_doc_identity;not null" json:"identity"` // u:<uid> / g:<游客名>
	UserID      uint      `gorm:"index" json:"user_id"`                                                  // 0=游客
	Name        string    `gorm:"size:64;not null" json:"name"`
	Avatar      string    `gorm:"size:500" json:"avatar"`
	Visits      int64     `gorm:"not null;default:1" json:"visits"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}
