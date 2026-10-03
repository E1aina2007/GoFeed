package video

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"gofeed/internal/testutil"
)

// 测试目标：验证轻量公开状态与现有完整卡片读取的真实 MySQL 可见性完全一致
// 预期效果：排除软删、非公开状态、缺失时间和任一媒体字段，保留零作者记录
func TestPublicVideoStatesVisibilityParity(t *testing.T) {
	gdb := testutil.DB(t)
	repo := NewRepository(gdb)
	ids := []uint{0}
	for _, status := range []string{VideoStatusPublished, VideoStatusDraft, VideoStatusProcessing, VideoStatusRejected, VideoStatusPurging} {
		v := seedVideo(t, repo, 0, status, status, baseTime)
		ids = append(ids, v.ID)
	}
	for _, field := range []string{"published_at", "play_url", "play_file_name", "play_original_name", "cover_url", "cover_file_name", "cover_original_name", "deleted_at"} {
		v := seedVideo(t, repo, 1, field, VideoStatusPublished, baseTime)
		ids = append(ids, v.ID)
		var value any = ""
		if field == "published_at" {
			value = nil
		}
		if field == "deleted_at" {
			value = baseTime
		}
		if err := gdb.Model(&Video{}).Where("id = ?", v.ID).UpdateColumn(field, value).Error; err != nil {
			t.Fatal(err)
		}
	}
	ids = append(ids, ids[1], 999999)
	states, err := repo.GetPublicVideoStates(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	full, err := repo.GetPublishedByIDs(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	a, b := []uint{}, []uint{}
	for _, v := range states {
		a = append(a, v.ID)
		if v.AuthorID != 0 {
			t.Fatal("作者零标识应保持")
		}
	}
	for _, v := range full {
		b = append(b, v.ID)
	}
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	if len(a) != 1 || !reflect.DeepEqual(a, b) {
		t.Fatalf("states=%v full=%v", a, b)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := repo.GetPublicVideoStates(ctx, ids); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// 测试目标：验证轻量状态批次的空、去重与 51 条边界
// 预期效果：空批次不需要数据库，51 个有效标识被接受，第 52 个被拒绝
func TestPublicVideoStatesBatchBounds(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	if got, err := repo.GetPublicVideoStates(t.Context(), []uint{0}); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got=%v err=%v", got, err)
	}
	ids := make([]uint, 51)
	for i := range ids {
		ids[i] = uint(i + 1)
	}
	if _, err := repo.GetPublicVideoStates(t.Context(), append(ids, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetPublicVideoStates(t.Context(), append(ids, 52)); !errors.Is(err, ErrInvalidPublishedVideoBatch) {
		t.Fatal(err)
	}
}
