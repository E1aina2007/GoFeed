package infrafeed

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	domainfeed "gofeed/internal/domain/feed"
	"gofeed/internal/testutil"
	"gofeed/internal/video"
)

// 测试目标：配置 Feed 持久化适配器测试进程
// 预期效果：运行前创建独立测试库并在全部用例结束后清理
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：固定适配器测试的基准发布时间
// 预期效果：所有用例使用同一时区的可比较时间
var feedBaseTime = time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)

// feedTimePtr 返回时间的指针副本，便于构造发布时刻
func feedTimePtr(value time.Time) *time.Time {
	return &value
}

// 测试目标：构造字段齐全的视频实体
// 预期效果：调用方可指定作者、状态与发布时间，媒体字段满足公开不变量
func newFeedVideoEntity(authorID uint, status string, publishedAt *time.Time) video.Video {
	return video.Video{
		AuthorID:          authorID,
		Title:             "集成标题",
		Description:       "集成描述",
		PlayURL:           "/static/videos/1/a.mp4",
		PlayFileName:      "a.mp4",
		PlayOriginalName:  "原始视频.mp4",
		CoverURL:          "/static/covers/1/a.webp",
		CoverFileName:     "a.webp",
		CoverOriginalName: "原始封面.webp",
		Status:            status,
		PublishedAt:       publishedAt,
	}
}

// 测试目标：构造指定标识的公开完整视频实体
// 预期效果：实体满足 IsPublicVideo 判断，可直接用于适配器映射断言
func newPublicFeedVideoRow(id, authorID uint, publishedAt time.Time) video.Video {
	row := newFeedVideoEntity(authorID, video.VideoStatusPublished, feedTimePtr(publishedAt))
	row.ID = id
	return row
}

// fakePublishedVideoReader 记录时间线读取入参并返回预设结果
type fakePublishedVideoReader struct {
	calls    int
	authorID uint
	cursor   *video.Cursor
	limit    int
	rows     []video.Video
	err      error
}

func (f *fakePublishedVideoReader) GetPublishedVideoList(ctx context.Context, authorID uint, cursor *video.Cursor, limit int) ([]video.Video, error) {
	f.calls++
	f.authorID = authorID
	f.cursor = cursor
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// fakePublicAuthorReader 记录作者批量读取入参并返回预设结果
type fakePublicAuthorReader struct {
	calls int
	ids   []uint
	rows  map[uint]video.Author
	err   error
}

func (f *fakePublicAuthorReader) GetPublicAuthors(ctx context.Context, authorIDs []uint) (map[uint]video.Author, error) {
	f.calls++
	f.ids = append([]uint(nil), authorIDs...)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// fakeEngagementCountsReader 记录统计批量读取入参并返回预设结果
type fakeEngagementCountsReader struct {
	calls int
	ids   []uint
	rows  map[uint]video.EngagementCounts
	err   error
}

func (f *fakeEngagementCountsReader) GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]video.EngagementCounts, error) {
	f.calls++
	f.ids = append([]uint(nil), videoIDs...)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

var (
	_ PublishedVideoReader = (*fakePublishedVideoReader)(nil)
	_ AuthorReader         = (*fakePublicAuthorReader)(nil)
	_ EngagementReader     = (*fakeEngagementCountsReader)(nil)
)

// 测试目标：验证时间线页只收录公开完整视频并映射页条目与卡片
// 预期效果：草稿、缺少发布时间和零值发布时间的行都不进入 Items 与 Cards，卡片键为视频标识
func TestListTimelinePageFiltersAndMapsRows(t *testing.T) {
	published := feedBaseTime
	withoutPublishedAt := newPublicFeedVideoRow(9, 4, feedBaseTime)
	withoutPublishedAt.PublishedAt = nil
	zeroPublishedAt := newPublicFeedVideoRow(10, 4, feedBaseTime)
	zeroPublishedAt.PublishedAt = feedTimePtr(time.Time{})
	reader := &fakePublishedVideoReader{rows: []video.Video{
		newPublicFeedVideoRow(7, 3, published),
		newFeedVideoEntity(3, video.VideoStatusDraft, feedTimePtr(feedBaseTime)),
		withoutPublishedAt,
		zeroPublishedAt,
	}}
	repo := New(reader, nil, nil)

	page, err := repo.ListTimelinePage(context.Background(), nil, 20)
	if err != nil {
		t.Fatalf("读取时间线失败: %v", err)
	}
	if reader.calls != 1 || reader.authorID != 0 || reader.limit != 20 || reader.cursor != nil {
		t.Fatalf("时间线读取入参错误 calls=%d authorID=%d limit=%d cursor=%+v", reader.calls, reader.authorID, reader.limit, reader.cursor)
	}
	if len(page.Items) != 1 {
		t.Fatalf("页条目应只收录公开视频 got=%+v", page.Items)
	}
	item := page.Items[0]
	if item.VideoID != 7 || item.AuthorID != 3 || !item.PublishedAt.Equal(published) {
		t.Fatalf("页条目映射错误 got=%+v", item)
	}
	if len(page.Cards) != 1 {
		t.Fatalf("卡片应只收录公开视频 got=%d", len(page.Cards))
	}
	card, ok := page.Cards[7]
	if !ok {
		t.Fatalf("卡片键应为视频标识 got=%+v", page.Cards)
	}
	if card.VideoID != 7 || card.AuthorID != 3 || card.Title != "集成标题" || !card.PublishedAt.Equal(published) {
		t.Fatalf("卡片映射错误 got=%+v", card)
	}
}

// 测试目标：验证时间线空结果返回可用的空页
// 预期效果：Items 与 Cards 均为非 nil 空集合
func TestListTimelinePageEmptyRows(t *testing.T) {
	page, err := New(&fakePublishedVideoReader{}, nil, nil).ListTimelinePage(context.Background(), nil, 20)
	if err != nil {
		t.Fatalf("空结果读取时间线失败: %v", err)
	}
	if page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("Items 应为非 nil 空切片 got=%v", page.Items)
	}
	if page.Cards == nil || len(page.Cards) != 0 {
		t.Fatalf("Cards 应为非 nil 空映射 got=%v", page.Cards)
	}
}

// 测试目标：验证时间线游标转换为视频读取位置
// 预期效果：发布时间与视频标识逐字段对齐，空游标透传为空且限制值原样传递
func TestListTimelinePageConvertsCursor(t *testing.T) {
	reader := &fakePublishedVideoReader{}
	repo := New(reader, nil, nil)
	at := feedBaseTime.Add(2 * time.Hour)

	if _, err := repo.ListTimelinePage(context.Background(), &domainfeed.TimelineCursor{PublishedAt: at, VideoID: 42}, 7); err != nil {
		t.Fatalf("带游标读取时间线失败: %v", err)
	}
	if reader.cursor == nil {
		t.Fatal("游标应转换为视频读取位置")
	}
	if !reader.cursor.PublishedAt.Equal(at) || reader.cursor.ID != 42 {
		t.Fatalf("游标字段未对齐 got=%+v", *reader.cursor)
	}
	if reader.limit != 7 {
		t.Fatalf("分页限制应透传 got=%d", reader.limit)
	}

	if _, err := repo.ListTimelinePage(context.Background(), nil, 5); err != nil {
		t.Fatalf("无游标读取时间线失败: %v", err)
	}
	if reader.cursor != nil {
		t.Fatalf("空游标应透传为空 got=%+v", *reader.cursor)
	}
}

// 测试目标：验证时间线读取在依赖缺失或底层失败时返回不可用
// 预期效果：nil 读取器与底层错误都满足 ErrUnavailable 且保留原始 cause
func TestListTimelinePageUnavailable(t *testing.T) {
	ctx := context.Background()

	if _, err := New(nil, nil, nil).ListTimelinePage(ctx, nil, 20); !errors.Is(err, domainfeed.ErrUnavailable) {
		t.Fatalf("nil 读取器应返回 ErrUnavailable, err=%v", err)
	}

	cause := errors.New("时间线读取失败")
	_, err := New(&fakePublishedVideoReader{err: cause}, nil, nil).ListTimelinePage(ctx, nil, 20)
	if !errors.Is(err, domainfeed.ErrUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("底层错误应包装为 ErrUnavailable 且保留 cause, err=%v", err)
	}
}

// 测试目标：验证作者与统计批量映射的键和字段
// 预期效果：结果以标识为键，各字段逐项对应且入参原样传递
func TestBatchGetAuthorAndStatsMapping(t *testing.T) {
	ctx := context.Background()
	authors := &fakePublicAuthorReader{rows: map[uint]video.Author{
		5: {ID: 5, Username: "作者五", AvatarURL: "/static/avatars/5.png"},
	}}
	stats := &fakeEngagementCountsReader{rows: map[uint]video.EngagementCounts{
		9: {LikesCount: 3, CommentsCount: 4},
	}}
	repo := New(nil, authors, stats)

	gotAuthors, err := repo.BatchGetAuthors(ctx, []uint{5, 6})
	if err != nil {
		t.Fatalf("批量读取作者失败: %v", err)
	}
	if len(gotAuthors) != 1 {
		t.Fatalf("作者结果数量错误 got=%+v", gotAuthors)
	}
	author, ok := gotAuthors[5]
	if !ok {
		t.Fatalf("作者结果应以标识为键 got=%+v", gotAuthors)
	}
	if author.ID != 5 || author.Username != "作者五" || author.AvatarURL != "/static/avatars/5.png" {
		t.Fatalf("作者字段映射错误 got=%+v", author)
	}
	if authors.calls != 1 || !slices.Equal(authors.ids, []uint{5, 6}) {
		t.Fatalf("作者读取入参错误 calls=%d ids=%v", authors.calls, authors.ids)
	}

	gotStats, err := repo.BatchGetStats(ctx, []uint{9})
	if err != nil {
		t.Fatalf("批量读取统计失败: %v", err)
	}
	stat, ok := gotStats[9]
	if !ok {
		t.Fatalf("统计结果应以标识为键 got=%+v", gotStats)
	}
	if stat.LikesCount != 3 || stat.CommentsCount != 4 {
		t.Fatalf("统计字段映射错误 got=%+v", stat)
	}
	if stats.calls != 1 || !slices.Equal(stats.ids, []uint{9}) {
		t.Fatalf("统计读取入参错误 calls=%d ids=%v", stats.calls, stats.ids)
	}
}

// 测试目标：验证作者与统计的空批量读取不访问底层依赖
// 预期效果：空入参返回非 nil 空 map 且不产生任何读取调用，nil 依赖也不报错
func TestBatchReadsSkipEmptyInput(t *testing.T) {
	ctx := context.Background()
	authors := &fakePublicAuthorReader{}
	stats := &fakeEngagementCountsReader{}
	repo := New(nil, authors, stats)

	for _, ids := range [][]uint{nil, {}} {
		gotAuthors, err := repo.BatchGetAuthors(ctx, ids)
		if err != nil || gotAuthors == nil || len(gotAuthors) != 0 {
			t.Fatalf("空作者批量应返回非 nil 空 map, got=%v err=%v", gotAuthors, err)
		}
		gotStats, err := repo.BatchGetStats(ctx, ids)
		if err != nil || gotStats == nil || len(gotStats) != 0 {
			t.Fatalf("空统计批量应返回非 nil 空 map, got=%v err=%v", gotStats, err)
		}
	}
	if authors.calls != 0 || stats.calls != 0 {
		t.Fatalf("空批量不应访问底层依赖 authors=%d stats=%d", authors.calls, stats.calls)
	}

	bare := New(nil, nil, nil)
	if got, err := bare.BatchGetAuthors(ctx, nil); err != nil || got == nil {
		t.Fatalf("nil 依赖下的空作者批量应返回非 nil 空 map, got=%v err=%v", got, err)
	}
	if got, err := bare.BatchGetStats(ctx, nil); err != nil || got == nil {
		t.Fatalf("nil 依赖下的空统计批量应返回非 nil 空 map, got=%v err=%v", got, err)
	}
}

// 测试目标：验证作者与统计读取失败的不可用语义
// 预期效果：nil 读取器与底层错误都返回 ErrUnavailable 且保留原始 cause
func TestBatchReadsUnavailable(t *testing.T) {
	ctx := context.Background()

	if _, err := New(nil, nil, nil).BatchGetAuthors(ctx, []uint{1}); !errors.Is(err, domainfeed.ErrUnavailable) {
		t.Fatalf("nil 作者读取器应返回 ErrUnavailable, err=%v", err)
	}
	if _, err := New(nil, nil, nil).BatchGetStats(ctx, []uint{1}); !errors.Is(err, domainfeed.ErrUnavailable) {
		t.Fatalf("nil 统计读取器应返回 ErrUnavailable, err=%v", err)
	}

	authorCause := errors.New("作者读取失败")
	_, err := New(nil, &fakePublicAuthorReader{err: authorCause}, nil).BatchGetAuthors(ctx, []uint{1})
	if !errors.Is(err, domainfeed.ErrUnavailable) || !errors.Is(err, authorCause) {
		t.Fatalf("作者错误应包装为 ErrUnavailable 且保留 cause, err=%v", err)
	}

	statsCause := errors.New("统计读取失败")
	_, err = New(nil, nil, &fakeEngagementCountsReader{err: statsCause}).BatchGetStats(ctx, []uint{1})
	if !errors.Is(err, domainfeed.ErrUnavailable) || !errors.Is(err, statsCause) {
		t.Fatalf("统计错误应包装为 ErrUnavailable 且保留 cause, err=%v", err)
	}
}
