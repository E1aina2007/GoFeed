package infrafeed

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	dbpkg "gofeed/internal/db"
	domainfeed "gofeed/internal/domain/feed"
	"gofeed/internal/testutil"
	"gofeed/internal/video"

	"gorm.io/gorm"
)

// fakePublishedBatchReader 记录批量读取的调用次数与标识集合，并按预设结果返回
type fakePublishedBatchReader struct {
	calls int
	ids   [][]uint
	rows  []video.Video
	err   error
}

func (f *fakePublishedBatchReader) GetPublishedByIDs(ctx context.Context, ids []uint) ([]video.Video, error) {
	f.calls++
	f.ids = append(f.ids, append([]uint(nil), ids...))
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// lastIDs 返回最近一次批量读取收到的标识集合
func (f *fakePublishedBatchReader) lastIDs() []uint {
	if len(f.ids) == 0 {
		return nil
	}
	return f.ids[len(f.ids)-1]
}

// 测试目标：构造指定数量的公开完整视频实体及其标识集合
// 预期效果：标识从起始值连续递增，发布时间互不相同
func newPublicFeedVideoRows(startID uint, count int) ([]video.Video, []uint) {
	rows := make([]video.Video, 0, count)
	ids := make([]uint, 0, count)
	for i := 0; i < count; i++ {
		id := startID + uint(i)
		rows = append(rows, newPublicFeedVideoRow(id, 1, feedBaseTime.Add(time.Duration(i)*time.Second)))
		ids = append(ids, id)
	}
	return rows, ids
}

// 测试目标：通过真实仓储写入视频实体
// 预期效果：写入失败时终止用例并回填自增标识
func persistFeedVideo(t *testing.T, repo *video.Repository, row *video.Video) {
	t.Helper()
	if err := repo.Create(context.Background(), row); err != nil {
		t.Fatalf("写入视频 %q 失败: %v", row.Title, err)
	}
}

var _ PublishedVideoBatchReader = (*fakePublishedBatchReader)(nil)

// 测试目标：验证空批次卡片读取不访问底层仓储
// 预期效果：空集合与全零集合返回非 nil 空 map 且读取调用次数为零
func TestBatchGetCardsEmptyBatchSkipsReader(t *testing.T) {
	ctx := context.Background()
	reader := &fakePublishedBatchReader{}
	cards := NewCardReader(reader)

	for _, ids := range [][]uint{nil, {}, {0, 0, 0}} {
		got, err := cards.BatchGetCards(ctx, ids)
		if err != nil {
			t.Fatalf("空批次不应报错 ids=%v err=%v", ids, err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("空批次应返回非 nil 空 map ids=%v got=%v", ids, got)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("空批次不应查询底层仓储 calls=%d", reader.calls)
	}

	if got, err := NewCardReader(nil).BatchGetCards(ctx, nil); err != nil || got == nil {
		t.Fatalf("nil 读取器下的空批次应返回非 nil 空 map, got=%v err=%v", got, err)
	}
}

// 测试目标：验证卡片批量读取忽略零标识并按首次出现去重
// 预期效果：底层仓储只收到一次调用且标识集合保持首次出现顺序
func TestBatchGetCardsDedupesAndIgnoresZero(t *testing.T) {
	rows, _ := newPublicFeedVideoRows(3, 3)
	reader := &fakePublishedBatchReader{rows: rows}
	cards, err := NewCardReader(reader).BatchGetCards(context.Background(), []uint{3, 0, 5, 3, 5, 0, 4})
	if err != nil {
		t.Fatalf("批量读取卡片失败: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("批量读取应只调用一次底层仓储 calls=%d", reader.calls)
	}
	if !slices.Equal(reader.lastIDs(), []uint{3, 5, 4}) {
		t.Fatalf("去重后的标识集合错误 got=%v", reader.lastIDs())
	}
	if len(cards) != 3 {
		t.Fatalf("卡片数量应等于有效唯一标识数 got=%d", len(cards))
	}
	for _, id := range []uint{3, 4, 5} {
		if card, ok := cards[id]; !ok || card.VideoID != id {
			t.Fatalf("标识 %d 的卡片缺失或错位 got=%+v", id, card)
		}
	}
}

// 测试目标：验证卡片批量读取的有效标识数量上限
// 预期效果：上限内一次参数化查询成功，超出一个有效标识即整体失败且不查询底层仓储
func TestBatchGetCardsBatchSizeBoundary(t *testing.T) {
	ctx := context.Background()
	rows, ids := newPublicFeedVideoRows(1, domainfeed.MaxCardBatchSize)
	reader := &fakePublishedBatchReader{rows: rows}

	cards, err := NewCardReader(reader).BatchGetCards(ctx, ids)
	if err != nil {
		t.Fatalf("上限内批量读取失败: %v", err)
	}
	if len(cards) != domainfeed.MaxCardBatchSize {
		t.Fatalf("上限内应返回全部卡片 got=%d", len(cards))
	}
	if reader.calls != 1 || len(reader.lastIDs()) != domainfeed.MaxCardBatchSize {
		t.Fatalf("上限内应一次传全部标识 calls=%d ids=%d", reader.calls, len(reader.lastIDs()))
	}

	duplicated := append(append([]uint{}, ids...), ids[0])
	dupReader := &fakePublishedBatchReader{rows: rows}
	if got, err := NewCardReader(dupReader).BatchGetCards(ctx, duplicated); err != nil || len(got) != domainfeed.MaxCardBatchSize {
		t.Fatalf("重复标识去重后不应超限 got=%d err=%v", len(got), err)
	}

	overRows, overIDs := newPublicFeedVideoRows(1, domainfeed.MaxCardBatchSize+1)
	overReader := &fakePublishedBatchReader{rows: overRows}
	got, err := NewCardReader(overReader).BatchGetCards(ctx, overIDs)
	if !errors.Is(err, domainfeed.ErrInvalidCardBatch) {
		t.Fatalf("超限应返回 ErrInvalidCardBatch, err=%v", err)
	}
	if got != nil {
		t.Fatalf("超限不应返回卡片 got=%v", got)
	}
	if overReader.calls != 0 {
		t.Fatalf("超限不应查询底层仓储 calls=%d", overReader.calls)
	}
}

// 测试目标：验证卡片按标识对齐而非依赖返回顺序
// 预期效果：乱序结果仍按标识建 map，未被请求的行被忽略
func TestBatchGetCardsMapsByIDAndIgnoresUnrequested(t *testing.T) {
	reader := &fakePublishedBatchReader{rows: []video.Video{
		newPublicFeedVideoRow(9, 1, feedBaseTime),
		newPublicFeedVideoRow(77, 1, feedBaseTime),
		newPublicFeedVideoRow(4, 1, feedBaseTime),
	}}
	cards, err := NewCardReader(reader).BatchGetCards(context.Background(), []uint{4, 9})
	if err != nil {
		t.Fatalf("批量读取卡片失败: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("结果应只包含被请求的标识 got=%+v", cards)
	}
	if _, ok := cards[77]; ok {
		t.Fatalf("未被请求的行不应出现在结果中 got=%+v", cards)
	}
	if card, ok := cards[4]; !ok || card.VideoID != 4 {
		t.Fatalf("标识 4 的卡片缺失或错位 got=%+v", card)
	}
	if card, ok := cards[9]; !ok || card.VideoID != 9 {
		t.Fatalf("标识 9 的卡片缺失或错位 got=%+v", card)
	}
}

// 测试目标：验证卡片批量读取只收录公开完整视频
// 预期效果：缺失、软删除、非公开状态、媒体不完整和发布时间为空的行都不出现在结果中
func TestBatchGetCardsSkipsInvisibleRows(t *testing.T) {
	ctx := context.Background()
	invisible := []struct {
		id     uint
		mutate func(*video.Video)
	}{
		{id: 21, mutate: func(row *video.Video) { row.Status = video.VideoStatusDraft }},
		{id: 22, mutate: func(row *video.Video) { row.Status = video.VideoStatusProcessing }},
		{id: 23, mutate: func(row *video.Video) { row.Status = video.VideoStatusRejected }},
		{id: 24, mutate: func(row *video.Video) { row.Status = video.VideoStatusPurging }},
		{id: 25, mutate: func(row *video.Video) { row.DeletedAt = gorm.DeletedAt{Time: feedBaseTime, Valid: true} }},
		{id: 26, mutate: func(row *video.Video) { row.PublishedAt = nil }},
		{id: 27, mutate: func(row *video.Video) { row.PublishedAt = feedTimePtr(time.Time{}) }},
		{id: 28, mutate: func(row *video.Video) { row.PlayURL = "" }},
		{id: 29, mutate: func(row *video.Video) { row.PlayFileName = "" }},
		{id: 30, mutate: func(row *video.Video) { row.PlayOriginalName = "" }},
		{id: 31, mutate: func(row *video.Video) { row.CoverURL = "" }},
		{id: 32, mutate: func(row *video.Video) { row.CoverFileName = "" }},
		{id: 33, mutate: func(row *video.Video) { row.CoverOriginalName = "" }},
	}

	public := newPublicFeedVideoRow(20, 1, feedBaseTime)
	rows := []video.Video{public}
	ids := []uint{public.ID}
	for _, item := range invisible {
		row := newPublicFeedVideoRow(item.id, 1, feedBaseTime)
		item.mutate(&row)
		rows = append(rows, row)
		ids = append(ids, row.ID)
	}
	ids = append(ids, 34)

	reader := &fakePublishedBatchReader{rows: rows}
	cards, err := NewCardReader(reader).BatchGetCards(ctx, ids)
	if err != nil {
		t.Fatalf("批量读取卡片失败: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("结果应只收录一条公开完整视频 got=%+v", cards)
	}
	if _, ok := cards[public.ID]; !ok {
		t.Fatalf("公开完整视频应被收录 got=%+v", cards)
	}
	for _, item := range invisible {
		if _, ok := cards[item.id]; ok {
			t.Fatalf("标识 %d 不应出现在结果中", item.id)
		}
	}
}

// 测试目标：验证卡片字段逐项映射且作者标识为零的行仍被收录
// 预期效果：卡片字段与实体完全一致，包括媒体原始文件名和发布时间
func TestBatchGetCardsMapsAllFields(t *testing.T) {
	published := feedBaseTime.Add(90 * time.Minute)
	row := newFeedVideoEntity(0, video.VideoStatusPublished, feedTimePtr(published))
	row.ID = 41
	row.Title = "标题文本"
	row.Description = "描述文本"
	row.PlayURL = "/static/videos/41/play.mp4"
	row.PlayFileName = "play.mp4"
	row.PlayOriginalName = "原始播放名.mp4"
	row.CoverURL = "/static/covers/41/cover.webp"
	row.CoverFileName = "cover.webp"
	row.CoverOriginalName = "原始封面名.webp"

	cards, err := NewCardReader(&fakePublishedBatchReader{rows: []video.Video{row}}).BatchGetCards(context.Background(), []uint{41})
	if err != nil {
		t.Fatalf("批量读取卡片失败: %v", err)
	}
	card, ok := cards[41]
	if !ok {
		t.Fatalf("作者标识为零的行应被收录 got=%+v", cards)
	}
	if card.VideoID != 41 || card.AuthorID != 0 {
		t.Fatalf("标识字段映射错误 got=%+v", card)
	}
	if card.Title != row.Title || card.Description != row.Description {
		t.Fatalf("文本字段映射错误 got=%+v", card)
	}
	if card.PlayURL != row.PlayURL || card.PlayFileName != row.PlayFileName || card.PlayOriginalName != row.PlayOriginalName {
		t.Fatalf("播放媒体字段映射错误 got=%+v", card)
	}
	if card.CoverURL != row.CoverURL || card.CoverFileName != row.CoverFileName || card.CoverOriginalName != row.CoverOriginalName {
		t.Fatalf("封面媒体字段映射错误 got=%+v", card)
	}
	if !card.PublishedAt.Equal(published) {
		t.Fatalf("发布时间映射错误 got=%v want=%v", card.PublishedAt, published)
	}
}

// 测试目标：验证卡片批量读取的错误语义与 cause 保留
// 预期效果：批上限错误映射为 ErrInvalidCardBatch，其余错误映射为 ErrUnavailable 并保留原始错误
func TestBatchGetCardsErrorSemantics(t *testing.T) {
	ctx := context.Background()

	cause := errors.New("批量读取失败")
	_, err := NewCardReader(&fakePublishedBatchReader{err: cause}).BatchGetCards(ctx, []uint{1})
	if !errors.Is(err, domainfeed.ErrUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("底层错误应包装为 ErrUnavailable 且保留 cause, err=%v", err)
	}

	_, err = NewCardReader(&fakePublishedBatchReader{err: video.ErrInvalidPublishedVideoBatch}).BatchGetCards(ctx, []uint{1})
	if !errors.Is(err, domainfeed.ErrInvalidCardBatch) {
		t.Fatalf("批上限错误应映射为 ErrInvalidCardBatch, err=%v", err)
	}

	wrapped := fmt.Errorf("批量读取包装: %w", video.ErrInvalidPublishedVideoBatch)
	_, err = NewCardReader(&fakePublishedBatchReader{err: wrapped}).BatchGetCards(ctx, []uint{1})
	if !errors.Is(err, domainfeed.ErrInvalidCardBatch) {
		t.Fatalf("包装后的批上限错误应映射为 ErrInvalidCardBatch, err=%v", err)
	}

	_, err = NewCardReader(nil).BatchGetCards(ctx, []uint{1})
	if !errors.Is(err, domainfeed.ErrUnavailable) {
		t.Fatalf("nil 读取器应返回 ErrUnavailable, err=%v", err)
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
