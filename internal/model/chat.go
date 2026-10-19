package model

import "time"

// ChatSession AI 在线体验会话（登录用户私有，归属 UserID）
type ChatSession struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	UserID         uint      `gorm:"index;not null" json:"user_id"`
	Title          string    `gorm:"size:100;not null;default:新会话" json:"title"`    // 首条消息后自动改为内容摘要
	AgentID        uint      `gorm:"not null;default:0" json:"agent_id"`            // 预留：关联智能体，0=默认助手
	TrustSensitive bool      `gorm:"not null;default:false" json:"trust_sensitive"` // 本会话敏感操作免逐次审批（可审计）
	CreatingSkill  bool      `gorm:"not null;default:false" json:"creating_skill"`  // 本会话正处于 /create-skill 多轮流程
	TaskBoard      string    `gorm:"type:text" json:"-"`                            // 多篇修改的任务清单 JSON，跨轮保留
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ChatMessage 会话消息（Role: user / assistant / system）
type ChatMessage struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	SessionID uint      `gorm:"index;not null" json:"session_id"`
	Role      string    `gorm:"size:16;not null" json:"role"`
	Content   string    `gorm:"type:longtext" json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// 会话标题默认值
const ChatDefaultTitle = "新会话"
