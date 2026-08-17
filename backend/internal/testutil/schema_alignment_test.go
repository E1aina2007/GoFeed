package testutil

import (
	"os"
	"testing"

	"gofeed/internal/auth"
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
