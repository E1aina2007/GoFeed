package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 测试目标：验证视频删除保留期可由环境变量覆盖。
// 预期效果：视频与用户保留期分别生效，方便独立调整清扫策略。
func TestOverrideWithEnvVideoRetention(t *testing.T) {
	t.Setenv("RETENTION_USER_DELETED_DAYS", "14")
	t.Setenv("RETENTION_VIDEO_DELETED_DAYS", "3")

	cfg := Config{}
	OverrideWithEnv(&cfg)

	if cfg.Retention.UserDeletedDays != 14 {
		t.Fatalf("用户保留期错误 got=%d want=14", cfg.Retention.UserDeletedDays)
	}
	if cfg.Retention.VideoDeletedDays != 3 {
		t.Fatalf("视频保留期错误 got=%d want=3", cfg.Retention.VideoDeletedDays)
	}
}

// 测试目标：验证 YAML 中的密码和运行模式不会覆盖环境变量专属配置。
// 预期效果：敏感配置只接受环境变量，普通配置仍从 YAML 读取。
func TestLoadEnvironmentOnlyFields(t *testing.T) {
	t.Setenv("MODE", "prod")
	t.Setenv("MYSQL_ROOT_PASSWORD", "env-root-password")
	t.Setenv("MYSQL_PASSWORD", "env-password")

	filename := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
dev: true
server:
  port: 9090
database:
  host: db.example
  port: 3307
  user: app
  password: yaml-password
  dbname: app_db
`)
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	cfg, err := Load(filename)
	if err != nil {
		t.Fatalf("读取测试配置失败: %v", err)
	}
	if cfg.Dev {
		t.Fatal("MODE=prod 未生效：不应处于开发模式")
	}
	if cfg.DB.Password != "env-root-password" {
		t.Fatalf("数据库密码未使用环境变量或优先级错误 got=%q", cfg.DB.Password)
	}
	if cfg.Server.Port != 9090 || cfg.DB.Host != "db.example" || cfg.DB.DBName != "app_db" {
		t.Fatalf("普通 YAML 配置未按预期读取: %+v", cfg)
	}
}

// 测试目标：验证只有 prod 启用生产模式，其他值都归一为 dev。
// 预期效果：模式控制统一归一到 Config.Dev。
func TestOverrideWithEnvMode(t *testing.T) {
	for _, value := range []string{"", "dev", "staging", "production", "DEV"} {
		t.Run("MODE="+value, func(t *testing.T) {
			t.Setenv("MODE", value)
			cfg := Config{}
			OverrideWithEnv(&cfg)
			if !cfg.Dev {
				t.Fatal("非 prod 值应归一为开发模式")
			}
		})
	}

	t.Setenv("MODE", "prod")
	cfg := Config{}
	OverrideWithEnv(&cfg)
	if cfg.Dev {
		t.Fatal("prod 未归一为生产模式")
	}
}

// 测试目标：验证环境变量缺失时不会保留调用方传入的数据库密码。
// 预期效果：密码字段不会通过 Config 结构绕过环境变量约束。
func TestOverrideWithEnvClearsPasswordWithoutEnv(t *testing.T) {
	t.Setenv("MYSQL_ROOT_PASSWORD", "")
	t.Setenv("MYSQL_PASSWORD", "")

	cfg := Config{DB: DatabaseConfig{Password: "caller-supplied-password"}}
	OverrideWithEnv(&cfg)

	if cfg.DB.Password != "" {
		t.Fatalf("未设置密码环境变量时应清空密码 got=%q", cfg.DB.Password)
	}
}
