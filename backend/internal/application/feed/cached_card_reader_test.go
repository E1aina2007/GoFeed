package applicationfeed

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

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

// 测试目标：验证冷读、全部命中和部分缺失的批量回源
// 预期效果：忽略零与重复标识，仅回源缺失卡片，允许作者零标识并保留命中观测
func TestCachedCardReaderColdHitAndPartial(t *testing.T) {
	source, cache := fixtureSource(), &cardCacheStub{}
	events := make(map[string]int)
	reader, err := NewCachedCardReader(source, stateReaderFunc(fixtureStates), cache, func(e CardCacheObservation) { events[e.Result] += e.Count })
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		cards, err := reader.BatchGetCards(t.Context(), []uint{0, 2, 1, 2})
		if err != nil || len(cards) != 2 {
			t.Fatalf("cards=%v err=%v", cards, err)
		}
	}
	if source.calls != 1 || cache.sets != 1 || events["hit"] != 2 || !reflect.DeepEqual(cache.requested, []uint{2, 1}) {
		t.Fatalf("source=%d cache=%+v events=%v", source.calls, cache, events)
	}
	delete(cache.cards, 1)
	if _, err := reader.BatchGetCards(t.Context(), []uint{2, 1}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(source.ids, []uint{1}) || events["hit"] != 3 {
		t.Fatalf("ids=%v events=%v", source.ids, events)
	}
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
