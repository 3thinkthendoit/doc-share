package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"doc-share/internal/llm"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var errLLMOnly = errors.New("only llm config")
var errLLMGone = errors.New("llm config gone")

func clearLLMDefaults(tx *gorm.DB) error {
	return tx.Model(&model.LLMConfig{}).Where("is_default = ? OR default_mark IS NOT NULL", true).
		Updates(map[string]any{"is_default": false, "default_mark": nil}).Error
}

func assignLLMDefault(tx *gorm.DB, id uint) error {
	res := tx.Model(&model.LLMConfig{}).Where("id = ?", id).
		Updates(map[string]any{"is_default": true, "default_mark": 1})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errLLMGone
	}
	return nil
}

// DefaultLLM 返回站点默认 LLM 配置（无默认或无效时返回零值 Config）
func (a *App) DefaultLLM() llm.Config {
	var row model.LLMConfig
	err := a.DB.Where("is_default = ?", true).Order("id ASC").First(&row).Error
	if err != nil {
		// 无显式默认时取最早一条，兼容历史脏数据
		if err2 := a.DB.Order("id ASC").First(&row).Error; err2 != nil {
			return llm.Config{}
		}
	}
	return llmConfigFromRow(row)
}

func llmConfigFromRow(row model.LLMConfig) llm.Config {
	cfg := llm.Config{
		BaseURL: strings.TrimSpace(row.BaseURL),
		APIKey:  strings.TrimSpace(row.APIKey),
		Model:   strings.TrimSpace(row.Model),
	}
	if v := strings.TrimSpace(row.Temperature); v != "" {
		if t, err := strconv.ParseFloat(v, 64); err == nil && t >= 0 && t <= 2 {
			cfg.Temperature = &t
		}
	}
	return cfg
}

type llmConfigReq struct {
	Name        string `json:"name"`
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model"`
	Temperature string `json:"temperature"`
	IsDefault   bool   `json:"is_default"`
}

func parseLLMConfigReq(req llmConfigReq, keepAPIKey string) (model.LLMConfig, string) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.LLMConfig{}, "请填写名称"
	}
	if len([]rune(name)) > 64 {
		return model.LLMConfig{}, "名称过长（≤64 字符）"
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	modelName := strings.TrimSpace(req.Model)
	if baseURL == "" || modelName == "" {
		return model.LLMConfig{}, "请填写接口地址与模型名"
	}
	if len(baseURL) > 300 {
		return model.LLMConfig{}, "接口地址过长（≤300 字符）"
	}
	if len(modelName) > 128 {
		return model.LLMConfig{}, "模型名过长（≤128 字符）"
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		apiKey = keepAPIKey
	}
	if len(apiKey) > 500 {
		return model.LLMConfig{}, "API Key 过长（≤500 字符）"
	}
	temp := strings.TrimSpace(req.Temperature)
	if temp != "" {
		t, err := strconv.ParseFloat(temp, 64)
		if err != nil || t < 0 || t > 2 {
			return model.LLMConfig{}, "采样温度须在 0–2 之间，或留空"
		}
		temp = strconv.FormatFloat(t, 'f', -1, 64)
	}
	return model.LLMConfig{
		Name:        name,
		BaseURL:     baseURL,
		APIKey:      apiKey,
		Model:       modelName,
		Temperature: temp,
		IsDefault:   req.IsDefault,
	}, ""
}

// ListLLMConfigs 列出全部站点 LLM 配置
func (a *App) ListLLMConfigs(c *gin.Context) {
	var rows []model.LLMConfig
	if err := a.DB.Order("is_default DESC, id ASC").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "加载失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

// CreateLLMConfig 新增 LLM 配置；第一条自动设为默认
func (a *App) CreateLLMConfig(c *gin.Context) {
	var req llmConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	row, errmsg := parseLLMConfigReq(req, "")
	if errmsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errmsg})
		return
	}

	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.LLMConfig{}).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			row.IsDefault = true
		}
		if row.IsDefault {
			if err := clearLLMDefaults(tx); err != nil {
				return err
			}
		}
		row.DefaultMark = model.LLMDefaultMark(row.IsDefault)
		return tx.Create(&row).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": row})
}

// UpdateLLMConfig 编辑 LLM 配置；API Key 留空保留原值
func (a *App) UpdateLLMConfig(c *gin.Context) {
	row, ok := a.loadLLMConfig(c)
	if !ok {
		return
	}
	var req llmConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	next, errmsg := parseLLMConfigReq(req, row.APIKey)
	if errmsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errmsg})
		return
	}
	// 已是默认时不允许通过编辑关掉默认（须先设另一条为默认）
	if row.IsDefault {
		next.IsDefault = true
	}

	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if next.IsDefault && !row.IsDefault {
			if err := clearLLMDefaults(tx); err != nil {
				return err
			}
		}
		row.Name = next.Name
		row.BaseURL = next.BaseURL
		row.APIKey = next.APIKey
		row.Model = next.Model
		row.Temperature = next.Temperature
		row.IsDefault = next.IsDefault
		row.DefaultMark = model.LLMDefaultMark(next.IsDefault)
		return tx.Save(&row).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": row})
}

// DeleteLLMConfig 删除 LLM 配置；禁止删除唯一一条；删默认时自动提升最早剩余为默认
func (a *App) DeleteLLMConfig(c *gin.Context) {
	row, ok := a.loadLLMConfig(c)
	if !ok {
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.LLMConfig{}).Count(&n).Error; err != nil {
			return err
		}
		if n <= 1 {
			return errLLMOnly
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		if row.IsDefault {
			var next model.LLMConfig
			if err := tx.Order("id ASC").First(&next).Error; err != nil {
				return err
			}
			return assignLLMDefault(tx, next.ID)
		}
		return nil
	})
	if errors.Is(err, errLLMOnly) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "至少保留一条 LLM 配置"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SetDefaultLLMConfig 将指定配置设为默认
func (a *App) SetDefaultLLMConfig(c *gin.Context) {
	row, ok := a.loadLLMConfig(c)
	if !ok {
		return
	}
	if row.IsDefault {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := clearLLMDefaults(tx); err != nil {
			return err
		}
		return assignLLMDefault(tx, row.ID)
	})
	if errors.Is(err, errLLMGone) {
		c.JSON(http.StatusNotFound, gin.H{"error": "配置不存在"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "设置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type llmTestReq struct {
	ID          uint   `json:"id"`
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model"`
	Temperature string `json:"temperature"`
}

// assembleTestLLM 组装连通性测试配置。
// 仅编辑已有行（row != nil）时，空白字段沿用该行；新建不得借用其它配置的 API Key。
func assembleTestLLM(req llmTestReq, row *model.LLMConfig) (llm.Config, string) {
	var saved llm.Config
	if row != nil {
		saved = llmConfigFromRow(*row)
	}
	temp := saved.Temperature
	if row == nil {
		temp = nil
	}
	if v := strings.TrimSpace(req.Temperature); v != "" {
		if t, err := strconv.ParseFloat(v, 64); err == nil && t >= 0 && t <= 2 {
			temp = &t
		}
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" && row != nil {
		apiKey = saved.APIKey
	}
	cfg := llm.Config{
		BaseURL:     firstNonEmpty(req.BaseURL, saved.BaseURL),
		APIKey:      apiKey,
		Model:       firstNonEmpty(req.Model, saved.Model),
		Temperature: temp,
	}
	if !cfg.Valid() {
		return llm.Config{}, "请先填写接口地址与模型名"
	}
	return cfg, ""
}

// TestLLMConfig 测试连通性：表单当前值优先；编辑时留空字段沿用该行，不回退到默认配置
func (a *App) TestLLMConfig(c *gin.Context) {
	var req llmTestReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var row *model.LLMConfig
	if req.ID > 0 {
		var found model.LLMConfig
		if err := a.DB.First(&found, req.ID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "配置不存在"})
			return
		}
		row = &found
	}
	cfg, errmsg := assembleTestLLM(req, row)
	if errmsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errmsg})
		return
	}
	if err := cfg.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListLLMModels 拉取上游模型名，供设置页下拉提示。手填不受此列表限制。
func (a *App) ListLLMModels(c *gin.Context) {
	var req llmTestReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var row *model.LLMConfig
	if req.ID > 0 {
		var found model.LLMConfig
		if err := a.DB.First(&found, req.ID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "配置不存在"})
			return
		}
		row = &found
	}
	base := strings.TrimSpace(req.BaseURL)
	apiKey := strings.TrimSpace(req.APIKey)
	if row != nil {
		saved := llmConfigFromRow(*row)
		if base == "" {
			base = saved.BaseURL
		}
		if apiKey == "" {
			apiKey = saved.APIKey
		}
	}
	if base == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先填写接口地址"})
		return
	}
	names, err := (llm.Config{BaseURL: base, APIKey: apiKey}).ListModels(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"models": []string{}, "error": err.Error()})
		return
	}
	if names == nil {
		names = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"models": names})
}

func (a *App) loadLLMConfig(c *gin.Context) (model.LLMConfig, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效 ID"})
		return model.LLMConfig{}, false
	}
	var row model.LLMConfig
	if err := a.DB.First(&row, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "配置不存在"})
		return model.LLMConfig{}, false
	}
	return row, true
}
