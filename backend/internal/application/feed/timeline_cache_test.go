package applicationfeed

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

type cacheKey struct {
	scene    domainfeed.Scene
	limit    int
	position string
}

func keyOf(query PageCacheQuery) cacheKey {
	position := "start"
	if query.Cursor != nil {
		position = query.Cursor.PublishedAt.UTC().Format(time.RFC3339Nano) + ":" +
			strconv.FormatUint(uint64(query.Cursor.VideoID), 10)
	}
	return cacheKey{scene: query.Scene, limit: query.Limit, position: position}
}

// fakePageCache 同时具备可脚本化的故障注入与真实的内存往返能力
type fakePageCache struct {
	mu sync.Mutex

	entries map[cacheKey]CachedPage
	gets    int
	sets    int
	lastSet CachedPage
	hasSet  bool

	getErr  error
	setErr  error
	forced  *CachedPage
	entered chan struct{}
	block   <-chan struct{}
}

func newFakePageCache() *fakePageCache {
	return &fakePageCache{entries: map[cacheKey]CachedPage{}}
}

func (f *fakePageCache) GetPage(ctx context.Context, query PageCacheQuery) (CachedPage, bool, error) {
	f.mu.Lock()
	f.gets++
	getErr := f.getErr
	forced := f.forced
	entered := f.entered
	block := f.block
	f.mu.Unlock()

	if entered != nil {
		entered <- struct{}{}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return CachedPage{}, false, ctx.Err()
		}
	}
	if getErr != nil {
		return CachedPage{}, false, getErr
	}
	if forced != nil {
		return *forced, true, nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	page, ok := f.entries[keyOf(query)]
	return page, ok, nil
}

func (f *fakePageCache) SetPage(_ context.Context, query PageCacheQuery, page CachedPage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	f.lastSet = page
	f.hasSet = true
	if f.setErr != nil {
		return f.setErr
	}
	if f.entries == nil {
		f.entries = map[cacheKey]CachedPage{}
	}
	f.entries[keyOf(query)] = page
	return nil
}

func (f *fakePageCache) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

func (f *fakePageCache) setCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sets
}

func (f *fakePageCache) lastSetPage() (CachedPage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSet, f.hasSet
}

type fakeCardReader struct {
	mu    sync.Mutex
	calls int
	ids   []uint
	fn    func(videoIDs []uint) (map[uint]domainfeed.FeedCard, error)
}

func (f *fakeCardReader) BatchGetCards(_ context.Context, videoIDs []uint) (map[uint]domainfeed.FeedCard, error) {
	f.mu.Lock()
	f.calls++
	f.ids = append([]uint(nil), videoIDs...)
	fn := f.fn
	f.mu.Unlock()
	if fn == nil {
		return map[uint]domainfeed.FeedCard{}, nil
	}
	return fn(videoIDs)
}

func (f *fakeCardReader) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeCardReader) idList() []uint {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint(nil), f.ids...)
}

func (f *fakeCardReader) setFunc(fn func(videoIDs []uint) (map[uint]domainfeed.FeedCard, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fn = fn
}

type cacheObserverRecorder struct {
	mu      sync.Mutex
	results []string
}

func (o *cacheObserverRecorder) observe(observation CacheObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.results = append(o.results, observation.Result)
}

func (o *cacheObserverRecorder) contains(result string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, item := range o.results {
		if item == result {
			return true
		}
	}
	return false
}

func (o *cacheObserverRecorder) list() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.results...)
}

// pageStartingAt 构造从第 start 条开始的页，用于游标之后的后续页
func pageStartingAt(start, count int) domainfeed.TimelinePage {
	page := domainfeed.TimelinePage{
		Items: make([]domainfeed.FeedPageItem, 0, count),
		Cards: make(map[uint]domainfeed.FeedCard, count),
	}
	for index := start; index < start+count; index++ {
		item := pageItemAt(index, 7)
		page.Items = append(page.Items, item)
		page.Cards[item.VideoID] = cardOf(item)
	}
	return page
}

func cardsForItems(items []domainfeed.FeedPageItem) map[uint]domainfeed.FeedCard {
	cards := make(map[uint]domainfeed.FeedCard, len(items))
	for _, item := range items {
		cards[item.VideoID] = cardOf(item)
	}
	return cards
}

func encodedCursorAt(t *testing.T, index int) string {
	t.Helper()
	encoded, err := encodeTimelineCursor(cursorAt(index))
	if err != nil {
		t.Fatalf("构造游标失败: %v", err)
	}
	return encoded
}

func waitSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}

// 测试目标：首屏必须绕过页缓存，既不读取也不回填
// 预期效果：缓存读写与卡片读取都不发生，仅记录 first_page 与一次 MySQL 回源
func TestFeedCacheBypassesFirstPage(t *testing.T) {
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		return pageStartingAt(0, 3), nil
	}
	cache := newFakePageCache()
	cards := &fakeCardReader{}
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, cards, observer.observe))

	result, err := service.GetFeed(context.Background(), FeedRequest{Limit: 2})
	if err != nil {
		t.Fatalf("首屏失败: %v", err)
	}
	if cache.getCount() != 0 || cache.setCount() != 0 {
		t.Fatalf("首屏不应触碰缓存 got gets=%d sets=%d", cache.getCount(), cache.setCount())
	}
	if cards.callCount() != 0 {
		t.Fatalf("首屏不应批量读取卡片 got calls=%d", cards.callCount())
	}
	if !observer.contains("first_page") {
		t.Fatalf("应记录 first_page got=%v", observer.list())
	}
	if len(result.Items) != 2 || result.NextCursor == "" {
		t.Fatalf("首屏分页结果异常 got items=%d cursor=%q", len(result.Items), result.NextCursor)
	}
}

// 测试目标：后续页未命中时回源 MySQL 并回填完整的探测页
// 预期效果：回填条目为 limit+1 条且包含探测记录，命中前不读取卡片
func TestFeedCacheMissBackfillsFullProbePage(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		return pageStartingAt(1, 4), nil
	}
	cache := newFakePageCache()
	cards := &fakeCardReader{}
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, cards, observer.observe))

	result, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3})
	if err != nil {
		t.Fatalf("后续页失败: %v", err)
	}
	if cache.getCount() != 1 || cards.callCount() != 0 {
		t.Fatalf("未命中路径异常 got gets=%d cards=%d", cache.getCount(), cards.callCount())
	}
	page, ok := cache.lastSetPage()
	if !ok {
		t.Fatal("未命中后应回填缓存")
	}
	if len(page.Items) != 4 {
		t.Fatalf("回填必须是完整探测页 got=%d want=4", len(page.Items))
	}
	if page.Items[3].VideoID != pageItemAt(4, 7).VideoID {
		t.Fatalf("回填应包含探测记录 got=%+v", page.Items[3])
	}
	if len(result.Items) != 3 || result.NextCursor == "" {
		t.Fatalf("响应分页结果异常 got items=%d cursor=%q", len(result.Items), result.NextCursor)
	}
	if !observer.contains("miss") || !observer.contains("write_ok") || !observer.contains("mysql_read") {
		t.Fatalf("观测结果缺失 got=%v", observer.list())
	}
}

// 测试目标：缓存命中时校验整页当前公开卡片后直接组装响应
// 预期效果：命中路径不查询 MySQL，探测记录同样参与卡片校验
func TestFeedCacheHitValidatesWholePage(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	page := pageStartingAt(1, 4)
	repo := &stubRepository{}
	cache := newFakePageCache()
	cache.forced = &CachedPage{Items: page.Items}
	cards := &fakeCardReader{}
	cards.setFunc(func([]uint) (map[uint]domainfeed.FeedCard, error) {
		return cardsForItems(page.Items), nil
	})
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, cards, observer.observe))

	result, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3})
	if err != nil {
		t.Fatalf("命中路径失败: %v", err)
	}
	if repo.listCallCount() != 0 {
		t.Fatalf("命中校验通过后不应回源 got calls=%d", repo.listCallCount())
	}
	if got := cards.idList(); !equalIDs(got, []uint{899, 898, 897, 896}) {
		t.Fatalf("卡片批次应覆盖整页含探测记录 got=%v", got)
	}
	if len(result.Items) != 3 || result.NextCursor == "" {
		t.Fatalf("响应分页结果异常 got items=%d cursor=%q", len(result.Items), result.NextCursor)
	}
	if got := repo.statIDList(); !equalIDs(got, []uint{899, 898, 897}) {
		t.Fatalf("统计批次应只覆盖最终响应页 got=%v", got)
	}
	if !observer.contains("hit") {
		t.Fatalf("应记录命中 got=%v", observer.list())
	}
}

// 测试目标：缓存页与当前公开卡片不一致时必须整页回源而不是过滤旧分页
// 预期效果：卡片缺失、作者变化、发布时间变化都触发回源并重新回填
func TestFeedCacheStalePageReloadsFromDatabase(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(cards map[uint]domainfeed.FeedCard, items []domainfeed.FeedPageItem)
	}{
		{"卡片缺失", func(cards map[uint]domainfeed.FeedCard, items []domainfeed.FeedPageItem) {
			delete(cards, items[1].VideoID)
		}},
		{"作者变化", func(cards map[uint]domainfeed.FeedCard, items []domainfeed.FeedPageItem) {
			card := cards[items[0].VideoID]
			card.AuthorID++
			cards[items[0].VideoID] = card
		}},
		{"发布时间变化", func(cards map[uint]domainfeed.FeedCard, items []domainfeed.FeedPageItem) {
			card := cards[items[0].VideoID]
			card.PublishedAt = card.PublishedAt.Add(time.Second)
			cards[items[0].VideoID] = card
		}},
		{"视频被删除", func(cards map[uint]domainfeed.FeedCard, items []domainfeed.FeedPageItem) {
			for _, item := range items {
				delete(cards, item.VideoID)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encodedCursorAt(t, 0)
			page := pageStartingAt(1, 4)
			repo := &stubRepository{}
			repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
				return pageStartingAt(1, 4), nil
			}
			cache := newFakePageCache()
			cache.forced = &CachedPage{Items: page.Items}
			cards := &fakeCardReader{}
			changed := cardsForItems(page.Items)
			tc.mutate(changed, page.Items)
			cards.setFunc(func([]uint) (map[uint]domainfeed.FeedCard, error) {
				return changed, nil
			})
			observer := &cacheObserverRecorder{}
			service := New(repo, WithPageCache(cache, cards, observer.observe))

			result, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3})
			if err != nil {
				t.Fatalf("失效回源失败: %v", err)
			}
			if repo.listCallCount() != 1 {
				t.Fatalf("失效页必须整页回源 got calls=%d", repo.listCallCount())
			}
			if cache.setCount() != 1 {
				t.Fatalf("回源后应重新回填 got sets=%d", cache.setCount())
			}
			if len(result.Items) != 3 {
				t.Fatalf("响应应来自新回源页 got=%d", len(result.Items))
			}
			if !observer.contains("stale") {
				t.Fatalf("应记录 stale got=%v", observer.list())
			}
		})
	}
}

// 测试目标：缓存读取失败后不在同一请求继续尝试回填
// 预期效果：回源成功但写入次数为零，并记录 read_failed
func TestFeedCacheReadFailureSkipsBackfill(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		return pageStartingAt(1, 4), nil
	}
	cache := newFakePageCache()
	cache.getErr = errors.New("redis connection refused")
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, &fakeCardReader{}, observer.observe))

	result, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3})
	if err != nil {
		t.Fatalf("读取失败应回源成功 got error=%v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("响应应来自 MySQL got=%d", len(result.Items))
	}
	if cache.setCount() != 0 {
		t.Fatalf("读取失败后不应尝试回填 got sets=%d", cache.setCount())
	}
	if !observer.contains("read_failed") || !observer.contains("mysql_read") {
		t.Fatalf("观测结果缺失 got=%v", observer.list())
	}
}

// 测试目标：非法缓存载荷可由正确的 MySQL 结果覆盖
// 预期效果：记录 invalid_payload 后回源并成功回填
func TestFeedCacheInvalidPayloadIsOverwritten(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		return pageStartingAt(1, 4), nil
	}
	cache := newFakePageCache()
	cache.getErr = fmt.Errorf("%w: %w", ErrInvalidCachedPage, errors.New("unexpected field"))
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, &fakeCardReader{}, observer.observe))

	if _, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3}); err != nil {
		t.Fatalf("非法载荷应回源成功 got error=%v", err)
	}
	if cache.setCount() != 1 {
		t.Fatalf("非法载荷应被正确结果覆盖 got sets=%d", cache.setCount())
	}
	if !observer.contains("invalid_payload") || !observer.contains("write_ok") {
		t.Fatalf("观测结果缺失 got=%v", observer.list())
	}
}

// 测试目标：缓存写入失败不能改变已经成功的响应
// 预期效果：结果与无缓存时一致并记录 write_failed
func TestFeedCacheWriteFailureKeepsSuccessResult(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		return pageStartingAt(1, 4), nil
	}
	cache := newFakePageCache()
	cache.setErr = errors.New("redis write timeout")
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, &fakeCardReader{}, observer.observe))

	result, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3})
	if err != nil {
		t.Fatalf("写失败不应影响响应 got error=%v", err)
	}
	if len(result.Items) != 3 || result.NextCursor == "" {
		t.Fatalf("响应分页结果异常 got items=%d cursor=%q", len(result.Items), result.NextCursor)
	}
	if !observer.contains("write_failed") {
		t.Fatalf("应记录 write_failed got=%v", observer.list())
	}
}

// 测试目标：有效空页也能作为缓存载荷回填
// 预期效果：空页通过校验并写入，响应为非 nil 空切片且无下一页游标
func TestFeedCacheBackfillsValidEmptyPage(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		return pageStartingAt(1, 0), nil
	}
	cache := newFakePageCache()
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, &fakeCardReader{}, observer.observe))

	result, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3})
	if err != nil {
		t.Fatalf("有效空页失败: %v", err)
	}
	if result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("空页应输出非 nil 空切片 got=%v", result.Items)
	}
	if result.NextCursor != "" {
		t.Fatalf("空页不应给出下一页游标 got=%q", result.NextCursor)
	}
	page, ok := cache.lastSetPage()
	if !ok || len(page.Items) != 0 {
		t.Fatalf("有效空页应回填 got page=%+v ok=%v", page.Items, ok)
	}
	if !observer.contains("write_ok") {
		t.Fatalf("应记录 write_ok got=%v", observer.list())
	}
}

// 测试目标：卡片读取遇到数据库错误不能伪装成空页成功
// 预期效果：命中路径直接返回错误，不回源也不回填
func TestFeedCacheCardReadFailureIsNotAnEmptyPage(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	page := pageStartingAt(1, 4)
	cardError := errors.New("card database is down")
	repo := &stubRepository{}
	cache := newFakePageCache()
	cache.forced = &CachedPage{Items: page.Items}
	cards := &fakeCardReader{}
	cards.setFunc(func([]uint) (map[uint]domainfeed.FeedCard, error) {
		return nil, cardError
	})
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, cards, observer.observe))

	result, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3})
	if !errors.Is(err, cardError) {
		t.Fatalf("卡片读取失败必须返回错误 got error=%v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("失败不应返回条目 got=%d", len(result.Items))
	}
	if repo.listCallCount() != 0 || cache.setCount() != 0 {
		t.Fatalf("卡片失败不应回源或回填 got list=%d sets=%d", repo.listCallCount(), cache.setCount())
	}
	if !observer.contains("card_read_failed") {
		t.Fatalf("应记录 card_read_failed got=%v", observer.list())
	}
}

// 测试目标：缓存操作容量耗尽时跳过缓存直接回源
// 预期效果：记录 cache_busy 且响应仍由 MySQL 结果组成
func TestFeedCacheBusySkipsCacheAndReadsDatabase(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		return pageStartingAt(1, 4), nil
	}
	cache := newFakePageCache()
	cache.getErr = errPageCacheBusy
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, &fakeCardReader{}, observer.observe))

	result, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3})
	if err != nil {
		t.Fatalf("缓存忙应回源成功 got error=%v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("响应应来自 MySQL got=%d", len(result.Items))
	}
	if cache.setCount() != 0 {
		t.Fatalf("缓存忙时不应尝试回填 got sets=%d", cache.setCount())
	}
	if !observer.contains("cache_busy") {
		t.Fatalf("应记录 cache_busy got=%v", observer.list())
	}
}

// 测试目标：回填容量耗尽同样不影响响应
// 预期效果：写入失败路径记录 write_failed 但响应成功
func TestFeedCacheBusyOnWriteKeepsSuccessResult(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		return pageStartingAt(1, 4), nil
	}
	cache := newFakePageCache()
	cache.setErr = errPageCacheBusy
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, &fakeCardReader{}, observer.observe))

	if _, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3}); err != nil {
		t.Fatalf("回填忙不应影响响应 got error=%v", err)
	}
	if !observer.contains("write_failed") {
		t.Fatalf("应记录 write_failed got=%v", observer.list())
	}
}

// 测试目标：旧请求回填的失效轻量页在下次读取时仍会被校验并整页回源
// 预期效果：第二次请求记录 stale、重新查询 MySQL 并覆盖缓存
func TestFeedCacheStaleBackfillIsRevalidated(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	page := pageStartingAt(1, 4)
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		return pageStartingAt(1, 4), nil
	}
	cache := newFakePageCache()
	cards := &fakeCardReader{}
	cards.setFunc(func([]uint) (map[uint]domainfeed.FeedCard, error) {
		return cardsForItems(page.Items), nil
	})
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, cards, observer.observe))
	ctx := context.Background()

	if _, err := service.GetFeed(ctx, FeedRequest{Cursor: encoded, Limit: 3}); err != nil {
		t.Fatalf("首次回填失败: %v", err)
	}
	if cache.setCount() != 1 || repo.listCallCount() != 1 {
		t.Fatalf("首次请求应回源并回填 got sets=%d list=%d", cache.setCount(), repo.listCallCount())
	}

	changed := cardsForItems(page.Items)
	card := changed[page.Items[0].VideoID]
	card.AuthorID++
	changed[page.Items[0].VideoID] = card
	cards.setFunc(func([]uint) (map[uint]domainfeed.FeedCard, error) {
		return changed, nil
	})

	if _, err := service.GetFeed(ctx, FeedRequest{Cursor: encoded, Limit: 3}); err != nil {
		t.Fatalf("第二次请求失败: %v", err)
	}
	if repo.listCallCount() != 2 {
		t.Fatalf("失效载荷必须整页回源 got list=%d", repo.listCallCount())
	}
	if cache.setCount() != 2 {
		t.Fatalf("回源后应重新回填 got sets=%d", cache.setCount())
	}
	if !observer.contains("stale") {
		t.Fatalf("应记录 stale got=%v", observer.list())
	}
}

// 测试目标：并发读取容量上限为 32 个启用缓存的 Feed 请求
// 预期效果：超出上限的请求返回安全不可用，释放后容量恢复
func TestFeedCacheReadCapacityLimit(t *testing.T) {
	repo := &stubRepository{}
	cache := newFakePageCache()
	observer := &cacheObserverRecorder{}
	service := New(repo, WithPageCache(cache, &fakeCardReader{}, observer.observe))

	probeReadCapacity(t, service, repo)

	if !observer.contains("read_busy") {
		t.Fatalf("容量耗尽应记录 read_busy got=%v", observer.list())
	}
}

// 测试目标：缓存操作容量上限为 16 个并发操作
// 预期效果：第 17 个请求跳过缓存并在容量释放后仍能正常工作
func TestFeedCacheOperationCapacityLimit(t *testing.T) {
	encoded := encodedCursorAt(t, 0)
	page := pageStartingAt(1, 4)

	entered := make(chan struct{}, maxCacheOperations+4)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	cache := newFakePageCache()
	cache.entered = entered
	cache.block = release
	cache.forced = &CachedPage{Items: page.Items}
	cards := &fakeCardReader{}
	cards.setFunc(func([]uint) (map[uint]domainfeed.FeedCard, error) {
		return cardsForItems(page.Items), nil
	})
	observer := &cacheObserverRecorder{}
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		return pageStartingAt(1, 4), nil
	}
	service := New(repo, WithPageCache(cache, cards, observer.observe))

	var wg sync.WaitGroup
	errs := make([]error, maxCacheOperations)
	for index := 0; index < maxCacheOperations; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, errs[slot] = service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3})
		}(index)
	}
	for index := 0; index < maxCacheOperations; index++ {
		waitSignal(t, entered, "并发缓存操作未达到容量上限")
	}

	result, err := service.GetFeed(context.Background(), FeedRequest{Cursor: encoded, Limit: 3})
	if err != nil {
		t.Fatalf("容量耗尽应跳过缓存 got error=%v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("跳过缓存后应回源成功 got=%d", len(result.Items))
	}
	if cache.getCount() != maxCacheOperations {
		t.Fatalf("容量耗尽时不应真正访问缓存 got=%d", cache.getCount())
	}
	if !observer.contains("cache_busy") {
		t.Fatalf("应记录 cache_busy got=%v", observer.list())
	}

	unblock()
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个并发请求失败: %v", index, err)
		}
	}
}

// 测试目标：父上下文已取消时不占用并发槽也不访问依赖
// 预期效果：返回包装后的不可用错误并保留取消原因
func TestFeedCacheRespectsCancelledContext(t *testing.T) {
	repo := &stubRepository{}
	cache := newFakePageCache()
	service := New(repo, WithPageCache(cache, &fakeCardReader{}, nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.GetFeed(ctx, FeedRequest{})
	if !errors.Is(err, domainfeed.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("got error=%v want ErrUnavailable 且 context.Canceled", err)
	}
	if repo.listCallCount() != 0 || cache.getCount() != 0 {
		t.Fatalf("取消后不应访问依赖 got list=%d gets=%d", repo.listCallCount(), cache.getCount())
	}
}

// 测试目标：读取中途取消也必须释放并发槽
// 预期效果：取消请求返回后仍可再次占满 32 个读取容量
func TestFeedCacheReleasesReadSlotOnCancel(t *testing.T) {
	repo := &stubRepository{}
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	repo.listEntered = entered
	repo.listBlock = release

	cache := newFakePageCache()
	service := New(repo, WithPageCache(cache, &fakeCardReader{}, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.GetFeed(ctx, FeedRequest{})
		done <- err
	}()

	waitSignal(t, entered, "请求未进入仓储读取")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后应返回取消原因 got error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后请求未返回")
	}

	close(release)
	repo.mu.Lock()
	repo.listEntered = nil
	repo.listBlock = nil
	repo.mu.Unlock()

	probeReadCapacity(t, service, repo)
}

// probeReadCapacity 验证读取容量可被完全占满，超额请求被拒绝，释放后可恢复
func probeReadCapacity(t *testing.T, service *Service, repo *stubRepository) {
	t.Helper()

	entered := make(chan struct{}, maxCachedFeedReads+4)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	repo.mu.Lock()
	repo.listEntered = entered
	repo.listBlock = release
	repo.mu.Unlock()

	var wg sync.WaitGroup
	errs := make([]error, maxCachedFeedReads)
	for index := 0; index < maxCachedFeedReads; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, errs[slot] = service.GetFeed(context.Background(), FeedRequest{})
		}(index)
	}
	for index := 0; index < maxCachedFeedReads; index++ {
		waitSignal(t, entered, "并发读取未达到容量上限")
	}

	if _, err := service.GetFeed(context.Background(), FeedRequest{}); !errors.Is(err, domainfeed.ErrUnavailable) {
		t.Fatalf("容量耗尽 got error=%v want=%v", err, domainfeed.ErrUnavailable)
	}

	unblock()
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个并发请求失败: %v", index, err)
		}
	}

	if _, err := service.GetFeed(context.Background(), FeedRequest{}); err != nil {
		t.Fatalf("容量释放后请求失败: %v", err)
	}

	repo.mu.Lock()
	repo.listEntered = nil
	repo.listBlock = nil
	repo.mu.Unlock()
}
