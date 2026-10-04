package applicationfeed

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
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

// 测试目标：缺省场景与缺省页大小按 Timeline 和 20 解析并向仓储多取一条探测记录
// 预期效果：仓储收到 limit=21，响应恰好 20 条并给出下一页游标
func TestGetFeedAppliesDefaultSceneAndLimit(t *testing.T) {
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
		return buildPage(limit, singleAuthor), nil
	}

	result, err := New(repo).GetFeed(context.Background(), FeedRequest{})
	if err != nil {
		t.Fatalf("缺省请求失败: %v", err)
	}
	if repo.listCallCount() != 1 || repo.observedLimit() != defaultFeedLimit+1 {
		t.Fatalf("仓储调用 got calls=%d limit=%d want calls=1 limit=%d", repo.listCallCount(), repo.observedLimit(), defaultFeedLimit+1)
	}
	if repo.observedCursor() != nil {
		t.Fatalf("首屏不应携带游标 got=%+v", repo.observedCursor())
	}
	if len(result.Items) != defaultFeedLimit {
		t.Fatalf("响应条数 got=%d want=%d", len(result.Items), defaultFeedLimit)
	}
	if result.NextCursor == "" {
		t.Fatal("存在探测记录时应给出下一页游标")
	}
	if repo.statIDList()[0] != 900 || len(repo.statIDList()) != defaultFeedLimit {
		t.Fatalf("统计批次应只覆盖最终响应页 got=%v", repo.statIDList())
	}
}

// 测试目标：页大小边界按 1 与 50 放行并始终多取一条探测记录
// 预期效果：合法边界向仓储请求 limit+1，越界返回 ErrInvalidLimit 且不访问仓储
func TestGetFeedLimitBoundaries(t *testing.T) {
	valid := []struct {
		name      string
		limit     int
		wantLimit int
	}{
		{"最小页大小", 1, 2},
		{"最大页大小", domainfeed.MaxLimit, domainfeed.MaxLimit + 1},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepository{}
			if _, err := New(repo).GetFeed(context.Background(), FeedRequest{Limit: tc.limit}); err != nil {
				t.Fatalf("合法页大小 got error=%v", err)
			}
			if got := repo.observedLimit(); got != tc.wantLimit {
				t.Fatalf("仓储探测条数 got=%d want=%d", got, tc.wantLimit)
			}
		})
	}

	invalid := []int{-1, domainfeed.MaxLimit + 1, 1000}
	for _, limit := range invalid {
		repo := &stubRepository{}
		if _, err := New(repo).GetFeed(context.Background(), FeedRequest{Limit: limit}); !errors.Is(err, domainfeed.ErrInvalidLimit) {
			t.Fatalf("页大小 %d got error=%v want=%v", limit, err, domainfeed.ErrInvalidLimit)
		}
		if repo.listCallCount() != 0 {
			t.Fatalf("非法页大小 %d 不应访问仓储 got calls=%d", limit, repo.listCallCount())
		}
	}
}

// 测试目标：场景契约区分未知场景与已定义但未启用的场景
// 预期效果：未知场景返回 ErrInvalidScene，未启用场景返回 ErrSceneNotEnabled 且都不查询仓储
func TestGetFeedSceneContract(t *testing.T) {
	enabled := []struct {
		name  string
		scene domainfeed.Scene
	}{
		{"显式 Timeline", domainfeed.SceneTimeline},
	}
	for _, tc := range enabled {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepository{}
			if _, err := New(repo).GetFeed(context.Background(), FeedRequest{Scene: tc.scene}); err != nil {
				t.Fatalf("已启用场景失败: %v", err)
			}
			if repo.listCallCount() != 1 {
				t.Fatalf("已启用场景应查询仓储 got calls=%d", repo.listCallCount())
			}
		})
	}

	disabled := []domainfeed.Scene{domainfeed.SceneHot, domainfeed.SceneRecommend}
	for _, scene := range disabled {
		repo := &stubRepository{}
		if _, err := New(repo).GetFeed(context.Background(), FeedRequest{Scene: scene}); !errors.Is(err, domainfeed.ErrSceneNotEnabled) {
			t.Fatalf("未启用场景 %s got error=%v want=%v", scene, err, domainfeed.ErrSceneNotEnabled)
		}
		if repo.listCallCount() != 0 {
			t.Fatalf("未启用场景 %s 不应查询仓储 got calls=%d", scene, repo.listCallCount())
		}
	}

	unknown := []domainfeed.Scene{"unknown", "timeline2", "TIMELINE"}
	for _, scene := range unknown {
		repo := &stubRepository{}
		if _, err := New(repo).GetFeed(context.Background(), FeedRequest{Scene: scene}); !errors.Is(err, domainfeed.ErrInvalidScene) {
			t.Fatalf("未知场景 %s got error=%v want=%v", scene, err, domainfeed.ErrInvalidScene)
		}
		if repo.listCallCount() != 0 {
			t.Fatalf("未知场景 %s 不应查询仓储 got calls=%d", scene, repo.listCallCount())
		}
	}
}

// 测试目标：下一页游标由截断后最后一条生成并可无损往返
// 预期效果：第二轮请求向仓储传入与首轮末条一致的发布时间与视频 ID
func TestGetFeedCursorRoundTrip(t *testing.T) {
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
		return buildPage(limit, singleAuthor), nil
	}
	service := New(repo)

	first, err := service.GetFeed(context.Background(), FeedRequest{Limit: 2})
	if err != nil {
		t.Fatalf("首屏失败: %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("首屏存在探测记录时应给出游标")
	}

	second, err := service.GetFeed(context.Background(), FeedRequest{Cursor: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("第二页失败: %v", err)
	}
	cursor := repo.observedCursor()
	want := pageItemAt(1, 7)
	if cursor == nil || !cursor.PublishedAt.Equal(want.PublishedAt) || cursor.VideoID != want.VideoID {
		t.Fatalf("游标位置 got=%+v want=%+v", cursor, want)
	}
	if second.NextCursor == "" {
		t.Fatal("第二页仍存在探测记录时应继续给出游标")
	}
}

// 测试目标：分页尾部在恰好整页、不足一页与空页时都不产生下一页游标
// 预期效果：只有超出页大小一条探测记录时才生成游标，空结果输出非 nil 切片
func TestGetFeedPaginationTail(t *testing.T) {
	cases := []struct {
		name         string
		returned     int
		wantItems    int
		wantNextPage bool
	}{
		{"超出页大小一条探测记录", 4, 3, true},
		{"恰好整页", 3, 3, false},
		{"不足一页", 2, 2, false},
		{"空页", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepository{}
			repo.listFn = func(_ *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
				return buildPage(tc.returned, singleAuthor), nil
			}
			result, err := New(repo).GetFeed(context.Background(), FeedRequest{Limit: 3})
			if err != nil {
				t.Fatalf("分页失败: %v", err)
			}
			if result.Items == nil {
				t.Fatal("响应条目必须是非 nil 切片以保证 items: [] 输出")
			}
			if len(result.Items) != tc.wantItems {
				t.Fatalf("响应条数 got=%d want=%d", len(result.Items), tc.wantItems)
			}
			if (result.NextCursor != "") != tc.wantNextPage {
				t.Fatalf("下一页游标 got=%q want=%v", result.NextCursor, tc.wantNextPage)
			}
		})
	}
}

// 测试目标：探测记录只参与分页判断，不进入作者与统计批次
// 预期效果：批量查询只覆盖最终响应页的视频与作者
func TestGetFeedProbeRecordExcludedFromBatches(t *testing.T) {
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
		return buildPage(limit, func(index int) uint { return uint(10 + index) }), nil
	}

	result, err := New(repo).GetFeed(context.Background(), FeedRequest{Limit: 2})
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("响应条数 got=%d want=2", len(result.Items))
	}
	wantVideos := []uint{900, 899}
	if got := repo.statIDList(); !equalIDs(got, wantVideos) {
		t.Fatalf("统计批次 got=%v want=%v", got, wantVideos)
	}
	wantAuthors := []uint{10, 11}
	if got := repo.authorIDList(); !equalIDs(got, wantAuthors) {
		t.Fatalf("作者批次 got=%v want=%v", got, wantAuthors)
	}
}

// 测试目标：同页共享作者只查询一次，视频 ID 按出现顺序去重
// 预期效果：批量作者查询收到去重后的作者集合
func TestGetFeedDeduplicatesBatchIDs(t *testing.T) {
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
		return buildPage(limit, func(index int) uint {
			if index < 2 {
				return 42
			}
			return 43
		}), nil
	}

	if _, err := New(repo).GetFeed(context.Background(), FeedRequest{Limit: 3}); err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if got := repo.authorIDList(); !equalIDs(got, []uint{42, 43}) {
		t.Fatalf("作者批次 got=%v want=[42 43]", got)
	}
	if got := repo.statIDList(); !equalIDs(got, []uint{900, 899, 898}) {
		t.Fatalf("统计批次 got=%v want=[900 899 898]", got)
	}
}

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

// 测试目标：缺少互动统计按零值处理而不是判定读模型损坏
// 预期效果：统计缺失时仍返回条目且计数为零
func TestGetFeedTreatsMissingStatAsZero(t *testing.T) {
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
		return buildPage(limit, singleAuthor), nil
	}
	repo.statFn = func([]uint) (map[uint]domainfeed.FeedStat, error) {
		return map[uint]domainfeed.FeedStat{}, nil
	}

	result, err := New(repo).GetFeed(context.Background(), FeedRequest{Limit: 1})
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("响应条数 got=%d want=1", len(result.Items))
	}
	if result.Items[0].Stat.LikesCount != 0 || result.Items[0].Stat.CommentsCount != 0 {
		t.Fatalf("缺失统计应按零值处理 got=%+v", result.Items[0].Stat)
	}
}

// 测试目标：仓储错误原样向上返回且保留原因
// 预期效果：分页、统计与作者三处失败都能被 errors.Is 追溯
func TestGetFeedPropagatesRepositoryErrors(t *testing.T) {
	databaseError := errors.New("database is down")

	t.Run("分页读取失败", func(t *testing.T) {
		repo := &stubRepository{}
		repo.listFn = func(*domainfeed.TimelineCursor, int) (domainfeed.TimelinePage, error) {
			return domainfeed.TimelinePage{}, fmt.Errorf("读取时间线失败: %w", databaseError)
		}
		_, err := New(repo).GetFeed(context.Background(), FeedRequest{})
		if !errors.Is(err, databaseError) {
			t.Fatalf("原因未保留 got error=%v", err)
		}
		if repo.statIDList() != nil {
			t.Fatalf("分页失败后不应查询统计 got=%v", repo.statIDList())
		}
	})

	t.Run("统计读取失败", func(t *testing.T) {
		repo := &stubRepository{}
		repo.listFn = func(_ *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
			return buildPage(limit, singleAuthor), nil
		}
		repo.statFn = func([]uint) (map[uint]domainfeed.FeedStat, error) {
			return nil, fmt.Errorf("统计失败: %w", databaseError)
		}
		_, err := New(repo).GetFeed(context.Background(), FeedRequest{Limit: 1})
		if !errors.Is(err, databaseError) {
			t.Fatalf("原因未保留 got error=%v", err)
		}
		if repo.authorIDList() != nil {
			t.Fatalf("统计失败后不应继续查询作者 got=%v", repo.authorIDList())
		}
	})

	t.Run("作者读取失败", func(t *testing.T) {
		repo := &stubRepository{}
		repo.listFn = func(_ *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
			return buildPage(limit, singleAuthor), nil
		}
		repo.authorFn = func([]uint) (map[uint]domainfeed.Author, error) {
			return nil, fmt.Errorf("作者失败: %w", databaseError)
		}
		_, err := New(repo).GetFeed(context.Background(), FeedRequest{Limit: 1})
		if !errors.Is(err, databaseError) {
			t.Fatalf("原因未保留 got error=%v", err)
		}
	})
}

// 测试目标：缺少仓储依赖时返回不可用而不是空结果
// 预期效果：nil 仓储请求返回 ErrUnavailable
func TestGetFeedWithoutRepository(t *testing.T) {
	_, err := New(nil).GetFeed(context.Background(), FeedRequest{})
	if !errors.Is(err, domainfeed.ErrUnavailable) {
		t.Fatalf("got error=%v want=%v", err, domainfeed.ErrUnavailable)
	}
}

// 测试目标：旧 /api/video 游标不能在新时间线上复用
// 预期效果：旧格式游标与非游标负载都返回 ErrInvalidCursor 且不查询仓储
func TestGetFeedRejectsLegacyVideoCursor(t *testing.T) {
	legacy := base64.RawURLEncoding.EncodeToString([]byte(
		`{"v":1,"k":"published","a":0,"p":"2026-05-04T03:02:01Z","i":899}`,
	))
	others := []struct {
		name   string
		cursor string
	}{
		{"旧视频游标", legacy},
		{"非 base64 载荷", "not-a-cursor"},
		{"空 JSON 载荷", base64.RawURLEncoding.EncodeToString([]byte(`{}`))},
	}
	for _, tc := range others {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepository{}
			_, err := New(repo).GetFeed(context.Background(), FeedRequest{Cursor: tc.cursor})
			if !errors.Is(err, domainfeed.ErrInvalidCursor) {
				t.Fatalf("got error=%v want=%v", err, domainfeed.ErrInvalidCursor)
			}
			if repo.listCallCount() != 0 {
				t.Fatalf("非法游标不应查询仓储 got calls=%d", repo.listCallCount())
			}
		})
	}
}

// 测试目标：响应卡片与作者、统计按条目一一对应
// 预期效果：展示字段来自卡片，作者与计数来自批量读模型
func TestGetFeedAssemblesItemFields(t *testing.T) {
	repo := &stubRepository{}
	repo.listFn = func(_ *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
		return buildPage(limit, singleAuthor), nil
	}

	result, err := New(repo).GetFeed(context.Background(), FeedRequest{Limit: 1})
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	wantCard := cardOf(pageItemAt(0, 7))
	if len(result.Items) != 1 || result.Items[0].Card != wantCard {
		t.Fatalf("卡片字段不匹配 got=%+v want=%+v", result.Items[0].Card, wantCard)
	}
	if result.Items[0].Author.Username != "作者7" {
		t.Fatalf("作者字段不匹配 got=%+v", result.Items[0].Author)
	}
	if result.Items[0].Stat.LikesCount != 1 || result.Items[0].Stat.CommentsCount != 2 {
		t.Fatalf("统计字段不匹配 got=%+v", result.Items[0].Stat)
	}
}

func equalIDs(got, want []uint) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
