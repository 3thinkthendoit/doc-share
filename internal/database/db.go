package database

import (
	"fmt"
	"log"
	"time"

	"doc-share/internal/config"
	"doc-share/internal/model"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Init 连接数据库、执行迁移并种子管理员账号
func Init(cfg *config.Config) (*gorm.DB, error) {
	level := logger.Warn
	if cfg.Database.ShowLog {
		level = logger.Info
	}

	db, err := gorm.Open(mysql.Open(cfg.Database.DSN), &gorm.Config{
		Logger: logger.Default.LogMode(level),
		// 翻译驱动错误（如唯一键冲突 → gorm.ErrDuplicatedKey），供业务层精确归因
		TranslateError: true,
		// 不建数据库外键：project_id/category_id 用 0 表示未分组，与 FK 冲突；
		// 引用完整性由应用层 validProjectRef/validCategoryRef 校验保证
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connect mysql: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConn)
	sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConn)

	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Category{}, &model.ApiKey{}, &model.Document{}, &model.Share{}, &model.ProjectShare{}, &model.ShareAccessRequest{}, &model.SystemSetting{},
		&model.DocumentRevision{}, &model.Comment{}, &model.DocumentVisitor{}, &model.ProjectMember{}, &model.Message{}, &model.DocTemplate{},
		&model.ChatSession{}, &model.ChatMessage{}, &model.ChatSkill{}); err != nil {
		return nil, fmt.Errorf("auto migrate: %w", err)
	}

	if err := dropLegacyIndexes(db); err != nil {
		log.Printf("[migrate] 清理旧索引失败（可手动处理）: %v", err)
	}

	if err := backfillDocVersions(db); err != nil {
		log.Printf("[migrate] 回填文档版本号失败（可手动处理）: %v", err)
	}

	// 分类时间列是后加的：旧行可能为 NULL，扫进 time.Time 会变成零值并在页面显示 0001-01-01。
	// 回填失败则中断启动，避免带着坏数据对外服务。
	if err := backfillCategoryTimes(db); err != nil {
		return nil, fmt.Errorf("backfill category times: %w", err)
	}

	if err := seedAdmin(db, cfg); err != nil {
		return nil, err
	}

	return db, nil
}

// dropLegacyIndexes 清理历史遗留索引：AutoMigrate 只建不删。
// categories 改为个人所有后，旧的全局唯一索引 idx_categories_name 会阻止不同用户的同名分类。
func dropLegacyIndexes(db *gorm.DB) error {
	var n int64
	err := db.Raw(
		"SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'categories' AND index_name = ?",
		"idx_categories_name").Scan(&n).Error
	if err != nil || n == 0 {
		return err
	}
	if err := db.Exec("ALTER TABLE categories DROP INDEX idx_categories_name").Error; err != nil {
		return err
	}
	log.Printf("[migrate] 已删除旧索引 categories.idx_categories_name（分类改为个人唯一）")
	return nil
}

// backfillDocVersions 全量回填业务版本号：version = v1.0.{content_version}（规则见 model.DerivedVersion）。
// 版本号改为系统自动派生后，历史文档的手填值 / 空值在启动时统一归一；幂等操作，重复执行无副作用。
// 按派生值分组批量 UPDATE（而非逐行），且不用 SQL CONCAT——SQLite 无该函数，Go 层拼接保证跨方言一致
func backfillDocVersions(db *gorm.DB) error {
	var docs []struct {
		ID             uint
		ContentVersion int64
		Version        string
	}
	if err := db.Model(&model.Document{}).Select("id", "content_version", "version").Find(&docs).Error; err != nil {
		return err
	}
	// 仅收集与派生值不一致的行，按目标版本号聚合 id，每组一条 UPDATE
	byVersion := make(map[string][]uint)
	for _, d := range docs {
		if want := model.DerivedVersion(d.ContentVersion); d.Version != want {
			byVersion[want] = append(byVersion[want], d.ID)
		}
	}
	total := 0
	for ver, ids := range byVersion {
		if err := db.Model(&model.Document{}).Where("id IN ?", ids).Update("version", ver).Error; err != nil {
			return err
		}
		total += len(ids)
	}
	if total > 0 {
		log.Printf("[migrate] 已回填 %d 篇文档的版本号（自动派生 v1.0.N）", total)
	}
	return nil
}

// backfillCategoryTimes 给历史分类补上创建/更新时间。
// 这两列是后加的：AutoMigrate ADD COLUMN 后旧行多为 NULL；GORM 扫 NULL 进 time.Time 会得到零值
//（不是报错），页面会显示 0001-01-01。没有真实历史，只能记为本次启动时刻；已有值的行不改。幂等。
func backfillCategoryTimes(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&model.Category{}, "CreatedAt") {
		return nil
	}
	now := time.Now()
	// 同时覆盖 NULL 与已写入的零值时间（如 '0001-01-01'），避免漏填
	zero := time.Time{}
	res := db.Exec("UPDATE categories SET created_at = ? WHERE created_at IS NULL OR created_at <= ?", now, zero)
	if res.Error != nil {
		return res.Error
	}
	n := res.RowsAffected
	res = db.Exec("UPDATE categories SET updated_at = ? WHERE updated_at IS NULL OR updated_at <= ?", now, zero)
	if res.Error != nil {
		return res.Error
	}
	if n += res.RowsAffected; n > 0 {
		log.Printf("[migrate] 已回填 %d 处分类创建/更新时间（历史行无真实时间戳，记为启动时刻）", n)
	}
	return nil
}

// seedAdmin 首次运行时创建默认管理员
func seedAdmin(db *gorm.DB, cfg *config.Config) error {
	var count int64
	if err := db.Model(&model.User{}).Where("role = ?", model.RoleAdmin).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.Auth.DefaultPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	admin := model.User{
		Username:     cfg.Auth.DefaultAdmin,
		PasswordHash: string(hash),
		Nickname:     "管理员",
		Role:         model.RoleAdmin,
		Status:       model.StatusEnabled,
	}
	if err := db.Create(&admin).Error; err != nil {
		return err
	}
	log.Printf("[seed] 已创建默认管理员账号: %s / %s (请尽快修改密码)", cfg.Auth.DefaultAdmin, cfg.Auth.DefaultPassword)
	return nil
}
