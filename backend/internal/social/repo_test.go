package social

import (
	"context"
	"os"
	"testing"
	"time"

	"gofeed/internal/testutil"
	"gofeed/internal/user"
	"gofeed/internal/video"

	"gorm.io/gorm"
)

// 测试目标：初始化 social 包需要的独立 MySQL 测试库
// 预期效果：互动仓储测试在迁移后的隔离表结构中运行
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

func seedUser(t *testing.T, db *gorm.DB, username string) *user.User {
	t.Helper()
	account := &user.User{Username: username, Password: "test-password-hash"}
	if err := db.Create(account).Error; err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return account
}

func seedPublishedVideo(t *testing.T, db *gorm.DB, authorID uint) *video.Video {
	t.Helper()
	publishedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	item := &video.Video{
		AuthorID:    authorID,
		Title:       "social test video",
		Description: "",
		PlayURL:     "/static/videos/1/20260826/test.mp4",
		CoverURL:    "/static/covers/1/20260826/test.png",
		Status:      video.VideoStatusPublished,
		PublishedAt: &publishedAt,
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatalf("创建测试视频失败: %v", err)
	}
	return item
}

// 测试目标：验证互动仓储保存唯一关系并计算实时统计
// 预期效果：重复点赞和关注不重复写入，删除评论后统计立即减少
func TestRepositoryStoresInteractionsAndAggregatesMetrics(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	author := seedUser(t, db, "social-author")
	viewer := seedUser(t, db, "social-viewer")
	item := seedPublishedVideo(t, db, author.ID)

	created, err := repo.CreateLike(ctx, item.ID, viewer.ID)
	if err != nil || !created {
		t.Fatalf("首次点赞失败 created=%t err=%v", created, err)
	}
	created, err = repo.CreateLike(ctx, item.ID, viewer.ID)
	if err != nil || created {
		t.Fatalf("重复点赞应幂等 created=%t err=%v", created, err)
	}
	if _, err := repo.CreateFollow(ctx, viewer.ID, author.ID); err != nil {
		t.Fatalf("创建关注失败: %v", err)
	}
	comment := &Comment{VideoID: item.ID, AuthorID: viewer.ID, Content: "repo comment"}
	if err := repo.CreateComment(ctx, comment); err != nil {
		t.Fatalf("创建评论失败: %v", err)
	}

	engagement, err := repo.GetEngagementCounts(ctx, []uint{item.ID})
	if err != nil || engagement[item.ID].LikesCount != 1 || engagement[item.ID].CommentsCount != 1 {
		t.Fatalf("视频互动统计错误 counts=%+v err=%v", engagement, err)
	}
	metrics, err := repo.GetProfileMetrics(ctx, author.ID)
	if err != nil || metrics.TotalLikes != 1 || metrics.FollowerCount != 1 || metrics.VloggerCount != 0 {
		t.Fatalf("用户互动统计错误 metrics=%+v err=%v", metrics, err)
	}
	deleted, err := repo.DeleteComment(ctx, comment.ID, viewer.ID)
	if err != nil || !deleted {
		t.Fatalf("删除评论失败 deleted=%t err=%v", deleted, err)
	}
	engagement, err = repo.GetEngagementCounts(ctx, []uint{item.ID})
	if err != nil || engagement[item.ID].CommentsCount != 0 {
		t.Fatalf("软删除评论后统计错误 counts=%+v err=%v", engagement, err)
	}
}

// 测试目标：验证父视频硬删除会回收全部互动子记录
// 预期效果：外键级联后点赞和评论不再保留孤儿数据
func TestRepositoryHardDeleteVideoCascadesInteractions(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	author := seedUser(t, db, "cascade-author")
	viewer := seedUser(t, db, "cascade-viewer")
	item := seedPublishedVideo(t, db, author.ID)
	if _, err := repo.CreateLike(ctx, item.ID, viewer.ID); err != nil {
		t.Fatalf("创建点赞失败: %v", err)
	}
	if err := repo.CreateComment(ctx, &Comment{VideoID: item.ID, AuthorID: viewer.ID, Content: "cascade comment"}); err != nil {
		t.Fatalf("创建评论失败: %v", err)
	}
	if err := db.Unscoped().Delete(&video.Video{}, item.ID).Error; err != nil {
		t.Fatalf("硬删除视频失败: %v", err)
	}

	var likes, comments int64
	if err := db.Model(&VideoLike{}).Count(&likes).Error; err != nil {
		t.Fatalf("统计点赞失败: %v", err)
	}
	if err := db.Unscoped().Model(&Comment{}).Count(&comments).Error; err != nil {
		t.Fatalf("统计评论失败: %v", err)
	}
	if likes != 0 || comments != 0 {
		t.Fatalf("硬删除后仍有互动孤儿记录 likes=%d comments=%d", likes, comments)
	}
}
