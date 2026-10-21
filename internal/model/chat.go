package model

import (
	"time"

	"gorm.io/gorm"
)

// ChatSession AI 在线体验会话（登录用户私有，归属 UserID）
type ChatSession struct {
	ID             uint           `gorm:"primaryKey" json:"id"`
	UserID         uint           `gorm:"index;not null" json:"user_id"`
	DocumentID     uint           `gorm:"index;not null;default:0" json:"document_id"`   // 0=全局会话；>0=绑定单篇文档（预览/分享页对话）
	Title          string         `gorm:"size:100;not null;default:新会话" json:"title"`    // 首条消息后自动改为内容摘要
	AgentID        uint           `gorm:"not null;default:0" json:"agent_id"`            // 预留：关联智能体，0=默认助手
	TrustSensitive bool           `gorm:"not null;default:false" json:"trust_sensitive"` // 历史字段；敏感操作一律审批，此标记不再跳过确认
	CreatingSkill  bool           `gorm:"not null;default:false" json:"creating_skill"`  // 本会话正处于 /create-skill 多轮流程
	TaskBoard      string         `gorm:"type:text" json:"-"`                            // 多篇修改的任务清单 JSON，跨轮保留
	RollingSummary string         `gorm:"type:text" json:"-"`                            // 滑出上下文窗口的旧轮滚动摘要
	SummaryUptoID  uint           `gorm:"not null;default:0" json:"-"`                   // 摘要已覆盖到的最大消息 id（含）
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	// DeletedAt 软删除（回收站）：Valid=true 表示在回收站，15 天内可还原，到期由清扫器彻底销毁
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
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
