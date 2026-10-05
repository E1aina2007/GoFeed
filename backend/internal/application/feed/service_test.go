package applicationfeed

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

// originTime 是所有测试页共用的最新发布时间基准
var originTime = time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)

// stubRepository 记录调用参数并按用例注入返回值
type stubRepository struct {
	mu sync.Mutex

	listCalls  int
	listCursor *domainfeed.TimelineCursor
	listLimit  int
	listFn     func(cursor *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error)

	// listEntered 与 listBlock 用于并发用例：先通知进入读取，再等待放行或上下文取消
	listEntered chan struct{}
	listBlock   <-chan struct{}

	statCalls int
	statIDs   []uint
	statFn    func(videoIDs []uint) (map[uint]domainfeed.FeedStat, error)

	authorCalls int
	authorIDs   []uint
	authorFn    func(authorIDs []uint) (map[uint]domainfeed.Author, error)
}

func (r *stubRepository) ListTimelinePage(ctx context.Context, cursor *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
	r.mu.Lock()
	r.listCalls++
	r.listLimit = limit
	if cursor == nil {
		r.listCursor = nil
	} else {
		copied := *cursor
		r.listCursor = &copied
	}
	fn := r.listFn
	entered := r.listEntered
	block := r.listBlock
	r.mu.Unlock()

	if entered != nil {
		entered <- struct{}{}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return domainfeed.TimelinePage{}, ctx.Err()
		}
	}
	if fn == nil {
		return domainfeed.TimelinePage{Items: []domainfeed.FeedPageItem{}, Cards: map[uint]domainfeed.FeedCard{}}, nil
	}
	return fn(cursor, limit)
}

func (r *stubRepository) BatchGetStats(_ context.Context, videoIDs []uint) (map[uint]domainfeed.FeedStat, error) {
	r.mu.Lock()
	r.statCalls++
	r.statIDs = append([]uint(nil), videoIDs...)
	fn := r.statFn
	r.mu.Unlock()
	if fn == nil {
		return defaultStats(videoIDs), nil
	}
	return fn(videoIDs)
}

func (r *stubRepository) BatchGetAuthors(_ context.Context, authorIDs []uint) (map[uint]domainfeed.Author, error) {
	r.mu.Lock()
	r.authorCalls++
	r.authorIDs = append([]uint(nil), authorIDs...)
	fn := r.authorFn
	r.mu.Unlock()
	if fn == nil {
		return defaultAuthors(authorIDs), nil
	}
	return fn(authorIDs)
}

func (r *stubRepository) observedLimit() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listLimit
}

func (r *stubRepository) observedCursor() *domainfeed.TimelineCursor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCursor
}

func (r *stubRepository) listCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCalls
}

func (r *stubRepository) statIDList() []uint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint(nil), r.statIDs...)
}

func (r *stubRepository) authorIDList() []uint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint(nil), r.authorIDs...)
}

func defaultAuthors(authorIDs []uint) map[uint]domainfeed.Author {
	authors := make(map[uint]domainfeed.Author, len(authorIDs))
	for _, id := range authorIDs {
		authors[id] = domainfeed.Author{
			ID: id, Username: fmt.Sprintf("作者%d", id), AvatarURL: fmt.Sprintf("/static/avatars/%d.png", id),
		}
	}
	return authors
}

func defaultStats(videoIDs []uint) map[uint]domainfeed.FeedStat {
	stats := make(map[uint]domainfeed.FeedStat, len(videoIDs))
	for index, id := range videoIDs {
		stats[id] = domainfeed.FeedStat{LikesCount: int64(index + 1), CommentsCount: int64(index + 2)}
	}
	return stats
}

// pageItemAt 生成第 index 条页条目，发布时间严格递减，视频 ID 同步递减
func pageItemAt(index int, authorID uint) domainfeed.FeedPageItem {
	return domainfeed.FeedPageItem{
		VideoID:     uint(900 - index),
		AuthorID:    authorID,
		PublishedAt: originTime.Add(-time.Duration(index) * time.Minute),
	}
}

// cardOf 生成与页条目严格一致的展示卡片
func cardOf(item domainfeed.FeedPageItem) domainfeed.FeedCard {
	return domainfeed.FeedCard{
		VideoID:           item.VideoID,
		AuthorID:          item.AuthorID,
		Title:             fmt.Sprintf("标题%d", item.VideoID),
		Description:       fmt.Sprintf("描述%d", item.VideoID),
		PlayURL:           fmt.Sprintf("/static/videos/%d/play.mp4", item.VideoID),
		PlayFileName:      "play.mp4",
		PlayOriginalName:  "原始 视频.mp4",
		CoverURL:          fmt.Sprintf("/static/covers/%d/cover.png", item.VideoID),
		CoverFileName:     "cover.png",
		CoverOriginalName: "原始 封面.png",
		PublishedAt:       item.PublishedAt,
	}
}

// cursorAt 取第 index 条页条目作为游标位置
func cursorAt(index int) *domainfeed.TimelineCursor {
	item := pageItemAt(index, 7)
	return &domainfeed.TimelineCursor{PublishedAt: item.PublishedAt, VideoID: item.VideoID}
}

type stateReaderFunc func(context.Context, []uint) (map[uint]domainfeed.FeedPageItem, error)

func (f stateReaderFunc) BatchGetPublicCardStates(ctx context.Context, ids []uint) (map[uint]domainfeed.FeedPageItem, error) {
	return f(ctx, ids)
}

type cardCacheStub struct {
	cards          map[uint]domainfeed.FeedCard
	getErr, setErr error
	gets, sets     int
	get            func(context.Context)
	set            func(context.Context)
	requested      []uint
}

func (c *cardCacheStub) GetCards(ctx context.Context, ids []uint) (CachedCards, error) {
	c.gets++
	c.requested = append([]uint(nil), ids...)
	if c.get != nil {
		c.get(ctx)
	}
	return CachedCards{Cards: c.cards}, c.getErr
}
func (c *cardCacheStub) SetCards(ctx context.Context, cards []domainfeed.FeedCard) (CardCacheWrite, error) {
	c.sets++
	if c.set != nil {
		c.set(ctx)
	}
	if c.setErr != nil {
		return CardCacheWrite{}, c.setErr
	}
	if c.cards == nil {
		c.cards = make(map[uint]domainfeed.FeedCard)
	}
	for _, card := range cards {
		c.cards[card.VideoID] = card
	}
	return CardCacheWrite{Stored: len(cards)}, nil
}
func (c *cardCacheStub) DeleteCards(_ context.Context, ids []uint) error {
	for _, id := range ids {
		delete(c.cards, id)
	}
	return nil
}

func cachedFixture(id uint) domainfeed.FeedCard {
	return domainfeed.FeedCard{VideoID: id, AuthorID: 0, PublishedAt: time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC), Title: "card", PlayURL: "/v", PlayFileName: "v", PlayOriginalName: "v.mp4", CoverURL: "/c", CoverFileName: "c", CoverOriginalName: "c.jpg"}
}
func fixtureStates(_ context.Context, ids []uint) (map[uint]domainfeed.FeedPageItem, error) {
	states := make(map[uint]domainfeed.FeedPageItem)
	for _, id := range ids {
		card := cachedFixture(id)
		states[id] = domainfeed.FeedPageItem{VideoID: id, AuthorID: card.AuthorID, PublishedAt: card.PublishedAt}
	}
	return states, nil
}
func fixtureSource() *fakeCardReader {
	return &fakeCardReader{fn: func(ids []uint) (map[uint]domainfeed.FeedCard, error) {
		cards := make(map[uint]domainfeed.FeedCard)
		for _, id := range ids {
			cards[id] = cachedFixture(id)
		}
		return cards, nil
	}}
}

// 测试目标：验证缓存故障回源与 MySQL 故障传播
// 预期效果：缓存读取失败不继续回填，写入失败保留成功卡片，事实源故障不伪装成空页
func TestCachedCardReaderFaults(t *testing.T) {
	boom := errors.New("unavailable")
	for _, kind := range []string{"get", "set", "state", "source"} {
		t.Run(kind, func(t *testing.T) {
			source, cache := fixtureSource(), &cardCacheStub{}
			states := stateReaderFunc(fixtureStates)
			switch kind {
			case "get":
				cache.getErr = boom
			case "set":
				cache.setErr = boom
			case "state":
				states = func(context.Context, []uint) (map[uint]domainfeed.FeedPageItem, error) { return nil, boom }
			case "source":
				source.fn = func([]uint) (map[uint]domainfeed.FeedCard, error) { return nil, boom }
			}
			reader, _ := NewCachedCardReader(source, states, cache, nil)
			cards, err := reader.BatchGetCards(t.Context(), []uint{1})
			if kind == "state" || kind == "source" {
				if !errors.Is(err, boom) {
					t.Fatalf("err=%v", err)
				}
			} else if err != nil || len(cards) != 1 {
				t.Fatalf("cards=%v err=%v", cards, err)
			}
			if kind == "get" && cache.sets != 0 {
				t.Fatal("读取失败不应回填")
			}
			if kind == "state" && cache.gets != 0 {
				t.Fatal("公开检查失败不应查缓存")
			}
		})
	}
}

// 测试目标：验证取消、批量边界与容量释放
// 预期效果：取消不返回迟到成功，超限和空批次无副作用，缓存饱和回源且后续可重新占用名额
func TestCachedCardReaderCancellationAndCapacity(t *testing.T) {
	for _, phase := range []string{"before", "get", "set"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cache := &cardCacheStub{}
			switch phase {
			case "before":
				cancel()
			case "get":
				cache.get = func(context.Context) { cancel() }
			case "set":
				cache.set = func(context.Context) { cancel() }
			}
			reader, _ := NewCachedCardReader(fixtureSource(), stateReaderFunc(fixtureStates), cache, nil)
			if _, err := reader.BatchGetCards(ctx, []uint{1}); !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
			if len(reader.slots) != 0 {
				t.Fatal("取消泄漏名额")
			}
		})
	}
	source, cache := fixtureSource(), &cardCacheStub{}
	stateCalls := 0
	reader, _ := NewCachedCardReader(source, stateReaderFunc(func(ctx context.Context, ids []uint) (map[uint]domainfeed.FeedPageItem, error) {
		stateCalls++
		return fixtureStates(ctx, ids)
	}), cache, nil)
	ids := make([]uint, 52)
	for i := range ids {
		ids[i] = uint(i + 1)
	}
	if _, err := reader.BatchGetCards(t.Context(), ids); !errors.Is(err, domainfeed.ErrInvalidCardBatch) {
		t.Fatalf("err=%v", err)
	}
	if cards, err := reader.BatchGetCards(t.Context(), []uint{0, 0}); err != nil || cards == nil || len(cards) != 0 {
		t.Fatalf("cards=%v err=%v", cards, err)
	}
	if stateCalls != 0 || cache.gets != 0 {
		t.Fatal("无效或空批次触发 IO")
	}
	for i := 0; i < cap(reader.slots); i++ {
		reader.slots <- struct{}{}
	}
	if cards, err := reader.BatchGetCards(t.Context(), []uint{1}); err != nil || len(cards) != 1 {
		t.Fatalf("cards=%v err=%v", cards, err)
	}
	if cache.gets != 0 || cache.sets != 0 {
		t.Fatal("饱和时访问缓存")
	}
	for len(reader.slots) > 0 {
		<-reader.slots
	}
	if _, err := reader.BatchGetCards(t.Context(), []uint{1}); err != nil || cache.sets != 1 {
		t.Fatalf("err=%v sets=%d", err, cache.sets)
	}
}

type blockingCardCache struct {
	entered chan struct{}
	release <-chan struct{}
}

func (c blockingCardCache) GetCards(ctx context.Context, _ []uint) (CachedCards, error) {
	c.entered <- struct{}{}
	select {
	case <-c.release:
		return CachedCards{}, nil
	case <-ctx.Done():
		return CachedCards{}, ctx.Err()
	}
}
func (c blockingCardCache) SetCards(context.Context, []domainfeed.FeedCard) (CardCacheWrite, error) {
	return CardCacheWrite{}, nil
}
func (c blockingCardCache) DeleteCards(context.Context, []uint) error { return nil }

// 测试目标：并发占满卡片缓存名额并验证与页缓存共享容量
// 预期效果：最多 16 个缓存操作，额外读取回源，取消释放全部名额且卡片开关单独开启时无装配
func TestCachedCardReaderConcurrentCapacityAndWiring(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 32)
	cache := blockingCardCache{entered: entered, release: release}
	service := New(&stubRepository{}, WithPageCache(newFakePageCache(), fixtureSource(), nil), WithCardCache(cache, stateReaderFunc(fixtureStates), nil))
	reader, ok := service.timelineCache.cards.(*CachedCardReader)
	if !ok || reader.slots != service.timelineCache.cacheSlots {
		t.Fatal("卡片与页缓存未共享容量")
	}
	cardOnly := New(&stubRepository{}, WithCardCache(cache, stateReaderFunc(fixtureStates), nil))
	if cardOnly.timelineCache != nil {
		t.Fatal("卡片开关隐式启用页缓存")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := reader.BatchGetCards(ctx, []uint{1}); !errors.Is(err, context.Canceled) {
				t.Errorf("取消 err=%v", err)
			}
		}()
	}
	for i := 0; i < 16; i++ {
		waitSignal(t, entered, "缓存操作未进入")
	}
	if cards, err := reader.BatchGetCards(t.Context(), []uint{2}); err != nil || len(cards) != 1 {
		t.Fatalf("饱和回源 cards=%v err=%v", cards, err)
	}
	select {
	case <-entered:
		t.Fatal("超出缓存容量")
	default:
	}
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	waitSignal(t, done, "取消未释放操作")
	if len(reader.slots) != 0 {
		t.Fatal("名额泄漏")
	}
	close(release)
	if _, err := reader.BatchGetCards(t.Context(), []uint{1}); err != nil {
		t.Fatal(err)
	}
}

// 测试目标：把旧请求实际阻塞在回填期间，并并发删除视频
// 预期效果：允许迟到值有限存活，但下一次读取仍由 MySQL 公开状态拦住且不再访问 Redis
func TestCachedCardReaderDeletionDuringFill(t *testing.T) {
	var visible atomic.Bool
	visible.Store(true)
	entered, release := make(chan struct{}), make(chan struct{})
	states := stateReaderFunc(func(ctx context.Context, ids []uint) (map[uint]domainfeed.FeedPageItem, error) {
		if !visible.Load() {
			return map[uint]domainfeed.FeedPageItem{}, nil
		}
		return fixtureStates(ctx, ids)
	})
	cache := &cardCacheStub{set: func(ctx context.Context) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}}
	reader, _ := NewCachedCardReader(fixtureSource(), states, cache, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := reader.BatchGetCards(t.Context(), []uint{1}); err != nil {
			t.Errorf("旧请求 err=%v", err)
		}
	}()
	waitSignal(t, entered, "旧请求未进入回填")
	visible.Store(false)
	close(release)
	waitSignal(t, done, "迟到写入未完成")
	if len(cache.cards) != 1 {
		t.Fatal("未覆盖真实迟到写入")
	}
	before := cache.gets
	cards, err := reader.BatchGetCards(t.Context(), []uint{1})
	if err != nil || len(cards) != 0 || cache.gets != before {
		t.Fatalf("cards=%v err=%v gets=%d", cards, err, cache.gets)
	}
}

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
