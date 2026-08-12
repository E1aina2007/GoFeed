package testutil

import (
	"os"
	"testing"

	"gofeed/internal/auth"
	"gofeed/internal/user"
	"gofeed/internal/video"
)

func TestMain(m *testing.M) {
	os.Exit(Main(m))
}

// TestModelsAlignWithMigrations 把每个 GORM 模型与真实迁移结果逐项对照，
// 防止未来类型名、表名或列定义与手写 DDL 漂移
func TestModelsAlignWithMigrations(t *testing.T) {
	db := DB(t)

	type modelCheck struct {
		model   any
		table   string
		columns []string
	}
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
				"cover_file_name", "cover_original_name", "status",
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
	}

	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatalf("读取迁移后的表清单失败: %v", err)
	}
	actual := make(map[string]bool, len(tables))
	for _, table := range tables {
		actual[table] = true
	}

	for _, check := range checks {
		if !actual[check.table] {
			t.Errorf("迁移缺少表 %s，实际表=%v", check.table, tables)
			continue
		}
		// 用 GORM 自身的解析结果查询主键列，类型名映射错表时这里会失败
		if !db.Migrator().HasColumn(check.model, "id") {
			t.Errorf("模型 %T 未能解析到表 %s", check.model, check.table)
		}

		// 直接查 information_schema，不依赖 GORM 的解析结果
		var columns []string
		if err := db.Raw(
			"SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			check.table,
		).Scan(&columns).Error; err != nil {
			t.Fatalf("读取表 %s 的列清单失败: %v", check.table, err)
		}
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
