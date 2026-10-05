package infrafeed

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	dbpkg "gofeed/internal/db"
	domainfeed "gofeed/internal/domain/feed"
	"gofeed/internal/testutil"
	"gofeed/internal/video"
)

// 测试目标：通过真实仓储写入视频实体
// 预期效果：写入失败时终止用例并回填自增标识
func persistFeedVideo(t *testing.T, repo *video.Repository, row *video.Video) {
	t.Helper()
	if err := repo.Create(context.Background(), row); err != nil {
		t.Fatalf("写入视频 %q 失败: %v", row.Title, err)
	}
}

// 测试目标：验证真实库上卡片批量读取的公开可见性过滤
// 预期效果：非公开状态、软删除、媒体不完整、发布时间为空和不存在的行都不返回，作者标识为零的公开行仍返回
func TestBatchGetCardsRealRepositoryFiltersInvisible(t *testing.T) {
	gdb := testutil.DB(t)
	repo := video.NewRepository(gdb)
	ctx := context.Background()

	public := newFeedVideoEntity(1, video.VideoStatusPublished, feedTimePtr(feedBaseTime))
	public.Title = "公开视频"
	persistFeedVideo(t, repo, &public)
	authorless := newFeedVideoEntity(0, video.VideoStatusPublished, feedTimePtr(feedBaseTime.Add(time.Minute)))
	persistFeedVideo(t, repo, &authorless)

	invisible := []struct {
		name        string
		status      string
		publishedAt *time.Time
		mutate      func(*video.Video)
		deleted     bool
	}{
		{name: "草稿状态", status: video.VideoStatusDraft, publishedAt: feedTimePtr(feedBaseTime)},
		{name: "处理中状态", status: video.VideoStatusProcessing, publishedAt: feedTimePtr(feedBaseTime)},
		{name: "已拒绝状态", status: video.VideoStatusRejected, publishedAt: feedTimePtr(feedBaseTime)},
		{name: "清扫中状态", status: video.VideoStatusPurging, publishedAt: feedTimePtr(feedBaseTime)},
		{name: "发布时间为空", status: video.VideoStatusPublished, publishedAt: nil},
		{name: "缺少播放地址", status: video.VideoStatusPublished, publishedAt: feedTimePtr(feedBaseTime), mutate: func(row *video.Video) { row.PlayURL = "" }},
		{name: "缺少播放文件名", status: video.VideoStatusPublished, publishedAt: feedTimePtr(feedBaseTime), mutate: func(row *video.Video) { row.PlayFileName = "" }},
		{name: "缺少播放原始名", status: video.VideoStatusPublished, publishedAt: feedTimePtr(feedBaseTime), mutate: func(row *video.Video) { row.PlayOriginalName = "" }},
		{name: "缺少封面地址", status: video.VideoStatusPublished, publishedAt: feedTimePtr(feedBaseTime), mutate: func(row *video.Video) { row.CoverURL = "" }},
		{name: "缺少封面文件名", status: video.VideoStatusPublished, publishedAt: feedTimePtr(feedBaseTime), mutate: func(row *video.Video) { row.CoverFileName = "" }},
		{name: "缺少封面原始名", status: video.VideoStatusPublished, publishedAt: feedTimePtr(feedBaseTime), mutate: func(row *video.Video) { row.CoverOriginalName = "" }},
		{name: "软删除", status: video.VideoStatusPublished, publishedAt: feedTimePtr(feedBaseTime), deleted: true},
	}

	ids := []uint{public.ID, authorless.ID}
	invisibleIDs := make([]uint, 0, len(invisible))
	for _, item := range invisible {
		row := newFeedVideoEntity(1, item.status, item.publishedAt)
		if item.mutate != nil {
			item.mutate(&row)
		}
		persistFeedVideo(t, repo, &row)
		if item.deleted {
			if err := gdb.Delete(&video.Video{}, row.ID).Error; err != nil {
				t.Fatalf("软删除 %s 失败: %v", item.name, err)
			}
		}
		ids = append(ids, row.ID)
		invisibleIDs = append(invisibleIDs, row.ID)
	}
	ids = append(ids, 999999)

	cards, err := NewCardReader(repo).BatchGetCards(ctx, ids)
	if err != nil {
		t.Fatalf("真实库批量读取卡片失败: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("结果应只包含两条公开完整视频 got=%d", len(cards))
	}
	for _, id := range invisibleIDs {
		if _, ok := cards[id]; ok {
			t.Fatalf("标识 %d 不应出现在结果中", id)
		}
	}
	card, ok := cards[public.ID]
	if !ok {
		t.Fatalf("公开视频应被收录 got=%+v", cards)
	}
	if card.VideoID != public.ID || card.AuthorID != public.AuthorID || card.Title != public.Title {
		t.Fatalf("卡片基础字段与实体不一致 got=%+v", card)
	}
	if card.PlayURL != public.PlayURL || card.PlayFileName != public.PlayFileName || card.PlayOriginalName != public.PlayOriginalName {
		t.Fatalf("卡片播放媒体字段与实体不一致 got=%+v", card)
	}
	if card.CoverURL != public.CoverURL || card.CoverFileName != public.CoverFileName || card.CoverOriginalName != public.CoverOriginalName {
		t.Fatalf("卡片封面媒体字段与实体不一致 got=%+v", card)
	}
	if !card.PublishedAt.Equal(*public.PublishedAt) {
		t.Fatalf("卡片发布时间与实体不一致 got=%v want=%v", card.PublishedAt, *public.PublishedAt)
	}
	zeroAuthor, ok := cards[authorless.ID]
	if !ok || zeroAuthor.AuthorID != 0 {
		t.Fatalf("作者标识为零的公开行应被收录 got=%+v", zeroAuthor)
	}
}

// 测试目标：验证真实库上卡片批量读取的数量上限与查询次数
// 预期效果：上限内标识合并为一次参数化查询，超限时在查询数据库前返回 ErrInvalidCardBatch
func TestBatchGetCardsRealRepositoryBatchBoundary(t *testing.T) {
	gdb := testutil.DB(t)
	repo := video.NewRepository(gdb)
	if err := dbpkg.RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("注册查询计数回调失败: %v", err)
	}

	ids := []uint{0}
	for i := 0; i < domainfeed.MaxCardBatchSize; i++ {
		row := newFeedVideoEntity(1, video.VideoStatusPublished, feedTimePtr(feedBaseTime.Add(time.Duration(i)*time.Second)))
		persistFeedVideo(t, repo, &row)
		ids = append(ids, row.ID)
	}
	ids = append(ids, ids[1])

	reader := NewCardReader(repo)
	ctx := dbpkg.WithQueryCounter(context.Background())
	cards, err := reader.BatchGetCards(ctx, ids)
	if err != nil {
		t.Fatalf("上限内批量读取失败: %v", err)
	}
	if len(cards) != domainfeed.MaxCardBatchSize {
		t.Fatalf("上限内应返回全部卡片 got=%d", len(cards))
	}
	if count := dbpkg.QueryCount(ctx); count != 1 {
		t.Fatalf("上限内应只执行一次参数化查询 got=%d", count)
	}

	extra := newFeedVideoEntity(1, video.VideoStatusPublished, feedTimePtr(feedBaseTime.Add(time.Hour)))
	persistFeedVideo(t, repo, &extra)
	over := append(append([]uint{}, ids...), extra.ID)

	overCtx := dbpkg.WithQueryCounter(context.Background())
	got, err := reader.BatchGetCards(overCtx, over)
	if !errors.Is(err, domainfeed.ErrInvalidCardBatch) {
		t.Fatalf("超限应返回 ErrInvalidCardBatch, err=%v", err)
	}
	if got != nil {
		t.Fatalf("超限不应返回卡片 got=%v", got)
	}
	if count := dbpkg.QueryCount(overCtx); count != 0 {
		t.Fatalf("超限不应查询数据库 got=%d", count)
	}
}

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

type followingReaderStub struct {
	activeErr, videoErr            error
	rows                           []video.Video
	viewer                         uint
	cursor                         *video.Cursor
	limit, activeCalls, videoCalls int
	ctx                            context.Context
}

func (f *followingReaderStub) GetActiveUser(ctx context.Context, viewer uint) error {
	f.activeCalls++
	f.viewer = viewer
	f.ctx = ctx
	return f.activeErr
}

func (f *followingReaderStub) GetFollowingVideoList(ctx context.Context, viewer uint, cursor *video.Cursor, limit int) ([]video.Video, error) {
	f.videoCalls++
	f.viewer = viewer
	f.cursor = cursor
	f.limit = limit
	f.ctx = ctx
	return f.rows, f.videoErr
}

// 测试目标：异常公开查询结果不能在 LIMIT 后静默丢行
// 预期效果：零值时间、缺媒体或不可见行均报不可用并保留异常读模型原因
func TestFollowingReaderRejectsInvalidRows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*video.Video)
	}{
		{"zero_time", func(v *video.Video) { v.PublishedAt = new(time.Time) }},
		{"nil_time", func(v *video.Video) { v.PublishedAt = nil }},
		{"missing_media", func(v *video.Video) { v.CoverOriginalName = "" }},
		{"draft", func(v *video.Video) { v.Status = video.VideoStatusDraft }},
		{"deleted", func(v *video.Video) { v.DeletedAt = gorm.DeletedAt{Time: feedBaseTime, Valid: true} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := newPublicFeedVideoRow(11, 7, feedBaseTime)
			tc.mutate(&row)
			fake := &followingReaderStub{rows: []video.Video{row}}
			_, err := NewFollowingReader(fake, fake).ListFollowingPage(t.Context(), 42, nil, 2)
			if !errors.Is(err, domainfeed.ErrUnavailable) || !errors.Is(err, domainfeed.ErrInvalidReadResult) {
				t.Fatalf("异常行 err=%v", err)
			}
		})
	}
}
