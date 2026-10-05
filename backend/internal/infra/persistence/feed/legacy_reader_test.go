package infrafeed

import (
	"context"
	"errors"
	"os"
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
