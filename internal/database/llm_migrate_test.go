package database

import (
	"testing"

	"doc-share/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestBackfillLLMConfigsFromSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.LLMConfig{}); err != nil {
		t.Fatal(err)
	}
	rows := []model.SystemSetting{
		{Key: "llm_base_url", Value: " https://api.deepseek.com "},
		{Key: "llm_api_key", Value: "sk-old"},
		{Key: "llm_model", Value: "deepseek-chat"},
		{Key: "llm_temperature", Value: "0.2"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillLLMConfigs(db); err != nil {
		t.Fatal(err)
	}
	if err := backfillLLMConfigs(db); err != nil {
		t.Fatal(err)
	}
	var got []model.LLMConfig
	if err := db.Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].IsDefault || got[0].APIKey != "sk-old" || got[0].BaseURL != "https://api.deepseek.com" || got[0].Model != "deepseek-chat" {
		t.Fatalf("backfill=%+v", got)
	}
	if got[0].DefaultMark == nil || *got[0].DefaultMark != 1 {
		t.Fatalf("default mark=%v", got[0].DefaultMark)
	}
}

func TestBackfillLLMSkipsIncomplete(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.LLMConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SystemSetting{Key: "llm_base_url", Value: "https://api.example.com"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillLLMConfigs(db); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Model(&model.LLMConfig{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("incomplete settings inserted %d rows", n)
	}
}

func TestEnsureSingleLLMDefault(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.LLMConfig{}); err != nil {
		t.Fatal(err)
	}
	rows := []model.LLMConfig{
		{Name: "a", BaseURL: "https://a.example", Model: "m", IsDefault: true},
		{Name: "b", BaseURL: "https://b.example", Model: "m", IsDefault: true},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureSingleLLMDefault(db); err != nil {
		t.Fatal(err)
	}
	var got []model.LLMConfig
	if err := db.Order("id ASC").Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].IsDefault || got[1].IsDefault {
		t.Fatalf("defaults=%+v", got)
	}
	if got[0].DefaultMark == nil || *got[0].DefaultMark != 1 || got[1].DefaultMark != nil {
		t.Fatalf("marks a=%v b=%v", got[0].DefaultMark, got[1].DefaultMark)
	}
}
