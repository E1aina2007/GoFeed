package redis

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"gofeed/internal/config"
)

// 测试目标：在显式启用时验证真实 Redis 的生命周期和 KV 及 Lua 多值契约
// 预期效果：仅操作随机测试键并清理且未启用时明确标记集成跳过
func TestRealRedis(t *testing.T) {
	if os.Getenv("GOFEED_REDIS_INTEGRATION") != "1" {
		t.Skip("集成跳过：设置 GOFEED_REDIS_INTEGRATION=1 并配置本机 Redis 后运行")
	}
	values, err := godotenv.Read("../../.env")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal("无法读取本地 Redis 测试配置")
	}
	for _, key := range []string{"REDIS_HOST", "REDIS_PORT", "REDIS_DB", "REDIS_PASSWORD"} {
		if _, exists := os.LookupEnv(key); !exists {
			if value, ok := values[key]; ok {
				t.Setenv(key, value)
			}
		}
	}
	path := "../../configs/config.dev.yaml"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = "../../configs/config.example.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal("无法加载 Redis 测试配置")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c, err := New(ctx, cfg.Redis)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	key := "gofeed:test:c1a:" + uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		if _, err := c.Del(cleanupCtx, key); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, key, "0", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if value, err := c.Get(ctx, key); err != nil || value != "0" {
		t.Fatalf("get=%q err=%v", value, err)
	}
	value, err := c.Eval(ctx, `return {redis.call('INCRBY', KEYS[1], ARGV[1]), redis.call('PTTL', KEYS[1])}`, []string{key}, 1)
	if err != nil {
		t.Fatal(err)
	}
	pair, ok := value.([]any)
	if !ok || len(pair) != 2 || pair[0] != int64(1) {
		t.Fatalf("eval=%#v", value)
	}
	ttl, ok := pair[1].(int64)
	if !ok || ttl <= 0 || ttl > 30000 {
		t.Fatalf("ttl=%#v", pair[1])
	}
	if count, err := c.Del(ctx, key); err != nil || count != 1 {
		t.Fatalf("del=%d err=%v", count, err)
	}
	if _, err := c.Get(ctx, key); err != redis.Nil {
		t.Fatalf("missing=%v", err)
	}
}
