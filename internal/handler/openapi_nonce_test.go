package handler

import (
	"strconv"
	"testing"
	"time"

	"doc-share/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestNonceSeenDBFirstThenMemory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:openapi_nonce_"+strconv.FormatInt(time.Now().UnixNano(), 10)+"?mode=memory&cache=shared"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.OpenAPINonce{}); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: db}
	key := "app1:nonce-abc-xyz-12"

	if a.nonceSeen(key) {
		t.Fatal("first use must not be seen")
	}
	if !nonceCached(key) {
		t.Fatal("success should remember in memory")
	}
	if !a.nonceSeen(key) {
		t.Fatal("second use must be replay")
	}

	// 另一 key：模拟「仅库冲突」——先直接写库，内存未记，应判重放并回填内存
	key2 := "app1:nonce-other-99"
	if err := db.Create(&model.OpenAPINonce{NonceKey: key2, CreatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if !a.nonceSeen(key2) {
		t.Fatal("DB-only prior insert must count as replay")
	}
	if !nonceCached(key2) {
		t.Fatal("duplicate path should warm memory cache")
	}
}
