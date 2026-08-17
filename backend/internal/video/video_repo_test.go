package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"gofeed/internal/testutil"

	"gorm.io/gorm"
)

// 测试目标：固定仓储测试的基准时间
// 预期效果：统一使用本地时区，避免读写往返产生时区断言差异
var baseTime = time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)

// 测试目标：配置视频仓储集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：构造字段齐全的视频测试数据
// 预期效果：调用方可指定作者、标题、状态和发布时间
func newVideoFixture(authorID uint, title, status string, publishedAt time.Time) *Video {
	return &Video{
		AuthorID:          authorID,
		Title:             title,
		Description:       "集成测试描述",
		PlayURL:           "/static/videos/1/a.mp4",
		PlayFileName:      "a.mp4",
		PlayOriginalName:  "原始视频.mp4",
		CoverURL:          "/static/covers/1/a.webp",
		CoverFileName:     "a.webp",
		CoverOriginalName: "封面.webp",
		Status:            status,
		PublishedAt:       publishedAt,
	}
}

// 测试目标：通过仓储写入一条视频测试数据
// 预期效果：返回已回填视频标识的实体
func seedVideo(t *testing.T, repo *Repository, authorID uint, title, status string, publishedAt time.Time) *Video {
	t.Helper()
	v := newVideoFixture(authorID, title, status, publishedAt)
	if err := repo.Create(context.Background(), v); err != nil {
		t.Fatalf("写入视频 %q 失败: %v", title, err)
	}
	return v
}

// 测试目标：验证视频仓储创建并按标识读取视频
// 预期效果：创建操作回填标识和时间，读取结果与写入字段一致
func TestRepositoryCreateAndGetByID(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	v := newVideoFixture(1, "往返", VideoStatusPublished, baseTime)
	if err := repo.Create(ctx, v); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if v.ID == 0 {
		t.Fatal("Create 未回填 ID")
	}
	if v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() {
		t.Fatalf("Create 未回填时间戳 created=%v updated=%v", v.CreatedAt, v.UpdatedAt)
	}

	got, err := repo.GetByID(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != v.ID || got.AuthorID != v.AuthorID || got.Title != v.Title || got.Description != v.Description {
		t.Fatalf("基础字段读回不一致 got=%+v want=%+v", got, v)
	}
	if got.PlayFileName != v.PlayFileName || got.PlayOriginalName != v.PlayOriginalName ||
		got.CoverFileName != v.CoverFileName || got.CoverOriginalName != v.CoverOriginalName {
		t.Fatalf("媒体文件名读回不一致 got=%+v", got)
	}
	if got.Status != v.Status {
		t.Fatalf("状态读回不一致 got=%s want=%s", got.Status, v.Status)
	}
	if !got.PublishedAt.Equal(v.PublishedAt) {
		t.Fatalf("PublishedAt 读回不一致 got=%v want=%v", got.PublishedAt, v.PublishedAt)
	}
}

// 测试目标：验证公开读取仅返回已发布视频
// 预期效果：草稿无法公开读取，通用读取仍可读取草稿
func TestRepositoryGetPublishedByIDFiltersStatus(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	published := seedVideo(t, repo, 1, "已发布", VideoStatusPublished, baseTime)
	draft := seedVideo(t, repo, 1, "草稿", VideoStatusDraft, baseTime.Add(time.Minute))

	if _, err := repo.GetPublishedByID(ctx, draft.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("草稿不应通过公开读取到, err=%v", err)
	}
	got, err := repo.GetPublishedByID(ctx, published.ID)
	if err != nil {
		t.Fatalf("已发布视频应可读: %v", err)
	}
	if got.ID != published.ID {
		t.Fatalf("got id=%d want=%d", got.ID, published.ID)
	}

	// 测试目标：验证管理侧读取不限制视频状态
	// 预期效果：草稿可通过通用读取接口返回
	raw, err := repo.GetByID(ctx, draft.ID)
	if err != nil {
		t.Fatalf("GetByID 读草稿失败: %v", err)
	}
	if raw.Status != VideoStatusDraft {
		t.Fatalf("草稿状态读回错误 got=%s", raw.Status)
	}
}

// 测试目标：验证零值视频标识的读取边界
// 预期效果：通用读取和公开读取均返回记录不存在错误
func TestRepositoryGetByIDZero(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	if _, err := repo.GetByID(ctx, 0); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("id=0 应返回 not found, err=%v", err)
	}
	if _, err := repo.GetPublishedByID(ctx, 0); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("公开读 id=0 应返回 not found, err=%v", err)
	}
}

// 测试目标：验证公开视频列表的状态过滤、作者过滤和排序
// 预期效果：仅返回已发布视频并按发布时间倒序排列
func TestRepositoryListPublishedFilterAndOrder(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	// 测试目标：准备不同作者和状态的视频测试数据
	// 预期效果：可同时验证全局列表和作者列表的过滤与排序
	seedVideo(t, repo, 1, "a1-new", VideoStatusPublished, baseTime.Add(2*time.Minute))
	seedVideo(t, repo, 1, "a1-old", VideoStatusPublished, baseTime)
	seedVideo(t, repo, 1, "a1-draft", VideoStatusDraft, baseTime.Add(3*time.Minute))
	seedVideo(t, repo, 2, "a2", VideoStatusPublished, baseTime.Add(time.Minute))

	all, err := repo.ListPublished(ctx, 0, nil, 10)
	if err != nil {
		t.Fatalf("ListPublished 全局列表: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("全局列表应只有 3 条 published, got=%d", len(all))
	}
	want := []string{"a1-new", "a2", "a1-old"}
	for i, title := range want {
		if all[i].Title != title {
			t.Fatalf("全局列表顺序错误 index=%d got=%s want=%s", i, all[i].Title, title)
		}
	}

	mine, err := repo.ListPublished(ctx, 1, nil, 10)
	if err != nil {
		t.Fatalf("ListPublished 作者列表: %v", err)
	}
	if len(mine) != 2 {
		t.Fatalf("author 1 应只有 2 条 published, got=%d", len(mine))
	}
	if mine[0].Title != "a1-new" || mine[1].Title != "a1-old" {
		t.Fatalf("作者列表顺序错误 got=%v", mine)
	}
}

// 测试目标：验证公开视频列表的游标分页完整性
// 预期效果：所有视频按倒序仅返回一次且不会遗漏或重复
func TestRepositoryListPublishedCursorPagination(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	// 测试目标：记录写入视频的标识与标题对应关系
	// 预期效果：后续可按翻页结果验证完整排序
	titles := map[uint]string{}
	for i := 0; i < 5; i++ {
		v := seedVideo(t, repo, 1, fmt.Sprintf("v%d", i), VideoStatusPublished, baseTime.Add(time.Duration(i)*time.Minute))
		titles[v.ID] = v.Title
	}

	// 测试目标：记录已读取视频和分页游标结果
	// 预期效果：可检测重复项并验证翻页完整性
	seen := map[uint]bool{}
	var cursor *Cursor
	var got []uint
	for page := 0; page < 10; page++ {
		items, err := repo.ListPublished(ctx, 0, cursor, 2)
		if err != nil {
			t.Fatalf("第 %d 页查询失败: %v", page, err)
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if seen[item.ID] {
				t.Fatalf("游标翻页重复返回 id=%d", item.ID)
			}
			seen[item.ID] = true
			got = append(got, item.ID)
		}
		last := items[len(items)-1]
		cursor = &Cursor{PublishedAt: last.PublishedAt, ID: last.ID}
	}

	if len(got) != 5 {
		t.Fatalf("应完整翻出 5 条, got=%v", got)
	}
	want := []string{"v4", "v3", "v2", "v1", "v0"}
	for i, id := range got {
		if titles[id] != want[i] {
			t.Fatalf("翻页顺序错误 index=%d id=%d title=%s want=%s", i, id, titles[id], want[i])
		}
	}
}

// 测试目标：验证发布时间相同时游标分页的次级排序
// 预期效果：视频按标识倒序返回且翻页边界不重复不遗漏
func TestRepositoryListPublishedCursorTieBreak(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	// 测试目标：准备发布时间相同的视频和其标识集合
	// 预期效果：验证同一时间点以视频标识作为次级排序条件
	same := baseTime
	var ids []uint
	for i := 0; i < 3; i++ {
		v := seedVideo(t, repo, 1, fmt.Sprintf("same-%d", i), VideoStatusPublished, same)
		ids = append(ids, v.ID)
	}

	first, err := repo.ListPublished(ctx, 0, nil, 2)
	if err != nil {
		t.Fatalf("第一页查询失败: %v", err)
	}
	if len(first) != 2 || first[0].ID != ids[2] || first[1].ID != ids[1] {
		t.Fatalf("同一 published_at 应按 id 倒序, got ids=%v", []uint{first[0].ID, first[1].ID})
	}

	last := first[1]
	second, err := repo.ListPublished(ctx, 0, &Cursor{PublishedAt: last.PublishedAt, ID: last.ID}, 2)
	if err != nil {
		t.Fatalf("第二页查询失败: %v", err)
	}
	if len(second) != 1 || second[0].ID != ids[0] {
		t.Fatalf("第二页应只剩 id=%d, got=%+v", ids[0], second)
	}

	empty, err := repo.ListPublished(ctx, 0, &Cursor{PublishedAt: same, ID: ids[0]}, 2)
	if err != nil {
		t.Fatalf("末页查询失败: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("游标指向最后一条后应为空, got=%+v", empty)
	}
}

// 测试目标：验证个人视频列表包含该作者的全部状态
// 预期效果：返回发布、草稿和处理中的视频，其他作者和零值作者不返回数据
func TestRepositoryListByAuthorIncludesAllStatuses(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	seedVideo(t, repo, 1, "pub", VideoStatusPublished, baseTime)
	seedVideo(t, repo, 1, "draft", VideoStatusDraft, baseTime.Add(time.Minute))
	seedVideo(t, repo, 1, "proc", VideoStatusProcessing, baseTime.Add(2*time.Minute))
	seedVideo(t, repo, 2, "other", VideoStatusPublished, baseTime.Add(3*time.Minute))

	mine, err := repo.ListByAuthor(ctx, 1, nil, 10)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(mine) != 3 {
		t.Fatalf("author 1 应有 3 条不分状态, got=%d", len(mine))
	}
	// 测试目标：收集个人列表中的视频状态
	// 预期效果：可验证每种预期状态均被返回
	statuses := map[string]bool{}
	for _, v := range mine {
		statuses[v.Status] = true
	}
	for _, want := range []string{VideoStatusPublished, VideoStatusDraft, VideoStatusProcessing} {
		if !statuses[want] {
			t.Fatalf("缺少状态 %s, got=%v", want, statuses)
		}
	}

	if empty, err := repo.ListByAuthor(ctx, 999, nil, 10); err != nil || len(empty) != 0 {
		t.Fatalf("不存在的作者应为空, got=%v err=%v", empty, err)
	}
	if empty, err := repo.ListByAuthor(ctx, 0, nil, 10); err != nil || len(empty) != 0 {
		t.Fatalf("authorID=0 应为空, got=%v err=%v", empty, err)
	}
}

// 测试目标：验证视频仓储执行软删除而非物理删除
// 预期效果：物理行保留删除时间，所有读取列表均排除该视频且重复删除报错
func TestRepositoryDeleteSoftDeletes(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	v := seedVideo(t, repo, 1, "删除", VideoStatusPublished, baseTime)

	if err := repo.Delete(ctx, v.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, v.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("软删后 GetByID 应 not found, err=%v", err)
	}
	if _, err := repo.GetPublishedByID(ctx, v.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("软删后公开读应 not found, err=%v", err)
	}

	// 测试目标：检查软删除后的物理记录
	// 预期效果：物理行保留且删除时间已写入
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM videos WHERE id = ?", v.ID).Scan(&count).Error; err != nil {
		t.Fatalf("原生统计失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("物理行应保留, count=%d", count)
	}
	var deletedAt *time.Time
	if err := db.Raw("SELECT deleted_at FROM videos WHERE id = ?", v.ID).Scan(&deletedAt).Error; err != nil {
		t.Fatalf("原生读 deleted_at 失败: %v", err)
	}
	if deletedAt == nil {
		t.Fatal("deleted_at 应为非空")
	}

	items, err := repo.ListPublished(ctx, 1, nil, 10)
	if err != nil {
		t.Fatalf("删除后列表查询失败: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("删除后列表应不含该视频, got=%+v", items)
	}

	if err := repo.Delete(ctx, v.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("重复删除应 not found, err=%v", err)
	}
}

// 测试目标：验证公开视频列表处理非正分页数量
// 预期效果：数量为零时查询成功并返回空列表
func TestRepositoryListPublishedNonPositiveLimit(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	seedVideo(t, repo, 1, "v", VideoStatusPublished, baseTime)

	items, err := repo.ListPublished(ctx, 0, nil, 0)
	if err != nil {
		t.Fatalf("limit=0 查询失败: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("limit=0 应为空, got=%+v", items)
	}
}
