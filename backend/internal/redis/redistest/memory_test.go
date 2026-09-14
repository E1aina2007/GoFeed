package redistest

import (
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	rds "gofeed/internal/redis"
)

// 测试目标：内存替身满足客户端接口并提供确定性过期和实例隔离
// 预期效果：推进时钟后仅到期键消失且不同实例互不共享数据
func TestMemoryTTLAndIsolation(t *testing.T) {
	m := New(t)
	var c	rds.Client = m
	other := New(t)
	ctx := t.Context()
	if err := c.Set(ctx, "expiring", "value", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "persistent", "value", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Get(ctx, "persistent"); err != redis.Nil {
		t.Fatalf("isolation: %v", err)
	}
	m.FastForward(999 * time.Millisecond)
	if _, err := c.Get(ctx, "expiring"); err != nil {
		t.Fatal(err)
	}
	m.FastForward(time.Millisecond)
	if _, err := c.Get(ctx, "expiring"); err != redis.Nil {
		t.Fatalf("expiry: %v", err)
	}
	if v, err := c.Get(ctx, "persistent"); err != nil || v != "value" {
		t.Fatalf("persistent=%q err=%v", v, err)
	}
}
