package sweeper

import (
	"context"
	"os"
	"testing"

	"gofeed/internal/testutil"
	"gofeed/internal/user"
	"gofeed/internal/video"
)

// 测试目标：为 sweeper 仓储用例初始化隔离的真实 MySQL 数据库
// 预期效果：媒体引用查询与迁移后的 users/videos 表结构保持一致
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：验证媒体引用查询包含视频和头像，并保留软删除保留期中的引用
// 预期效果：孤儿清扫不会删除活跃或尚未硬删除记录仍持有的本地媒体
func TestMediaReferenceRepositoryListReferencedMediaURLsIncludesSoftDeletedRecords(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	activeAvatar := "/static/avatars/1/20260928/active_0123456789abcdef0123456789abcdef.png"
	deletedAvatar := "/static/avatars/2/20260928/deleted_0123456789abcdef0123456789abcdef.png"
	if err := db.Create(&user.User{Username: "active", Password: "hash", AvatarURL: activeAvatar}).Error; err != nil {
		t.Fatalf("创建活跃用户失败: %v", err)
	}
	deletedUser := &user.User{Username: "deleted", Password: "hash", AvatarURL: deletedAvatar}
	if err := db.Create(deletedUser).Error; err != nil {
		t.Fatalf("创建软删用户失败: %v", err)
	}
	if err := db.Delete(deletedUser).Error; err != nil {
		t.Fatalf("软删除用户失败: %v", err)
	}

	activePlay := "/static/videos/1/20260928/play_0123456789abcdef0123456789abcdef.mp4"
	deletedCover := "/static/covers/2/20260928/cover_0123456789abcdef0123456789abcdef.png"
	activeVideo := &video.Video{AuthorID: 1, Title: "active", PlayURL: activePlay, Status: video.VideoStatusDraft}
	if err := db.Create(activeVideo).Error; err != nil {
		t.Fatalf("创建活跃视频失败: %v", err)
	}
	deletedVideo := &video.Video{AuthorID: 2, Title: "deleted", CoverURL: deletedCover, Status: video.VideoStatusDraft}
	if err := db.Create(deletedVideo).Error; err != nil {
		t.Fatalf("创建软删视频失败: %v", err)
	}
	if err := db.Delete(deletedVideo).Error; err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}

	urls, err := NewMediaReferenceRepository(db).ListReferencedMediaURLs(ctx)
	if err != nil {
		t.Fatalf("ListReferencedMediaURLs: %v", err)
	}
	got := make(map[string]struct{}, len(urls))
	for _, value := range urls {
		got[value] = struct{}{}
	}
	for _, want := range []string{activeAvatar, deletedAvatar, activePlay, deletedCover} {
		if _, exists := got[want]; !exists {
			t.Fatalf("媒体引用缺失 want=%s got=%v", want, urls)
		}
	}
}
