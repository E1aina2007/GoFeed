package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 测试目标：验证 YAML 中的密码和运行模式不会覆盖环境变量专属配置
// 预期效果：敏感配置只接受环境变量，普通配置仍从 YAML 读取
func TestLoadEnvironmentOnlyFields(t *testing.T) {
	t.Setenv("MODE", "prod")
	t.Setenv("MYSQL_ROOT_PASSWORD", "env-root-password")
	t.Setenv("MYSQL_PASSWORD", "env-password")
	t.Setenv("REDIS_PASSWORD", "env-redis-password")
	t.Setenv("RABBITMQ_DEFAULT_PASS", "env-rabbitmq-password")
	for _, key := range []string{
		"SERVER_PORT",
		"MYSQL_HOST",
		"MYSQL_PORT",
		"MYSQL_USER",
		"MYSQL_DATABASE",
		"REDIS_HOST",
		"REDIS_PORT",
		"REDIS_DB",
		"RABBITMQ_HOST",
		"RABBITMQ_PORT",
		"RABBITMQ_DEFAULT_USER",
		"RETENTION_USER_DELETED_DAYS",
		"RETENTION_VIDEO_DELETED_DAYS",
		"RETENTION_VIDEO_DRAFT_HOURS",
		"RETENTION_MEDIA_ORPHAN_HOURS",
		"SWEEPER_INTERVAL_MINUTES",
		"SWEEPER_DRAFT_PURGE_LEASE_MINUTES",
	} {
		t.Setenv(key, "")
	}

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
redis:
  host: redis.example
  port: 6380
  password: yaml-redis-password
  db: 3
rabbitmq:
  host: rabbitmq.example
  port: 5673
  username: yaml-user
  password: yaml-rabbitmq-password
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
	if cfg.Server.Port != 9090 || cfg.DB.Host != "db.example" || cfg.DB.Port != 3307 || cfg.DB.User != "app" || cfg.DB.DBName != "app_db" {
		t.Fatalf("普通 YAML 配置未按预期读取: %+v", cfg)
	}
	if cfg.Redis.Host != "redis.example" || cfg.Redis.Port != 6380 || cfg.Redis.DB != 3 || cfg.RabbitMQ.Host != "rabbitmq.example" || cfg.RabbitMQ.Port != 5673 || cfg.RabbitMQ.Username != "yaml-user" {
		t.Fatal("中间件普通 YAML 配置未按预期读取")
	}
	if cfg.Redis.Password != "env-redis-password" || cfg.RabbitMQ.Password != "env-rabbitmq-password" {
		t.Fatal("中间件凭据未使用环境变量")
	}
}

// 测试目标：验证环境变量缺失时不会保留调用方传入的中间件凭据
// 预期效果：数据库、Redis 和 RabbitMQ 密码均不能通过 Config 结构绕过环境变量约束
func TestOverrideWithEnvClearsSecretsWithoutEnv(t *testing.T) {
	t.Setenv("MYSQL_ROOT_PASSWORD", "")
	t.Setenv("MYSQL_PASSWORD", "")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("RABBITMQ_DEFAULT_PASS", "")

	cfg := Config{
		DB:       DatabaseConfig{Password: "caller-supplied-password"},
		Redis:    RedisConfig{Password: "caller-supplied-password"},
		RabbitMQ: RabbitMQConfig{Password: "caller-supplied-password"},
	}
	OverrideWithEnv(&cfg)

	if cfg.DB.Password != "" || cfg.Redis.Password != "" || cfg.RabbitMQ.Password != "" {
		t.Fatal("未设置密码环境变量时应清空全部凭据")
	}
}
