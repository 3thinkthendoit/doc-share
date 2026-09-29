package database

import (
	"testing"

	"doc-share/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestBackfillDocVersions 启动回填：历史空值 / 手填值统一归一为 v1.0.{content_version}，
// 已一致的行不改，且重复执行幂等
func TestBackfillDocVersions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 私有内存库须锁定单连接，否则连接池切换会看到不同的空库
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(&model.Document{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seed := []model.Document{
		{Title: "空版本", Slug: "bk1aaaa", Content: "c", ContentVersion: 3, Version: ""},
		{Title: "手填", Slug: "bk2bbbb", Content: "c", ContentVersion: 5, Version: "beta"},
		{Title: "已一致", Slug: "bk3cccc", Content: "c", ContentVersion: 0, Version: "v1.0.0"},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	if err := backfillDocVersions(db); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	want := map[uint]string{
		seed[0].ID: "v1.0.3",
		seed[1].ID: "v1.0.5",
		seed[2].ID: "v1.0.0",
	}
	for id, w := range want {
		var d model.Document
		if err := db.First(&d, id).Error; err != nil {
			t.Fatalf("reload %d: %v", id, err)
		}
		if d.Version != w {
			t.Errorf("doc %d version = %q, want %q", id, d.Version, w)
		}
	}
	// 幂等：再次执行不应报错
	if err := backfillDocVersions(db); err != nil {
		t.Fatalf("backfill again: %v", err)
	}
}
