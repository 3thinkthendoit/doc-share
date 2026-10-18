package model

import "time"

// LLMConfig 站点级 LLM 接入配置（OpenAI 兼容；可多条，其中一条为默认）
type LLMConfig struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Name    string `gorm:"size:64;not null" json:"name"` // 展示名，如 DeepSeek / 本地 Ollama
	BaseURL string `gorm:"size:300;not null" json:"base_url"`
	APIKey  string `gorm:"size:500;not null;default:''" json:"-"`
	Model   string `gorm:"size:128;not null" json:"model"`
	// Temperature 空串 = 未设置（不传上游）；有值则解析为 0–2
	Temperature string `gorm:"size:16;not null;default:''" json:"temperature"`
	IsDefault   bool   `gorm:"not null;default:false" json:"is_default"`
	// DefaultMark 仅默认行为 1，其余为 NULL。唯一索引允许多个 NULL，从而保证至多一条默认。
	DefaultMark *int      `gorm:"uniqueIndex" json:"-"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LLMDefaultMark 默认配置在唯一槽上的取值
func LLMDefaultMark(on bool) *int {
	if !on {
		return nil
	}
	n := 1
	return &n
}

func (LLMConfig) TableName() string { return "llm_configs" }
