package database

import (
	"testing"
	"time"

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

// TestBackfillCategoryTimes 后加的 created_at/updated_at：NULL / 零值行填成当前时刻，已有值不改，重复执行幂等
func TestBackfillCategoryTimes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(&model.Category{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	kept := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	seed := []model.Category{
		{Name: "缺时间", Sort: 1, OwnerID: 1},                          // Create 会写当前时间，下面再清成 NULL
		{Name: "已有时间", Sort: 2, OwnerID: 1, CreatedAt: kept, UpdatedAt: kept},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	if err := db.Exec("UPDATE categories SET created_at = NULL, updated_at = NULL WHERE id = ?", seed[0].ID).Error; err != nil {
		t.Fatalf("nullify: %v", err)
	}

	before := time.Now().Add(-time.Second)
	if err := backfillCategoryTimes(db); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	after := time.Now().Add(time.Second)

	var filled, preserved model.Category
	if err := db.First(&filled, seed[0].ID).Error; err != nil {
		t.Fatalf("reload filled: %v", err)
	}
	if filled.CreatedAt.IsZero() || filled.UpdatedAt.IsZero() {
		t.Fatalf("filled times still zero: created=%v updated=%v", filled.CreatedAt, filled.UpdatedAt)
	}
	if filled.CreatedAt.Before(before) || filled.CreatedAt.After(after) ||
		filled.UpdatedAt.Before(before) || filled.UpdatedAt.After(after) {
		t.Errorf("filled times out of range: created=%v updated=%v", filled.CreatedAt, filled.UpdatedAt)
	}

	if err := db.First(&preserved, seed[1].ID).Error; err != nil {
		t.Fatalf("reload preserved: %v", err)
	}
	if !preserved.CreatedAt.UTC().Equal(kept) || !preserved.UpdatedAt.UTC().Equal(kept) {
		t.Errorf("preserved times changed: created=%v updated=%v want %v", preserved.CreatedAt, preserved.UpdatedAt, kept)
	}

	snapCreated, snapUpdated := filled.CreatedAt, filled.UpdatedAt
	if err := backfillCategoryTimes(db); err != nil {
		t.Fatalf("backfill again: %v", err)
	}
	if err := db.First(&filled, seed[0].ID).Error; err != nil {
		t.Fatalf("reload after idempotent: %v", err)
	}
	if !filled.CreatedAt.Equal(snapCreated) || !filled.UpdatedAt.Equal(snapUpdated) {
		t.Errorf("idempotent rewrite: created %v→%v updated %v→%v", snapCreated, filled.CreatedAt, snapUpdated, filled.UpdatedAt)
	}
}
