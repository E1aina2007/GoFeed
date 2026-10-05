package infracachefeed

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// errCacheBoom 表示替身注入的任意业务错误
var errCacheBoom = errors.New("cache boom")

// cacheSetCall 记录一次写入调用的参数
type cacheSetCall struct {
	key        string
	value      string
	expiration time.Duration
}

// fakeStringCache 是可编程的缓存客户端替身，记录全部读写调用
// 用例可注入错误与延迟，并断言调用次数、键和过期时间
type fakeStringCache struct {
	mu       sync.Mutex
	values   map[string]string
	getCalls []string
	setCalls []cacheSetCall
	getErr   error
	setErr   error
	getDelay time.Duration
	setDelay time.Duration
}

func newFakeStringCache() *fakeStringCache {
	return &fakeStringCache{values: make(map[string]string)}
}

func (f *fakeStringCache) Get(ctx context.Context, key string) (string, error) {
	f.mu.Lock()
	f.getCalls = append(f.getCalls, key)
	delay, err := f.getDelay, f.getErr
	f.mu.Unlock()
	if delay > 0 {
		if waitErr := waitCacheDelay(ctx, delay); waitErr != nil {
			return "", waitErr
		}
	}
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	value, exists := f.values[key]
	if !exists {
		return "", redisdriver.Nil
	}
	return value, nil
}

func (f *fakeStringCache) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	f.mu.Lock()
	f.setCalls = append(f.setCalls, cacheSetCall{key: key, value: value, expiration: expiration})
	delay, err := f.setDelay, f.setErr
	f.mu.Unlock()
	if delay > 0 {
		if waitErr := waitCacheDelay(ctx, delay); waitErr != nil {
			return waitErr
		}
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = value
	return nil
}

func (f *fakeStringCache) seed(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = value
}

func (f *fakeStringCache) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.getCalls), len(f.setCalls)
}

func (f *fakeStringCache) lastSet(t *testing.T) cacheSetCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.setCalls) == 0 {
		t.Fatal("未发生写入调用")
	}
	return f.setCalls[len(f.setCalls)-1]
}

// waitCacheDelay 等待延迟结束或在上下文结束时提前返回
func waitCacheDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// timelineQuery 构造指定游标与页大小的时间线查询
func timelineQuery(cursor *domainfeed.TimelineCursor, limit int) applicationfeed.PageCacheQuery {
	return applicationfeed.PageCacheQuery{Scene: domainfeed.SceneTimeline, Cursor: cursor, Limit: limit}
}

// descendingItems 生成发布时间与视频标识同时严格倒序的页条目
func descendingItems(count int, newest time.Time) []domainfeed.FeedPageItem {
	items := make([]domainfeed.FeedPageItem, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, domainfeed.FeedPageItem{
			VideoID:     uint(9000 - i),
			AuthorID:    uint(100 + i),
			PublishedAt: newest.Add(-time.Duration(i) * time.Minute),
		})
	}
	return items
}

// mustNewPageCache 构造页缓存并在失败时终止用例
func mustNewPageCache(t *testing.T, client StringCache, options PageCacheOptions) *PageCache {
	t.Helper()
	cache, err := NewPageCache(client, options)
	if err != nil {
		t.Fatalf("构造页缓存失败: %v", err)
	}
	return cache
}

// 测试目标：零值配置下读写仍受默认操作超时约束
// 预期效果：客户端阻塞时按默认超时返回且耗时远小于阻塞时长
func TestPageCacheAppliesDefaultOperationTimeout(t *testing.T) {
	query := timelineQuery(nil, 20)
	fake := newFakeStringCache()
	fake.getDelay = 5 * time.Second
	cache := mustNewPageCache(t, fake, PageCacheOptions{})
	started := time.Now()
	_, _, err := cache.GetPage(t.Context(), query)
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if elapsed < 50*time.Millisecond || elapsed > time.Second {
		t.Fatalf("elapsed=%v", elapsed)
	}
}

// payloadItem 构造带作者标识的载荷条目
func payloadItem(videoID uint, authorID uint, publishedAt time.Time) pagePayloadItem {
	return pagePayloadItem{VideoID: videoID, AuthorID: &authorID, PublishedAt: publishedAt}
}

// payloadBefore 生成严格早于游标且按时间倒序的载荷条目
func payloadBefore(count int, cursor time.Time) []pagePayloadItem {
	items := make([]pagePayloadItem, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, payloadItem(uint(100-i), uint(1+i), cursor.Add(-time.Duration(i+1)*time.Minute)))
	}
	return items
}

// marshalPayload 将载荷编码为缓存 JSON 字符串
func marshalPayload(t *testing.T, payload pagePayload) string {
	t.Helper()
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("编码载荷失败: %v", err)
	}
	return string(value)
}

// comparePageItems 逐条比较页条目并检查发布时间已归一为 UTC
func comparePageItems(t *testing.T, got, want []domainfeed.FeedPageItem) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("条目数 got=%d want=%d", len(got), len(want))
	}
	for i, item := range got {
		if item.VideoID != want[i].VideoID || item.AuthorID != want[i].AuthorID {
			t.Fatalf("item[%d]=%+v want=%+v", i, item, want[i])
		}
		if !item.PublishedAt.Equal(want[i].PublishedAt) || item.PublishedAt.Location() != time.UTC {
			t.Fatalf("item[%d] published=%v want=%v", i, item.PublishedAt, want[i].PublishedAt)
		}
	}
}

// 测试目标：解码校验载荷版本、条数上限、标识唯一与游标范围
// 预期效果：合法载荷通过，其余载荷全部归类为无效缓存页
func TestDecodePageValidatesPayloadShape(t *testing.T) {
	instant := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	cursor := &domainfeed.TimelineCursor{PublishedAt: instant, VideoID: 100}
	query := timelineQuery(cursor, 3)
	valid := payloadBefore(4, instant)
	validJSON := marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: valid})
	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "valid at limit plus one", payload: validJSON},
		{name: "valid empty items", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{}})},
		{name: "over limit", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: payloadBefore(5, instant)}), wantErr: true},
		{name: "items null", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion}), wantErr: true},
		{name: "items missing", payload: `{"version":1,"sort_version":1}`, wantErr: true},
		{name: "unknown field", payload: `{"version":1,"sort_version":1,"items":[],"extra":1}`, wantErr: true},
		{name: "trailing json", payload: validJSON + `{"version":1}`, wantErr: true},
		{name: "invalid json", payload: `{"version":1,`, wantErr: true},
		{name: "version mismatch", payload: marshalPayload(t, pagePayload{Version: 2, SortVersion: applicationfeed.TimelinePageSortVersion, Items: valid}), wantErr: true},
		{name: "sort version mismatch", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion + 1, Items: valid}), wantErr: true},
		{name: "author id null", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{{VideoID: 90, PublishedAt: instant.Add(-time.Minute)}}}), wantErr: true},
		{name: "author id missing", payload: `{"version":1,"sort_version":1,"items":[{"video_id":90,"published_at":"2024-03-04T05:05:07Z"}]}`, wantErr: true},
		{name: "video id zero", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(0, 1, instant.Add(-time.Minute))}}), wantErr: true},
		{name: "duplicate video id", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(90, 1, instant.Add(-2*time.Minute)), payloadItem(90, 2, instant.Add(-time.Minute))}}), wantErr: true},
		{name: "ascending order", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{valid[1], valid[0]}}), wantErr: true},
		{name: "same time ascending video", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(90, 1, instant.Add(-time.Minute)), payloadItem(91, 1, instant.Add(-time.Minute))}}), wantErr: true},
		{name: "cursor position inclusive", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(cursor.VideoID, 1, instant)}}), wantErr: true},
		{name: "cursor time passed", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(1, 1, instant.Add(time.Minute))}}), wantErr: true},
		{name: "zero published at", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(90, 1, time.Time{})}}), wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			page, err := decodePage(testCase.payload, query, defaultMaxPayloadSize)
			if testCase.wantErr {
				if !errors.Is(err, applicationfeed.ErrInvalidCachedPage) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if page.Items == nil {
				t.Fatalf("items=%#v", page.Items)
			}
		})
	}
}

type scriptFunc func(context.Context, string, []string, ...any) (any, error)

func (f scriptFunc) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return f(ctx, script, keys, args...)
}
func adapterCard(id uint) domainfeed.FeedCard {
	return domainfeed.FeedCard{VideoID: id, PublishedAt: time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC), PlayURL: "/v", PlayFileName: "v", PlayOriginalName: "v.mp4", CoverURL: "/c", CoverFileName: "c", CoverOriginalName: "c.jpg"}
}
func encodedCard(t *testing.T, id uint) string {
	t.Helper()
	card := adapterCard(id)
	b, err := json.Marshal(cardPayload{Version: 1, Card: &cachedCard{FeedCard: card, AuthorID: &card.AuthorID}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 测试目标：验证缓存载荷的严格结构和公开字段边界
// 预期效果：错误版本、额外字段、尾随对象、错误标识和缺失媒体字段均视为无效，零作者保持兼容
func TestCardPayloadContract(t *testing.T) {
	good := encodedCard(t, 1)
	for name, body := range map[string]string{
		"version":        strings.Replace(good, `"version":1`, `"version":2`, 1),
		"unknown":        strings.Replace(good, `"version":1`, `"version":1,"extra":true`, 1),
		"nested_unknown": strings.Replace(good, `"VideoID":1`, `"VideoID":1,"extra":true`, 1),
		"trailing":       good + `{}`, "wrong_id": encodedCard(t, 2), "null": `{"version":1,"card":null}`,
		"missing_media":  strings.Replace(good, `"CoverOriginalName":"c.jpg"`, `"CoverOriginalName":""`, 1),
		"missing_time":   strings.Replace(good, `"PublishedAt":"2026-08-01T01:00:00Z"`, `"PublishedAt":null`, 1),
		"missing_author": strings.Replace(good, `,"AuthorID":0`, ``, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCard(body, 1, 16384); !errors.Is(err, applicationfeed.ErrInvalidCachedCard) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if card, err := decodeCard(good, 1, 16384); err != nil || card.AuthorID != 0 {
		t.Fatalf("card=%v err=%v", card, err)
	}
	if _, err := decodeCard(good, 1, len(good)-1); err == nil {
		t.Fatal("载荷上限未生效")
	}
}

// 测试目标：验证混合命中批次和超大写入的有界操作
// 预期效果：单次批量调用保留好值，坏值计数，超大卡片跳过且不阻止其他卡片回填
func TestCardAdapterMixedBatchAndOversized(t *testing.T) {
	calls := 0
	cache, err := NewCardCache(scriptFunc(func(_ context.Context, _ string, keys []string, args ...any) (any, error) {
		calls++
		if len(keys) != 4 || keys[0] != "gofeed:feed:card:v1:2" || args[0] != 16384 {
			t.Fatalf("keys=%v args=%v", keys, args)
		}
		return []any{encodedCard(t, 2), nil, int64(1), "broken"}, nil
	}), CardCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := cache.GetCards(t.Context(), []uint{0, 2, 1, 2, 3, 4})
	if err != nil || len(result.Cards) != 1 || result.InvalidCount != 2 || calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
	cache, err = NewCardCache(scriptFunc(func(_ context.Context, _ string, keys []string, args ...any) (any, error) {
		if len(keys) != 1 || keys[0] != "gofeed:feed:card:v1:1" || args[0] != int64(30000) {
			t.Fatalf("keys=%v ttl=%v", keys, args[0])
		}
		return int64(1), nil
	}), CardCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	large := adapterCard(2)
	large.Description = strings.Repeat("x", 16384)
	written, err := cache.SetCards(t.Context(), []domainfeed.FeedCard{large, adapterCard(1), adapterCard(1)})
	if err != nil || written.Stored != 1 || written.SkippedOversized != 1 {
		t.Fatalf("written=%+v err=%v", written, err)
	}
}

// 测试目标：在真实 Redis 上批量访问卡片并持有精确清理键
// 预期效果：只操作随机前缀内的已知键，不扫描共享实例
func (c *namespacedPageCache) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	full := make([]string, len(keys))
	c.mu.Lock()
	for i, key := range keys {
		full[i] = c.prefix + key
		c.keys = append(c.keys, full[i])
	}
	c.mu.Unlock()
	return c.client.Eval(ctx, script, full, args...)
}

// 测试目标：验证真实 Redis 卡片往返、超大值保护、损坏和默认 TTL 自然过期
// 预期效果：真实命中及删除可观测，超大值不返回给驱动，30 秒默认 TTL 到期后缺失并精确清理
func TestRealRedisCardCacheLifecycle(t *testing.T) {
	_, wrapped := realRedisCache(t, PageCacheOptions{})
	cache, err := NewCardCache(wrapped, CardCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	write, err := cache.SetCards(t.Context(), []domainfeed.FeedCard{adapterCard(1), adapterCard(2)})
	if err != nil || write.Stored != 2 {
		t.Fatalf("write=%v err=%v", write, err)
	}
	read, err := cache.GetCards(t.Context(), []uint{1, 2, 3})
	if err != nil || len(read.Cards) != 2 {
		t.Fatalf("read=%v err=%v", read, err)
	}
	ttl, err := wrapped.client.Eval(t.Context(), "return redis.call('PTTL',KEYS[1])", []string{wrapped.prefix + cardKeys([]uint{1})[0]})
	if err != nil || ttl.(int64) < 29000 || ttl.(int64) > 30000 {
		t.Fatalf("TTL=%v err=%v", ttl, err)
	}
	if err := wrapped.Set(t.Context(), cardKeys([]uint{2})[0], strings.Repeat("x", 16385), time.Minute); err != nil {
		t.Fatal(err)
	}
	read, err = cache.GetCards(t.Context(), []uint{1, 2})
	if err != nil || len(read.Cards) != 1 || read.InvalidCount != 1 {
		t.Fatalf("read=%v err=%v", read, err)
	}
	if err := cache.DeleteCards(t.Context(), []uint{2, 2}); err != nil {
		t.Fatal(err)
	}
	read, err = cache.GetCards(t.Context(), []uint{2})
	if err != nil || len(read.Cards) != 0 || read.InvalidCount != 0 {
		t.Fatalf("read=%v err=%v", read, err)
	}
	select {
	case <-time.After(31 * time.Second):
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	read, err = cache.GetCards(t.Context(), []uint{1})
	if err != nil || len(read.Cards) != 0 {
		t.Fatalf("TTL 未自然过期 read=%v err=%v", read, err)
	}
	t.Log("真实 Redis 两条卡片命中，超大值被标记，精确删除与默认 30s 自然过期通过")
}

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
