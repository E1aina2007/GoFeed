package social

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"gorm.io/gorm"
)

type fakeStore struct {
	users        map[uint]bool
	videos       map[uint]bool
	likes        map[[2]uint]bool
	follows      map[[2]uint]bool
	comments     map[uint]Comment
	nextComment  uint
	commentClock time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:        map[uint]bool{1: true, 2: true, 3: true},
		videos:       map[uint]bool{10: true},
		likes:        make(map[[2]uint]bool),
		follows:      make(map[[2]uint]bool),
		comments:     make(map[uint]Comment),
		commentClock: time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC),
	}
}

func (f *fakeStore) GetActiveUser(_ context.Context, id uint) error {
	if !f.users[id] {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (f *fakeStore) GetPublishedVideo(_ context.Context, id uint) error {
	if !f.videos[id] {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (f *fakeStore) GetPublicUser(_ context.Context, id uint) (PublicUser, error) {
	if !f.users[id] {
		return PublicUser{}, gorm.ErrRecordNotFound
	}
	return PublicUser{ID: id, Username: "user"}, nil
}

func (f *fakeStore) CreateLike(_ context.Context, videoID, userID uint) (bool, error) {
	key := [2]uint{videoID, userID}
	if f.likes[key] {
		return false, nil
	}
	f.likes[key] = true
	return true, nil
}

func (f *fakeStore) RemoveLike(_ context.Context, videoID, userID uint) (bool, error) {
	key := [2]uint{videoID, userID}
	if !f.likes[key] {
		return false, nil
	}
	delete(f.likes, key)
	return true, nil
}

func (f *fakeStore) GetLikeState(_ context.Context, videoID, userID uint) (bool, error) {
	return f.likes[[2]uint{videoID, userID}], nil
}

func (f *fakeStore) GetLikeCount(_ context.Context, videoID uint) (int64, error) {
	var count int64
	for key := range f.likes {
		if key[0] == videoID {
			count++
		}
	}
	return count, nil
}

func (f *fakeStore) CreateFollow(_ context.Context, followerID, followeeID uint) (bool, error) {
	key := [2]uint{followerID, followeeID}
	if f.follows[key] {
		return false, nil
	}
	f.follows[key] = true
	return true, nil
}

func (f *fakeStore) RemoveFollow(_ context.Context, followerID, followeeID uint) (bool, error) {
	key := [2]uint{followerID, followeeID}
	if !f.follows[key] {
		return false, nil
	}
	delete(f.follows, key)
	return true, nil
}

func (f *fakeStore) GetFollowState(_ context.Context, followerID, followeeID uint) (bool, error) {
	return f.follows[[2]uint{followerID, followeeID}], nil
}

func (f *fakeStore) GetFollowerCount(_ context.Context, followeeID uint) (int64, error) {
	var count int64
	for key := range f.follows {
		if key[1] == followeeID {
			count++
		}
	}
	return count, nil
}

func (f *fakeStore) CreateComment(_ context.Context, comment *Comment) error {
	f.nextComment++
	comment.ID = f.nextComment
	comment.CreatedAt = f.commentClock.Add(time.Duration(comment.ID) * time.Second)
	f.comments[comment.ID] = *comment
	return nil
}

func (f *fakeStore) GetComment(_ context.Context, id uint) (*Comment, error) {
	comment, ok := f.comments[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &comment, nil
}

func (f *fakeStore) DeleteComment(_ context.Context, id, authorID uint) (bool, error) {
	comment, ok := f.comments[id]
	if !ok || comment.AuthorID != authorID {
		return false, nil
	}
	delete(f.comments, id)
	return true, nil
}

func (f *fakeStore) GetCommentList(_ context.Context, videoID uint, cursor *CommentCursor, limit int) ([]CommentItem, error) {
	items := make([]CommentItem, 0, len(f.comments))
	for _, comment := range f.comments {
		if comment.VideoID != videoID {
			continue
		}
		if cursor != nil && (comment.CreatedAt.After(cursor.CreatedAt) || (comment.CreatedAt.Equal(cursor.CreatedAt) && comment.ID >= cursor.ID)) {
			continue
		}
		items = append(items, CommentItem{
			ID:        comment.ID,
			VideoID:   comment.VideoID,
			Author:    PublicUser{ID: comment.AuthorID, Username: "user"},
			Content:   comment.Content,
			CreatedAt: comment.CreatedAt,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (f *fakeStore) GetFollowerList(_ context.Context, _ uint, _ *FollowCursor, _ int) ([]FollowListItem, error) {
	return nil, nil
}

func (f *fakeStore) GetFollowingList(_ context.Context, _ uint, _ *FollowCursor, _ int) ([]FollowListItem, error) {
	return nil, nil
}

// 测试目标：验证点赞和取消点赞在重复请求下保持幂等
// 预期效果：重复写入不会重复计数，重复取消保持未点赞状态
func TestServiceCreateAndRemoveLikeAreIdempotent(t *testing.T) {
	service := NewService(newFakeStore())
	ctx := context.Background()

	first, err := service.CreateLike(ctx, 10, 1)
	if err != nil || !first.Liked || first.LikesCount != 1 {
		t.Fatalf("首次点赞结果错误 state=%+v err=%v", first, err)
	}
	second, err := service.CreateLike(ctx, 10, 1)
	if err != nil || !second.Liked || second.LikesCount != 1 {
		t.Fatalf("重复点赞结果错误 state=%+v err=%v", second, err)
	}
	first, err = service.RemoveLike(ctx, 10, 1)
	if err != nil || first.Liked || first.LikesCount != 0 {
		t.Fatalf("首次取消点赞结果错误 state=%+v err=%v", first, err)
	}
	second, err = service.RemoveLike(ctx, 10, 1)
	if err != nil || second.Liked || second.LikesCount != 0 {
		t.Fatalf("重复取消点赞结果错误 state=%+v err=%v", second, err)
	}
}

// 测试目标：验证关注和评论写入校验资源归属与作者权限
// 预期效果：禁止自关注和越权删评，合法评论可由作者删除
func TestServiceCreateFollowAndCommentBoundaries(t *testing.T) {
	service := NewService(newFakeStore())
	ctx := context.Background()

	if _, err := service.CreateFollow(ctx, 1, 1); !errors.Is(err, ErrSelfFollow) {
		t.Fatalf("自关注应被拒绝 err=%v", err)
	}
	follow, err := service.CreateFollow(ctx, 1, 2)
	if err != nil || !follow.Following || follow.FollowerCount != 1 {
		t.Fatalf("关注结果错误 state=%+v err=%v", follow, err)
	}
	comment, err := service.CreateComment(ctx, 10, 1, "  第一条评论  ")
	if err != nil || comment.Content != "第一条评论" || comment.Author.ID != 1 {
		t.Fatalf("创建评论结果错误 comment=%+v err=%v", comment, err)
	}
	if err := service.DeleteComment(ctx, 10, comment.ID, 2); !errors.Is(err, ErrCommentNotAuthor) {
		t.Fatalf("越权删评应被拒绝 err=%v", err)
	}
	if err := service.DeleteComment(ctx, 10, comment.ID, 1); err != nil {
		t.Fatalf("作者删除评论失败 err=%v", err)
	}
}

// 测试目标：验证评论列表使用不透明游标保持倒序分页稳定
// 预期效果：连续分页不会重复或遗漏已创建的评论
func TestServiceListCommentsPaginatesByCreatedAtAndID(t *testing.T) {
	service := NewService(newFakeStore())
	ctx := context.Background()
	for _, content := range []string{"one", "two", "three"} {
		if _, err := service.CreateComment(ctx, 10, 1, content); err != nil {
			t.Fatalf("创建评论失败 content=%s err=%v", content, err)
		}
	}

	first, err := service.GetCommentList(ctx, 10, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("首页评论分页错误 response=%+v err=%v", first, err)
	}
	second, err := service.GetCommentList(ctx, 10, first.NextCursor, 2)
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("下一页评论分页错误 response=%+v err=%v", second, err)
	}
	if first.Items[0].ID == second.Items[0].ID || first.Items[1].ID == second.Items[0].ID {
		t.Fatalf("评论分页出现重复 first=%+v second=%+v", first.Items, second.Items)
	}
}
