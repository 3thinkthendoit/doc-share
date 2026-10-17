package model

import "time"

// ChatSkill 用户自定义 Agent Skill（对标 Cursor SKILL.md）。
// 通过 /create-skill 引导创建，用 /{name} 显式调用；DisableModelInvocation=false 时可被模型按 description 自动选用。
type ChatSkill struct {
	ID                     uint      `gorm:"primaryKey" json:"id"`
	UserID                 uint      `gorm:"uniqueIndex:idx_skill_user_name;not null" json:"user_id"`
	Name                   string    `gorm:"size:64;uniqueIndex:idx_skill_user_name;not null" json:"name"` // 小写字母/数字/连字符
	Description            string    `gorm:"size:1024;not null" json:"description"`
	Content                string    `gorm:"type:longtext;not null" json:"content"` // SKILL.md 正文（含可选 frontmatter）
	DisableModelInvocation bool      `gorm:"not null;default:true" json:"disable_model_invocation"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// TableName 显式表名
func (ChatSkill) TableName() string { return "chat_skills" }
