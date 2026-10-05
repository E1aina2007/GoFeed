package applicationfeed

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

// buildPage 构造 count 条页条目及一一对应的卡片
func buildPage(count int, authorOf func(index int) uint) domainfeed.TimelinePage {
	page := domainfeed.TimelinePage{
		Items: make([]domainfeed.FeedPageItem, 0, count),
		Cards: make(map[uint]domainfeed.FeedCard, count),
	}
	for index := 0; index < count; index++ {
		item := pageItemAt(index, authorOf(index))
		page.Items = append(page.Items, item)
		page.Cards[item.VideoID] = cardOf(item)
	}
	return page
}

func singleAuthor(int) uint { return 7 }

// 测试目标：读模型不一致时拒绝组装而不是输出错误数据
// 预期效果：缺失卡片、零视频 ID、零发布时间、字段不一致与缺失作者都返回 ErrInvalidReadResult
func TestGetFeedRejectsInconsistentReadModel(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(page *domainfeed.TimelinePage, repo *stubRepository)
	}{
		{"缺少卡片", func(page *domainfeed.TimelinePage, _ *stubRepository) {
			delete(page.Cards, page.Items[1].VideoID)
		}},
		{"条目视频 ID 为零", func(page *domainfeed.TimelinePage, _ *stubRepository) {
			page.Items[0].VideoID = 0
		}},
		{"条目发布时间为零", func(page *domainfeed.TimelinePage, _ *stubRepository) {
			page.Items[0].PublishedAt = time.Time{}
		}},
		{"卡片视频 ID 不一致", func(page *domainfeed.TimelinePage, _ *stubRepository) {
			card := page.Cards[page.Items[0].VideoID]
			card.VideoID++
			page.Cards[page.Items[0].VideoID] = card
		}},
		{"卡片作者 ID 不一致", func(page *domainfeed.TimelinePage, _ *stubRepository) {
			card := page.Cards[page.Items[0].VideoID]
			card.AuthorID++
			page.Cards[page.Items[0].VideoID] = card
		}},
		{"卡片发布时间不一致", func(page *domainfeed.TimelinePage, _ *stubRepository) {
			card := page.Cards[page.Items[0].VideoID]
			card.PublishedAt = card.PublishedAt.Add(time.Second)
			page.Cards[page.Items[0].VideoID] = card
		}},
		{"缺少作者", func(_ *domainfeed.TimelinePage, repo *stubRepository) {
			repo.authorFn = func([]uint) (map[uint]domainfeed.Author, error) {
				return map[uint]domainfeed.Author{}, nil
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepository{}
			page := buildPage(3, singleAuthor)
			repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
				return page, nil
			}
			tc.mutate(&page, repo)

			_, err := New(repo).GetFeed(context.Background(), FeedRequest{Limit: 2})
			if !errors.Is(err, domainfeed.ErrInvalidReadResult) {
				t.Fatalf("got error=%v want=%v", err, domainfeed.ErrInvalidReadResult)
			}
		})
	}
}

type followingReadFunc func(context.Context, uint, *domainfeed.FollowingCursor, int) (domainfeed.TimelinePage, error)

func (f followingReadFunc) ListFollowingPage(ctx context.Context, viewer uint, cursor *domainfeed.FollowingCursor, limit int) (domainfeed.TimelinePage, error) {
	return f(ctx, viewer, cursor, limit)
}

// 测试目标：关注流截断探测行后批量组装，并绕过耗尽的 Timeline 缓存容量
// 预期效果：仅最终两条参与作者与统计读取，游标绑定观看者且续页支持改变 limit
func TestFollowingFeedTruncatesAndBypassesTimelineCache(t *testing.T) {
	repo := &stubRepository{}
	page := buildPage(3, func(i int) uint { return uint(10 + i) })
	var seen *domainfeed.FollowingCursor
	reader := followingReadFunc(func(_ context.Context, viewer uint, cursor *domainfeed.FollowingCursor, limit int) (domainfeed.TimelinePage, error) {
		if viewer != 42 || limit < 2 || limit > 3 {
			t.Fatalf("读取参数 viewer=%d limit=%d", viewer, limit)
		}
		seen = cursor
		if cursor != nil {
			return domainfeed.TimelinePage{}, nil
		}
		return page, nil
	})
	s := New(repo, WithFollowingReader(reader))
	s.timelineCache = &timelineCache{readSlots: make(chan struct{}, 1), cacheSlots: make(chan struct{}, 1)}
	s.timelineCache.readSlots <- struct{}{}
	s.timelineCache.cacheSlots <- struct{}{}
	result, err := s.GetFeed(t.Context(), FeedRequest{Scene: domainfeed.SceneFollowing, ViewerID: 42, Limit: 2})
	if err != nil || len(result.Items) != 2 || result.NextCursor == "" {
		t.Fatalf("关注页 result=%+v err=%v", result, err)
	}
	if repo.listCallCount() != 0 || !reflect.DeepEqual(repo.statIDList(), []uint{900, 899}) || !reflect.DeepEqual(repo.authorIDList(), []uint{10, 11}) {
		t.Fatalf("探测行不得组装 timeline=%d stats=%v authors=%v", repo.listCallCount(), repo.statIDList(), repo.authorIDList())
	}
	last := page.Items[1]
	position, err := decodeFollowingCursor(result.NextCursor, 42)
	if err != nil || position.VideoID != last.VideoID || !position.PublishedAt.Equal(last.PublishedAt) {
		t.Fatalf("游标位置=%+v err=%v", position, err)
	}
	if _, err := decodeFollowingCursor(result.NextCursor, 43); !errors.Is(err, domainfeed.ErrInvalidCursor) {
		t.Fatalf("跨观看者游标=%v", err)
	}
	if _, err := decodeTimelineCursor(result.NextCursor); !errors.Is(err, domainfeed.ErrInvalidCursor) {
		t.Fatalf("跨场景游标=%v", err)
	}
	next, err := s.GetFeed(t.Context(), FeedRequest{Scene: domainfeed.SceneFollowing, ViewerID: 42, Limit: 1, Cursor: result.NextCursor})
	if err != nil || seen == nil || seen.VideoID != last.VideoID || next.Items == nil || len(next.Items) != 0 || next.NextCursor != "" {
		t.Fatalf("末页=%+v cursor=%+v err=%v", next, seen, err)
	}
	if len(s.timelineCache.readSlots) != 1 || len(s.timelineCache.cacheSlots) != 1 {
		t.Fatal("关注流不应取得或释放 Timeline 容量")
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

// 测试目标：验证公开检查、状态变更和迟到回填不能复活已删除视频
// 预期效果：缓存只接收当前可见标识，旧作者或发布时间数据回源，不可见时不访问 Redis
func TestCachedCardReaderVisibilityAndLateWrite(t *testing.T) {
	visible := true
	states := stateReaderFunc(func(ctx context.Context, ids []uint) (map[uint]domainfeed.FeedPageItem, error) {
		if !visible {
			return map[uint]domainfeed.FeedPageItem{}, nil
		}
		return fixtureStates(ctx, ids[:1])
	})
	source, cache := fixtureSource(), &cardCacheStub{cards: map[uint]domainfeed.FeedCard{1: cachedFixture(1), 2: cachedFixture(2)}}
	reader, _ := NewCachedCardReader(source, states, cache, nil)
	for _, field := range []string{"author", "time", "media"} {
		card := cachedFixture(1)
		switch field {
		case "author":
			card.AuthorID = 5
		case "time":
			card.PublishedAt = card.PublishedAt.Add(time.Second)
		case "media":
			card.CoverOriginalName = ""
		}
		cache.cards[1] = card
		cards, err := reader.BatchGetCards(t.Context(), []uint{1, 2})
		if err != nil || len(cards) != 1 || !reflect.DeepEqual(source.ids, []uint{1}) || !reflect.DeepEqual(cache.requested, []uint{1}) {
			t.Fatalf("%s cards=%v err=%v", field, cards, err)
		}
	}
	visible = false
	cache.cards[1] = cachedFixture(1) // 模拟删除之后旧请求迟到写入
	before := cache.gets
	cards, err := reader.BatchGetCards(t.Context(), []uint{1})
	if err != nil || len(cards) != 0 || cache.gets != before {
		t.Fatalf("deleted cards=%v err=%v gets=%d", cards, err, cache.gets)
	}
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
