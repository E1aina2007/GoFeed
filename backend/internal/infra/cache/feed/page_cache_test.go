package infracachefeed

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"

	redisdriver "github.com/redis/go-redis/v9"
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

// 测试目标：页缓存的契约常量与零值默认配置符合约定
// 预期效果：版本号、过期时间、超时和载荷上限取到既定数值
func TestPageCacheContractConstants(t *testing.T) {
	if pagePayloadVersion != 1 || applicationfeed.TimelinePageSortVersion != 1 {
		t.Fatalf("版本 page=%d sort=%d", pagePayloadVersion, applicationfeed.TimelinePageSortVersion)
	}
	if defaultPageTTL != 30*time.Second || maxPageTTL != 5*time.Minute {
		t.Fatalf("ttl default=%v max=%v", defaultPageTTL, maxPageTTL)
	}
	if defaultPageTimeout != 100*time.Millisecond || maxPageTimeout != time.Second {
		t.Fatalf("timeout default=%v max=%v", defaultPageTimeout, maxPageTimeout)
	}
	if defaultMaxPayloadSize != 16*1024 || maxPayloadSize != 64*1024 {
		t.Fatalf("payload default=%d max=%d", defaultMaxPayloadSize, maxPayloadSize)
	}
	cache := mustNewPageCache(t, newFakeStringCache(), PageCacheOptions{})
	if cache.options.TTL != defaultPageTTL ||
		cache.options.OperationTimeout != defaultPageTimeout ||
		cache.options.MaxPayloadBytes != defaultMaxPayloadSize {
		t.Fatalf("options=%+v", cache.options)
	}
}

// 测试目标：构造页缓存时校验配置边界并接受零值与合法上界
// 预期效果：负值和超上限返回错误且不返回实例，其余返回可用实例
func TestNewPageCacheValidatesOptions(t *testing.T) {
	cases := []struct {
		name    string
		options PageCacheOptions
		wantErr bool
	}{
		{name: "zero", options: PageCacheOptions{}},
		{name: "max boundary", options: PageCacheOptions{TTL: maxPageTTL, OperationTimeout: maxPageTimeout, MaxPayloadBytes: maxPayloadSize}},
		{name: "negative ttl", options: PageCacheOptions{TTL: -time.Second}, wantErr: true},
		{name: "ttl over max", options: PageCacheOptions{TTL: maxPageTTL + time.Nanosecond}, wantErr: true},
		{name: "negative timeout", options: PageCacheOptions{OperationTimeout: -time.Millisecond}, wantErr: true},
		{name: "timeout over max", options: PageCacheOptions{OperationTimeout: maxPageTimeout + time.Millisecond}, wantErr: true},
		{name: "negative payload", options: PageCacheOptions{MaxPayloadBytes: -1}, wantErr: true},
		{name: "payload over max", options: PageCacheOptions{MaxPayloadBytes: maxPayloadSize + 1}, wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cache, err := NewPageCache(newFakeStringCache(), testCase.options)
			if testCase.wantErr {
				if err == nil || cache != nil {
					t.Fatalf("cache=%v err=%v", cache, err)
				}
				return
			}
			if err != nil || cache == nil {
				t.Fatalf("cache=%v err=%v", cache, err)
			}
		})
	}
}

// 测试目标：构造页缓存期间不访问缓存客户端
// 预期效果：默认配置和显式配置下读写调用次数都为零
func TestNewPageCacheDoesNotCallClient(t *testing.T) {
	fake := newFakeStringCache()
	mustNewPageCache(t, fake, PageCacheOptions{})
	mustNewPageCache(t, fake, PageCacheOptions{TTL: time.Minute, OperationTimeout: maxPageTimeout, MaxPayloadBytes: maxPayloadSize})
	if gets, sets := fake.counts(); gets != 0 || sets != 0 {
		t.Fatalf("构造期间调用 get=%d set=%d", gets, sets)
	}
}

// 测试目标：客户端为空时拒绝构造
// 预期效果：返回可被 errors.Is 匹配的缓存不可用错误且不返回实例
func TestNewPageCacheRejectsNilClient(t *testing.T) {
	cache, err := NewPageCache(nil, PageCacheOptions{})
	if cache != nil || !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) {
		t.Fatalf("cache=%v err=%v", cache, err)
	}
}

// 测试目标：空接收者或空客户端的页缓存读写都报不可用
// 预期效果：读写返回可被 errors.Is 匹配的 ErrPageCacheUnavailable
func TestPageCacheUnavailableWithoutClient(t *testing.T) {
	query := timelineQuery(nil, 20)
	var nilCache *PageCache
	if _, _, err := nilCache.GetPage(t.Context(), query); !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) {
		t.Fatalf("空接收者读取 err=%v", err)
	}
	if err := nilCache.SetPage(t.Context(), query, applicationfeed.CachedPage{}); !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) {
		t.Fatalf("空接收者写入 err=%v", err)
	}
	empty := &PageCache{options: PageCacheOptions{OperationTimeout: defaultPageTimeout, MaxPayloadBytes: defaultMaxPayloadSize}}
	if _, _, err := empty.GetPage(t.Context(), query); !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) {
		t.Fatalf("空客户端读取 err=%v", err)
	}
	if err := empty.SetPage(t.Context(), query, applicationfeed.CachedPage{}); !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) {
		t.Fatalf("空客户端写入 err=%v", err)
	}
}

// 测试目标：未命中时返回零页且不报错
// 预期效果：命中标志为假、错误为空并按规范化键读取一次
func TestGetPageMissReturnsNoError(t *testing.T) {
	fake := newFakeStringCache()
	cache := mustNewPageCache(t, fake, PageCacheOptions{})
	query := timelineQuery(nil, 20)
	page, hit, err := cache.GetPage(t.Context(), query)
	if err != nil || hit || page.Items != nil {
		t.Fatalf("page=%+v hit=%v err=%v", page, hit, err)
	}
	if gets, sets := fake.counts(); gets != 1 || sets != 0 {
		t.Fatalf("get=%d set=%d", gets, sets)
	}
}

// 测试目标：合法空页载荷命中并保留非空切片语义
// 预期效果：命中标志为真且条目为零长度非空切片
func TestGetPageHitOnEmptyPagePayload(t *testing.T) {
	fake := newFakeStringCache()
	cache := mustNewPageCache(t, fake, PageCacheOptions{})
	query := timelineQuery(nil, 20)
	fake.seed(pageKey(query), `{"version":1,"sort_version":1,"items":[]}`)
	page, hit, err := cache.GetPage(t.Context(), query)
	if err != nil || !hit {
		t.Fatalf("hit=%v err=%v", hit, err)
	}
	if page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("items=%#v", page.Items)
	}
}

// 测试目标：非空页写入后可原样读回且发布时间归一为 UTC
// 预期效果：条目字段一致、写入使用默认过期时间、缺失键返回未命中
func TestSetPageRoundTripThroughCache(t *testing.T) {
	fake := newFakeStringCache()
	cache := mustNewPageCache(t, fake, PageCacheOptions{})
	offset := time.FixedZone("UTC+8", 8*3600)
	items := descendingItems(3, time.Date(2024, 5, 6, 7, 8, 9, 123456789, time.UTC))
	for i := range items {
		items[i].PublishedAt = items[i].PublishedAt.In(offset)
	}
	query := timelineQuery(nil, 20)
	page := applicationfeed.CachedPage{Items: items}
	if err := cache.SetPage(t.Context(), query, page); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	call := fake.lastSet(t)
	if call.key != pageKey(query) || call.expiration != defaultPageTTL {
		t.Fatalf("key=%q expiration=%v", call.key, call.expiration)
	}
	got, hit, err := cache.GetPage(t.Context(), query)
	if err != nil || !hit || len(got.Items) != len(items) {
		t.Fatalf("items=%#v hit=%v err=%v", got.Items, hit, err)
	}
	for i, item := range got.Items {
		if item.VideoID != items[i].VideoID || item.AuthorID != items[i].AuthorID {
			t.Fatalf("item[%d]=%+v want=%+v", i, item, items[i])
		}
		if !item.PublishedAt.Equal(items[i].PublishedAt) || item.PublishedAt.Location() != time.UTC {
			t.Fatalf("item[%d] published=%v want=%v", i, item.PublishedAt, items[i].PublishedAt)
		}
	}
	if _, anotherHit, err := cache.GetPage(t.Context(), timelineQuery(nil, 21)); err != nil || anotherHit {
		t.Fatalf("不同页大小不应命中 hit=%v err=%v", anotherHit, err)
	}
}

// 测试目标：写入前拒绝不满足契约的页且不触达客户端
// 预期效果：返回 ErrInvalidCachedPage 且写入调用次数为零
func TestSetPageRejectsInvalidPage(t *testing.T) {
	newest := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	valid := descendingItems(2, newest)
	duplicated := []domainfeed.FeedPageItem{valid[0], {VideoID: valid[0].VideoID, AuthorID: 1, PublishedAt: valid[0].PublishedAt.Add(-time.Minute)}}
	ascending := []domainfeed.FeedPageItem{valid[1], valid[0]}
	zeroID := []domainfeed.FeedPageItem{{VideoID: 0, AuthorID: 1, PublishedAt: newest}}
	zeroTime := []domainfeed.FeedPageItem{{VideoID: 5, AuthorID: 1}}
	cases := []struct {
		name  string
		query applicationfeed.PageCacheQuery
		page  applicationfeed.CachedPage
	}{
		{name: "duplicate video id", query: timelineQuery(nil, 20), page: applicationfeed.CachedPage{Items: duplicated}},
		{name: "ascending order", query: timelineQuery(nil, 20), page: applicationfeed.CachedPage{Items: ascending}},
		{name: "zero video id", query: timelineQuery(nil, 20), page: applicationfeed.CachedPage{Items: zeroID}},
		{name: "zero published at", query: timelineQuery(nil, 20), page: applicationfeed.CachedPage{Items: zeroTime}},
		{name: "over limit", query: timelineQuery(nil, 1), page: applicationfeed.CachedPage{Items: descendingItems(3, newest)}},
		{name: "invalid query", query: timelineQuery(nil, 0), page: applicationfeed.CachedPage{Items: valid}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeStringCache()
			cache := mustNewPageCache(t, fake, PageCacheOptions{})
			err := cache.SetPage(t.Context(), testCase.query, testCase.page)
			if !errors.Is(err, applicationfeed.ErrInvalidCachedPage) && !errors.Is(err, applicationfeed.ErrInvalidPageCacheQuery) {
				t.Fatalf("err=%v", err)
			}
			if gets, sets := fake.counts(); gets != 0 || sets != 0 {
				t.Fatalf("非法页仍调用 get=%d set=%d", gets, sets)
			}
		})
	}
}

// 测试目标：非法查询在触达客户端前被拒绝
// 预期效果：读写都返回 ErrInvalidPageCacheQuery 且调用次数为零
func TestPageCacheRejectsInvalidQuery(t *testing.T) {
	instant := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	cases := []struct {
		name  string
		query applicationfeed.PageCacheQuery
	}{
		{name: "unknown scene", query: applicationfeed.PageCacheQuery{Scene: domainfeed.SceneHot, Limit: 20}},
		{name: "empty scene", query: applicationfeed.PageCacheQuery{Limit: 20}},
		{name: "zero limit", query: timelineQuery(nil, 0)},
		{name: "over max limit", query: timelineQuery(nil, domainfeed.MaxLimit+1)},
		{name: "zero cursor video", query: timelineQuery(&domainfeed.TimelineCursor{PublishedAt: instant}, 20)},
		{name: "zero cursor time", query: timelineQuery(&domainfeed.TimelineCursor{VideoID: 3}, 20)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeStringCache()
			cache := mustNewPageCache(t, fake, PageCacheOptions{})
			if _, _, err := cache.GetPage(t.Context(), testCase.query); !errors.Is(err, applicationfeed.ErrInvalidPageCacheQuery) {
				t.Fatalf("读取 err=%v", err)
			}
			if err := cache.SetPage(t.Context(), testCase.query, applicationfeed.CachedPage{}); !errors.Is(err, applicationfeed.ErrInvalidPageCacheQuery) {
				t.Fatalf("写入 err=%v", err)
			}
			if gets, sets := fake.counts(); gets != 0 || sets != 0 {
				t.Fatalf("非法查询仍调用 get=%d set=%d", gets, sets)
			}
		})
	}
}

// 测试目标：读取阶段保留未命中并包装其余错误
// 预期效果：业务错误、超时与取消都标记缓存不可用且可 errors.Is 追溯
func TestGetPageErrorSemantics(t *testing.T) {
	query := timelineQuery(nil, 20)
	t.Run("business error", func(t *testing.T) {
		fake := newFakeStringCache()
		fake.getErr = errCacheBoom
		cache := mustNewPageCache(t, fake, PageCacheOptions{})
		_, hit, err := cache.GetPage(t.Context(), query)
		if hit || !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) || !errors.Is(err, errCacheBoom) {
			t.Fatalf("hit=%v err=%v", hit, err)
		}
	})
	t.Run("deadline exceeded", func(t *testing.T) {
		fake := newFakeStringCache()
		fake.getDelay = 2 * time.Second
		cache := mustNewPageCache(t, fake, PageCacheOptions{OperationTimeout: 50 * time.Millisecond})
		_, _, err := cache.GetPage(t.Context(), query)
		if !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("context canceled", func(t *testing.T) {
		fake := newFakeStringCache()
		fake.getDelay = 2 * time.Second
		cache := mustNewPageCache(t, fake, PageCacheOptions{})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, _, err := cache.GetPage(ctx, query)
		if !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) || !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
}

// 测试目标：写入阶段把客户端错误包装为缓存不可用
// 预期效果：业务错误、超时与取消都标记缓存不可用且可 errors.Is 追溯
func TestSetPageErrorSemantics(t *testing.T) {
	query := timelineQuery(nil, 20)
	page := applicationfeed.CachedPage{Items: descendingItems(1, time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC))}
	t.Run("business error", func(t *testing.T) {
		fake := newFakeStringCache()
		fake.setErr = errCacheBoom
		cache := mustNewPageCache(t, fake, PageCacheOptions{})
		err := cache.SetPage(t.Context(), query, page)
		if !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) || !errors.Is(err, errCacheBoom) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("deadline exceeded", func(t *testing.T) {
		fake := newFakeStringCache()
		fake.setDelay = 2 * time.Second
		cache := mustNewPageCache(t, fake, PageCacheOptions{OperationTimeout: 50 * time.Millisecond})
		err := cache.SetPage(t.Context(), query, page)
		if !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("context canceled", func(t *testing.T) {
		fake := newFakeStringCache()
		fake.setDelay = 2 * time.Second
		cache := mustNewPageCache(t, fake, PageCacheOptions{})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := cache.SetPage(ctx, query, page)
		if !errors.Is(err, applicationfeed.ErrPageCacheUnavailable) || !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
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

// 测试目标：缓存键对同一时刻的不同时区写法归一
// 预期效果：UTC 与各种固定偏移写出的游标得到完全相同的键
func TestPageKeyNormalizesCursorLocation(t *testing.T) {
	instant := time.Date(2024, 5, 6, 7, 8, 9, 123456789, time.UTC)
	locations := []*time.Location{
		time.UTC,
		time.FixedZone("UTC+8", 8*3600),
		time.FixedZone("UTC-5", -5*3600),
		time.FixedZone("UTC+5:45", 5*3600+45*60),
		time.Local,
	}
	base := pageKey(timelineQuery(&domainfeed.TimelineCursor{PublishedAt: instant, VideoID: 42}, 20))
	for _, location := range locations {
		cursor := &domainfeed.TimelineCursor{PublishedAt: instant.In(location), VideoID: 42}
		if key := pageKey(timelineQuery(cursor, 20)); key != base {
			t.Fatalf("location=%v key=%q want=%q", location, key, base)
		}
	}
}

// 测试目标：页大小、游标时刻、视频标识和场景任一不同都产生不同键
// 预期效果：各维度键互不相等且首屏起始键与游标键区分
func TestPageKeyIsolatesQueryDimensions(t *testing.T) {
	instant := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	cursor := &domainfeed.TimelineCursor{PublishedAt: instant, VideoID: 42}
	base := pageKey(timelineQuery(cursor, 20))
	variants := []struct {
		name  string
		query applicationfeed.PageCacheQuery
	}{
		{name: "limit", query: timelineQuery(cursor, 21)},
		{name: "cursor time", query: timelineQuery(&domainfeed.TimelineCursor{PublishedAt: instant.Add(time.Nanosecond), VideoID: 42}, 20)},
		{name: "cursor video", query: timelineQuery(&domainfeed.TimelineCursor{PublishedAt: instant, VideoID: 43}, 20)},
		{name: "scene", query: applicationfeed.PageCacheQuery{Scene: domainfeed.SceneHot, Cursor: cursor, Limit: 20}},
	}
	seen := map[string]string{base: "base"}
	for _, variant := range variants {
		key := pageKey(variant.query)
		if key == base {
			t.Fatalf("%s 未与基准键隔离: %q", variant.name, key)
		}
		if previous, exists := seen[key]; exists {
			t.Fatalf("%s 与 %s 键冲突: %q", variant.name, previous, key)
		}
		seen[key] = variant.name
	}
	if start := pageKey(timelineQuery(nil, 20)); start == base {
		t.Fatalf("首屏起始键与游标键相同: %q", start)
	}
}

// 测试目标：缓存键布局固定包含版本、场景、页大小与规范化游标
// 预期效果：键等于约定分段字符串且分别带载荷版本与排序版本
func TestPageKeyMatchesDocumentedLayout(t *testing.T) {
	instant := time.Date(2024, 1, 2, 3, 4, 5, 123456789, time.UTC)
	cursor := &domainfeed.TimelineCursor{PublishedAt: instant, VideoID: 7}
	payloadSegment := ":v" + strconv.Itoa(pagePayloadVersion) + ":"
	sortSegment := ":s" + strconv.Itoa(applicationfeed.TimelinePageSortVersion) + ":"
	want := "gofeed:feed:page" + payloadSegment + "timeline" + sortSegment + "l20:2024-01-02T03:04:05.123456789Z:7"
	key := pageKey(timelineQuery(cursor, 20))
	if key != want {
		t.Fatalf("key=%q want=%q", key, want)
	}
	if !strings.Contains(key, payloadSegment) || !strings.Contains(key, sortSegment) {
		t.Fatalf("键缺少版本分段: %q", key)
	}
	start := pageKey(timelineQuery(nil, 20))
	wantStart := "gofeed:feed:page" + payloadSegment + "timeline" + sortSegment + "l20:start"
	if start != wantStart {
		t.Fatalf("start key=%q want=%q", start, wantStart)
	}
}

// 测试目标：损坏载荷在读取路径上归类为无效缓存页
// 预期效果：返回可 errors.Is 匹配的 ErrInvalidCachedPage 而非不可用
func TestGetPageRejectsInvalidPayload(t *testing.T) {
	instant := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	query := timelineQuery(&domainfeed.TimelineCursor{PublishedAt: instant, VideoID: 42}, 20)
	payloads := map[string]string{
		"version mismatch": `{"version":2,"sort_version":1,"items":[]}`,
		"invalid json":     `{"version":1,`,
		"unknown field":    `{"version":1,"sort_version":1,"items":[],"extra":1}`,
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			fake := newFakeStringCache()
			cache := mustNewPageCache(t, fake, PageCacheOptions{})
			fake.seed(pageKey(query), payload)
			_, hit, err := cache.GetPage(t.Context(), query)
			if hit || !errors.Is(err, applicationfeed.ErrInvalidCachedPage) {
				t.Fatalf("hit=%v err=%v", hit, err)
			}
			if errors.Is(err, applicationfeed.ErrPageCacheUnavailable) {
				t.Fatalf("损坏载荷不应归类为不可用: %v", err)
			}
		})
	}
}
