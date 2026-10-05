package redis

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"gofeed/internal/config"
)

// 测试目标：一次 Lua 调用返回计数与剩余 TTL 并保持并发原子性
// 预期效果：并发调用返回互不重复的计数及同一毫秒 TTL
func TestEvalAtomicMultipleResults(t *testing.T) {
	c := testClient(t)
	const script = `local n = redis.call('INCR', KEYS[1]); redis.call('PEXPIRE', KEYS[1], ARGV[1]); return {n, redis.call('PTTL', KEYS[1])}`
	const workers = 20
	results := make(chan int64, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			value, err := c.Eval(t.Context(), script, []string{"counter"}, 5000)
			if err != nil {
				t.Error(err)
				return
			}
			pair, ok := value.([]any)
			if !ok || len(pair) != 2 || !reflect.DeepEqual(pair[1], int64(5000)) {
				t.Errorf("result=%#v", value)
				return
			}
			n, ok := pair[0].(int64)
			if !ok {
				t.Errorf("counter=%#v", pair[0])
				return
			}
			results <- n
		})
	}
	wg.Wait()
	close(results)
	seen := make(map[int64]bool)
	for n := range results {
		if n < 1 || n > workers || seen[n] {
			t.Errorf("invalid counter=%d", n)
		}
		seen[n] = true
	}
	if len(seen) != workers {
		t.Fatalf("got %d results", len(seen))
	}
}

func testClient(t testing.TB) Client {
	t.Helper()
	s := miniredis.RunT(t)
	port, err := strconv.Atoi(s.Port())
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(context.Background(), config.RedisConfig{Host: s.Host(), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

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
