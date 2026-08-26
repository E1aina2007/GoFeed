package testutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gofeed/internal/auth"
	"gofeed/internal/social"
	"gofeed/internal/user"
	"gofeed/internal/video"
)

// 测试目标：配置模型结构集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(Main(m))
}

// 测试目标：验证模型定义与真实迁移后的数据库结构保持一致
// 预期效果：每个模型都映射到正确数据表且声明的列全部存在
func TestModelsAlignWithMigrations(t *testing.T) {
	db := DB(t)

	// 测试目标：描述单个模型与目标数据表的预期结构
	// 预期效果：供后续逐项校验模型、数据表和列定义
	type modelCheck struct {
		model   any
		table   string
		columns []string
	}
	// 测试目标：列出需要与迁移结果对齐的模型、数据表和列集合
	// 预期效果：覆盖全部业务模型的结构校验
	checks := []modelCheck{
		{
			model: &user.User{},
			table: "users",
			columns: []string{
				"id", "username", "password", "avatar_url", "bio", "deleted_at",
			},
		},
		{
			model: &video.Video{},
			table: "videos",
			columns: []string{
				"id", "author_id", "title", "description", "play_url",
				"play_file_name", "play_original_name", "cover_url",
				"cover_file_name", "cover_original_name", "status", "purge_token",
				"purge_lease_until", "play_purged_at", "cover_purged_at",
				"published_at", "likes_count", "comments_count",
				"created_at", "updated_at", "deleted_at",
			},
		},
		{
			model: &auth.AuthSession{},
			table: "auth_sessions",
			columns: []string{
				"id", "user_id", "refresh_token_hash", "expires_at",
				"revoked_at", "created_at", "updated_at",
			},
		},
		{
			model: &social.VideoLike{},
			table: "video_likes",
			columns: []string{
				"id", "video_id", "user_id", "created_at",
			},
		},
		{
			model: &social.Follow{},
			table: "user_follows",
			columns: []string{
				"id", "follower_id", "followee_id", "created_at",
			},
		},
		{
			model: &social.Comment{},
			table: "video_comments",
			columns: []string{
				"id", "video_id", "author_id", "content", "created_at", "updated_at", "deleted_at",
			},
		},
	}

	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatalf("读取迁移后的表清单失败: %v", err)
	}
	// 测试目标：收集真实迁移后的数据表名称
	// 预期效果：用于判断每个模型对应的数据表是否存在
	actual := make(map[string]bool, len(tables))
	for _, table := range tables {
		actual[table] = true
	}

	for _, check := range checks {
		if !actual[check.table] {
			t.Errorf("迁移缺少表 %s，实际表=%v", check.table, tables)
			continue
		}
		// 测试目标：使用对象关系映射的解析结果检查主键列
		// 预期效果：错误的数据表映射会被发现
		if !db.Migrator().HasColumn(check.model, "id") {
			t.Errorf("模型 %T 未能解析到表 %s", check.model, check.table)
		}

		// 测试目标：直接读取数据库元数据
		// 预期效果：不依赖对象关系映射的表名解析
		var columns []string
		if err := db.Raw(
			"SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			check.table,
		).Scan(&columns).Error; err != nil {
			t.Fatalf("读取表 %s 的列清单失败: %v", check.table, err)
		}
		// 测试目标：收集实际存在的列名称
		// 预期效果：用于判断模型声明的每个列是否完成迁移
		present := make(map[string]bool, len(columns))
		for _, column := range columns {
			present[column] = true
		}
		for _, want := range check.columns {
			if !present[want] {
				t.Errorf("表 %s 缺少列 %s，实际列=%v", check.table, want, columns)
			}
		}
	}
}

// 测试目标：验证草稿和清扫迁移的可空字段语义
// 预期效果：草稿可保持空媒体发布时间，清扫租约与检查点默认未设置
func TestVideoMigrationNullableColumns(t *testing.T) {
	db := DB(t)

	type columnMeta struct {
		ColumnName    string  `gorm:"column:COLUMN_NAME"`
		IsNullable    string  `gorm:"column:IS_NULLABLE"`
		ColumnDefault *string `gorm:"column:COLUMN_DEFAULT"`
	}
	var actual []columnMeta
	if err := db.Raw(`
		SELECT COLUMN_NAME, IS_NULLABLE, COLUMN_DEFAULT
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME = 'videos'
		  AND COLUMN_NAME IN ('published_at', 'play_url', 'cover_url', 'purge_token', 'purge_lease_until', 'play_purged_at', 'cover_purged_at')
	`).Scan(&actual).Error; err != nil {
		t.Fatalf("读取 videos 列元数据失败: %v", err)
	}

	byName := make(map[string]columnMeta, len(actual))
	for _, column := range actual {
		byName[column.ColumnName] = column
	}
	for _, name := range []string{"published_at", "purge_token", "purge_lease_until", "play_purged_at", "cover_purged_at"} {
		column, ok := byName[name]
		if !ok || column.IsNullable != "YES" || column.ColumnDefault != nil {
			t.Errorf("列 %s 应为无默认值的可空列 got=%+v", name, column)
		}
	}
	for _, name := range []string{"play_url", "cover_url"} {
		column, ok := byName[name]
		if !ok || column.IsNullable != "NO" || column.ColumnDefault == nil || *column.ColumnDefault != "" {
			t.Errorf("列 %s 应为默认空字符串的非空列 got=%+v", name, column)
		}
	}

	var indexes []string
	if err := db.Raw(`
		SELECT DISTINCT INDEX_NAME
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME = 'videos'
		  AND INDEX_NAME IN ('idx_videos_draft_created', 'idx_videos_purging_lease')
	`).Scan(&indexes).Error; err != nil {
		t.Fatalf("读取 videos 索引元数据失败: %v", err)
	}
	present := make(map[string]bool, len(indexes))
	for _, index := range indexes {
		present[index] = true
	}
	for _, name := range []string{"idx_videos_draft_created", "idx_videos_purging_lease"} {
		if !present[name] {
			t.Errorf("草稿清扫迁移缺少索引 %s，实际索引=%v", name, indexes)
		}
	}
}

// 测试目标：验证 000004 迁移会接管旧版误软删的非公开视频
// 预期效果：旧草稿转为未软删的 purging 并成为草稿清扫候选
func TestDraftPurgeMigrationAdoptsLegacyDeletedDraft(t *testing.T) {
	db := DB(t)
	legacy := &video.Video{
		AuthorID: 1,
		Title:    "legacy deleted draft",
		Status:   video.VideoStatusDraft,
	}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("创建旧版草稿失败: %v", err)
	}
	if err := db.Exec("UPDATE videos SET deleted_at = ? WHERE id = ?", time.Now().Add(-time.Hour), legacy.ID).Error; err != nil {
		t.Fatalf("设置旧版软删除状态失败: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(migrationsDir(), "000004_draft_purge_lease.up.sql"))
	if err != nil {
		t.Fatalf("读取 000004 迁移失败: %v", err)
	}
	updateSQL := draftPurgeCompatibilityUpdate(string(content))
	if updateSQL == "" {
		t.Fatal("000004 迁移缺少旧版草稿兼容更新")
	}
	if err := db.Exec(updateSQL).Error; err != nil {
		t.Fatalf("执行 000004 兼容更新失败: %v", err)
	}

	var got video.Video
	if err := db.Unscoped().First(&got, legacy.ID).Error; err != nil {
		t.Fatalf("读取兼容后的草稿失败: %v", err)
	}
	if got.Status != video.VideoStatusPurging || got.DeletedAt.Valid {
		t.Fatalf("兼容状态错误 status=%s deleted=%v", got.Status, got.DeletedAt.Valid)
	}

	ids, err := video.NewRepository(db).GetRecoverableDraftPurgeList(context.Background(), 10)
	if err != nil {
		t.Fatalf("查询兼容草稿清扫候选失败: %v", err)
	}
	if len(ids) != 1 || ids[0] != legacy.ID {
		t.Fatalf("兼容草稿未成为清扫候选 ids=%v", ids)
	}
}

func draftPurgeCompatibilityUpdate(content string) string {
	for _, statement := range strings.Split(content, ";") {
		if index := strings.Index(statement, "UPDATE videos"); index >= 0 {
			return strings.TrimSpace(statement[index:])
		}
	}
	return ""
}
