package handler

import (
	"strconv"
	"testing"
	"time"

	"doc-share/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPrepareCreatorRegion(t *testing.T) {
	dsn := "file:creator_geo_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Document{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	a := &App{DB: db}

	t.Run("shorten stored china region", func(t *testing.T) {
		doc := model.Document{Title: "t", Slug: "geo-short", CreatorIP: "203.0.113.1", CreatorRegion: "中国 广东 深圳"}
		if err := db.Create(&doc).Error; err != nil {
			t.Fatal(err)
		}
		a.prepareCreatorRegion(&doc)
		if doc.CreatorRegion != "广东" {
			t.Fatalf("in-memory region=%q want 广东", doc.CreatorRegion)
		}
		var got model.Document
		if err := db.First(&got, doc.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.CreatorRegion != "广东" {
			t.Fatalf("db region=%q want 广东", got.CreatorRegion)
		}
	})

	t.Run("backfill from creator ip", func(t *testing.T) {
		doc := model.Document{Title: "t2", Slug: "geo-fill", CreatorIP: "114.114.114.114", CreatorRegion: ""}
		if err := db.Create(&doc).Error; err != nil {
			t.Fatal(err)
		}
		a.prepareCreatorRegion(&doc)
		if doc.CreatorRegion == "" {
			t.Fatal("expected non-empty region from public IP")
		}
		var got model.Document
		if err := db.First(&got, doc.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.CreatorRegion != doc.CreatorRegion {
			t.Fatalf("db=%q mem=%q", got.CreatorRegion, doc.CreatorRegion)
		}
	})

	t.Run("noop when empty ip and region", func(t *testing.T) {
		doc := model.Document{Title: "t3", Slug: "geo-empty"}
		if err := db.Create(&doc).Error; err != nil {
			t.Fatal(err)
		}
		a.prepareCreatorRegion(&doc)
		if doc.CreatorRegion != "" {
			t.Fatalf("unexpected region %q", doc.CreatorRegion)
		}
	})
}
