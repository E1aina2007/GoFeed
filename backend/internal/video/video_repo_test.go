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

// baseTime 固定基准时间；时间统一用 time.Local 构造
// 与连接 DSN 的 Loc=time.Local 保持一致，避免时区导致的往返断言失败
var baseTime = time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)

func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// newVideoFixture 构造一条字段齐全的视频，状态与发布时间由调用方指定
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

// seedVideo 直接经仓储写入一条视频，返回回填 ID 后的实体
func seedVideo(t *testing.T, repo *Repository, authorID uint, title, status string, publishedAt time.Time) *Video {
	t.Helper()
	v := newVideoFixture(authorID, title, status, publishedAt)
	if err := repo.Create(context.Background(), v); err != nil {
		t.Fatalf("写入视频 %q 失败: %v", title, err)
	}
	return v
}

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

	// GetByID 不限制状态，管理侧应能读到草稿
	raw, err := repo.GetByID(ctx, draft.ID)
	if err != nil {
		t.Fatalf("GetByID 读草稿失败: %v", err)
	}
	if raw.Status != VideoStatusDraft {
		t.Fatalf("草稿状态读回错误 got=%s", raw.Status)
	}
}

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

func TestRepositoryListPublishedFilterAndOrder(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	// author 1：两条 published + 一条 draft；author 2：一条 published
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

func TestRepositoryListPublishedCursorPagination(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	titles := map[uint]string{}
	for i := 0; i < 5; i++ {
		v := seedVideo(t, repo, 1, fmt.Sprintf("v%d", i), VideoStatusPublished, baseTime.Add(time.Duration(i)*time.Minute))
		titles[v.ID] = v.Title
	}

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

func TestRepositoryListPublishedCursorTieBreak(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

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

	// 物理行保留，deleted_at 已写入，验证的是软删而非硬删
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
