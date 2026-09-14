package redis

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gofeed/internal/config"
)

// 测试目标：构造客户端时使用配置中的地址密码和数据库
// 预期效果：正确凭据连通并隔离数据库且错误凭据原样返回错误
func TestNewConfig(t *testing.T) {
	s := miniredis.RunT(t)
	s.RequireAuth("test-password")
	port, _ := strconv.Atoi(s.Port())
	cfg := config.RedisConfig{Host: s.Host(), Port: port, DB: 3, Password: "test-password"}
	c, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(t.Context(), "key", "value", 0); err != nil {
		t.Fatal(err)
	}
	if s.DB(0).Exists("key") || !s.DB(3).Exists("key") {
		t.Fatal("DB selection not respected")
	}
	cfg.Password = "wrong"
	bad, err := New(t.Context(), cfg)
	var redisErr redis.Error
	if bad != nil || !errors.As(err, &redisErr) {
		t.Fatalf("client=%v error=%v", bad, err)
	}
}

// 测试目标：构造期间无法连通或上下文取消时释放客户端并保留错误类型
// 预期效果：返回空接口和网络错误或原始取消错误
func TestNewFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	c, err := New(ctx, config.RedisConfig{Host: "127.0.0.1", Port: port})
	var netErr net.Error
	if c != nil || !errors.As(err, &netErr) {
		t.Fatalf("client=%v error=%v", c, err)
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	c, err = New(canceled, config.RedisConfig{Host: "127.0.0.1", Port: port})
	if c != nil || err != context.Canceled {
		t.Fatalf("client=%v error=%v", c, err)
	}
}

// 测试目标：验证基础 KV 操作及服务端错误透传
// 预期效果：写读删除结果一致且缺失键和 Lua 错误不被包装
func TestClientKVAndErrors(t *testing.T) {
	c := testClient(t)
	ctx := t.Context()
	if _, err := c.Get(ctx, "missing"); err != redis.Nil {
		t.Fatalf("missing: %v", err)
	}
	if err := c.Set(ctx, "key", "value", time.Minute); err != nil {
		t.Fatal(err)
	}
	if value, err := c.Get(ctx, "key"); err != nil || value != "value" {
		t.Fatalf("get=%q err=%v", value, err)
	}
	if n, err := c.Del(ctx, "key", "missing"); err != nil || n != 1 {
		t.Fatalf("del=%d err=%v", n, err)
	}
	if _, err := c.Get(ctx, "key"); err != redis.Nil {
		t.Fatalf("deleted: %v", err)
	}
	_, err := c.Eval(ctx, `return redis.error_reply('ERR test failure')`, nil)
	var redisErr redis.Error
	if !errors.As(err, &redisErr) || err.Error() != "ERR test failure" {
		t.Fatalf("eval: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := c.Set(canceled, "key", "ignored", 0); err != context.Canceled {
		t.Fatalf("canceled: %v", err)
	}
}

// 测试目标：关闭客户端后探测及所有命令均保留底层关闭语义
// 预期效果：每种操作返回同一关闭哨兵错误且关闭不自动重建连接
func TestClientClosed(t *testing.T) {
	c := testClient(t)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	_, getErr := c.Get(ctx, "key")
	_, delErr := c.Del(ctx, "key")
	_, evalErr := c.Eval(ctx, "return 1", nil)
	for _, err := range []error{c.Ping(ctx), getErr, delErr, evalErr, c.Set(ctx, "key", "value", 0), c.Close()} {
		if err != redis.ErrClosed {
			t.Fatalf("closed error=%v", err)
		}
	}
}

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
