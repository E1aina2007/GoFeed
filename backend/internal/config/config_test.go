package config

import "testing"

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
