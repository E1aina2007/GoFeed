package infracachefeed

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	redisdriver "github.com/redis/go-redis/v9"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/config"
	domainfeed "gofeed/internal/domain/feed"
	redisinfra "gofeed/internal/redis"
)

// 测试目标：仅加载真实 Redis 用例所需的本地配置
// 预期效果：保留环境变量优先级且纯缓存测试不初始化 MySQL
func TestMain(m *testing.M) {
	if _, file, _, ok := runtime.Caller(0); ok {
		_ = godotenv.Load(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".env"))
	}
	os.Exit(m.Run())
}

// namespacedPageCache 在真实客户端前叠加随机命名空间并记录本用例写过的键
type namespacedPageCache struct {
	client redisinfra.Client
	prefix string

	mu   sync.Mutex
	keys []string
}

func (c *namespacedPageCache) Get(ctx context.Context, key string) (string, error) {
	return c.client.Get(ctx, c.prefix+key)
}

func (c *namespacedPageCache) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	c.mu.Lock()
	c.keys = append(c.keys, c.prefix+key)
	c.mu.Unlock()
	return c.client.Set(ctx, c.prefix+key, value, expiration)
}

// writtenKeys 返回包装器实际写入过的真实键
func (c *namespacedPageCache) writtenKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.keys...)
}

// realRedisCache 连接真实 Redis 并返回带随机命名空间的页缓存与可观测包装器
func realRedisCache(t *testing.T, options PageCacheOptions) (*PageCache, *namespacedPageCache) {
	t.Helper()
	if values, err := godotenv.Read("../../../../.env"); err == nil {
		for _, key := range []string{"REDIS_HOST", "REDIS_PORT", "REDIS_DB", "REDIS_PASSWORD"} {
			if _, exists := os.LookupEnv(key); exists {
				continue
			}
			if value, ok := values[key]; ok {
				t.Setenv(key, value)
			}
		}
	}
	path := "../../../../configs/config.dev.yaml"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = "../../../../configs/config.example.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Skipf("需要真实 Redis：加载配置失败 %v", err)
	}
	if cfg.Redis.Host == "" || cfg.Redis.Port == 0 {
		t.Skip("需要真实 Redis：未配置 REDIS_HOST 与 REDIS_PORT")
	}
	dialCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := redisinfra.New(dialCtx, cfg.Redis)
	if err != nil {
		t.Skipf("需要真实 Redis：连接 127.0.0.1 失败 %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	wrapped := &namespacedPageCache{client: client, prefix: "gofeed:test:" + uuid.NewString() + ":"}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		for _, key := range wrapped.writtenKeys() {
			if _, err := client.Del(cleanupCtx, key); err != nil {
				t.Errorf("清理键失败: %v", err)
				continue
			}
			if _, err := client.Get(cleanupCtx, key); !errors.Is(err, redisdriver.Nil) {
				t.Errorf("清理后键仍存在: %v", err)
			}
		}
	})

	cache, err := NewPageCache(wrapped, options)
	if err != nil {
		t.Fatalf("构造页缓存失败: %v", err)
	}
	return cache, wrapped
}

// 测试目标：真实 Redis 上写入的页可按文档键布局原样读回
// 预期效果：真实键存在且载荷版本正确，读取命中且条目一致
func TestRealRedisPageCacheRoundTrip(t *testing.T) {
	cache, wrapped := realRedisCache(t, PageCacheOptions{TTL: time.Minute, OperationTimeout: time.Second})
	query := timelineQuery(nil, 20)
	items := descendingItems(3, time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC))
	if err := cache.SetPage(t.Context(), query, applicationfeed.CachedPage{Items: items}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	key := wrapped.prefix + pageKey(query)
	raw, err := wrapped.client.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("真实键读取失败 key=%s err=%v", key, err)
	}
	var payload pagePayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("真实载荷反序列化失败: %v", err)
	}
	if payload.Version != pagePayloadVersion || payload.SortVersion != applicationfeed.TimelinePageSortVersion || len(payload.Items) != len(items) {
		t.Fatalf("真实载荷=%+v", payload)
	}

	page, hit, err := cache.GetPage(t.Context(), query)
	if err != nil || !hit {
		t.Fatalf("hit=%v err=%v", hit, err)
	}
	comparePageItems(t, page.Items, items)
}

// 测试目标：真实 Redis 上未写入的查询返回未命中
// 预期效果：未命中标记为假且错误为空
func TestRealRedisPageCacheMiss(t *testing.T) {
	cache, _ := realRedisCache(t, PageCacheOptions{TTL: time.Minute, OperationTimeout: time.Second})
	cursor := &domainfeed.TimelineCursor{PublishedAt: time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC), VideoID: 99}
	query := timelineQuery(cursor, 20)
	page, hit, err := cache.GetPage(t.Context(), query)
	if err != nil {
		t.Fatalf("未命中不应报错: %v", err)
	}
	if hit || page.Items != nil {
		t.Fatalf("hit=%v page=%+v", hit, page)
	}
}

// 测试目标：真实 Redis 上空页往返后仍为命中且切片非空
// 预期效果：命中为真且条目为零长度非空切片
func TestRealRedisPageCacheEmptyPage(t *testing.T) {
	cache, _ := realRedisCache(t, PageCacheOptions{TTL: time.Minute, OperationTimeout: time.Second})
	query := timelineQuery(nil, 20)
	if err := cache.SetPage(t.Context(), query, applicationfeed.CachedPage{Items: []domainfeed.FeedPageItem{}}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	page, hit, err := cache.GetPage(t.Context(), query)
	if err != nil || !hit {
		t.Fatalf("hit=%v err=%v", hit, err)
	}
	if page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("items=%#v", page.Items)
	}
}

// 测试目标：真实 Redis 按写入的短过期时间使页自然失效
// 预期效果：写入后立即命中，等待超过过期时间后轮询到未命中
func TestRealRedisPageCacheExpires(t *testing.T) {
	const ttl = time.Second
	cache, _ := realRedisCache(t, PageCacheOptions{TTL: ttl, OperationTimeout: time.Second})
	query := timelineQuery(nil, 20)
	start := time.Now()
	if err := cache.SetPage(t.Context(), query, applicationfeed.CachedPage{Items: descendingItems(2, time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC))}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if _, hit, err := cache.GetPage(t.Context(), query); err != nil || !hit {
		t.Fatalf("过期前应命中 hit=%v err=%v", hit, err)
	}

	deadline := start.Add(5 * time.Second)
	for {
		page, hit, err := cache.GetPage(t.Context(), query)
		if err != nil {
			t.Fatalf("轮询失败: %v", err)
		}
		if !hit {
			if page.Items != nil {
				t.Fatalf("未命中应返回空页: %+v", page)
			}
			if time.Since(start) < ttl {
				t.Fatalf("过期时间不足就未命中 elapsed=%v", time.Since(start))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("超过 %v 仍未过期", 5*time.Second)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
