package social

import (
	"context"
	"errors"
	"time"

	"gofeed/internal/user"
	"gofeed/internal/video"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) GetActiveUser(ctx context.Context, id uint) error {
	if id == 0 {
		return gorm.ErrRecordNotFound
	}
	var count int64
	err := r.db.WithContext(ctx).Table("users").
		Where("id = ? AND deleted_at IS NULL", id).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Repository) GetPublicUser(ctx context.Context, id uint) (PublicUser, error) {
	var account PublicUser
	err := r.db.WithContext(ctx).Table("users").
		Select("id, username, avatar_url, bio").
		Where("id = ? AND deleted_at IS NULL", id).
		Take(&account).Error
	return account, err
}

func (r *Repository) GetPublishedVideo(ctx context.Context, id uint) error {
	if id == 0 {
		return gorm.ErrRecordNotFound
	}
	var count int64
	err := r.db.WithContext(ctx).Table("videos").
		Where("id = ? AND status = ? AND deleted_at IS NULL", id, video.VideoStatusPublished).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Repository) CreateLike(ctx context.Context, videoID, userID uint) (bool, error) {
	like := &VideoLike{VideoID: videoID, UserID: userID}
	if err := r.db.WithContext(ctx).Create(like).Error; err != nil {
		if isDuplicateKey(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *Repository) RemoveLike(ctx context.Context, videoID, userID uint) (bool, error) {
	result := r.db.WithContext(ctx).
		Where("video_id = ? AND user_id = ?", videoID, userID).
		Delete(&VideoLike{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (r *Repository) GetLikeState(ctx context.Context, videoID, userID uint) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&VideoLike{}).
		Where("video_id = ? AND user_id = ?", videoID, userID).
		Count(&count).Error
	return count > 0, err
}

func (r *Repository) GetLikeCount(ctx context.Context, videoID uint) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&VideoLike{}).
		Where("video_id = ?", videoID).
		Count(&count).Error
	return count, err
}

func (r *Repository) CreateFollow(ctx context.Context, followerID, followeeID uint) (bool, error) {
	follow := &Follow{FollowerID: followerID, FolloweeID: followeeID}
	if err := r.db.WithContext(ctx).Create(follow).Error; err != nil {
		if isDuplicateKey(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *Repository) RemoveFollow(ctx context.Context, followerID, followeeID uint) (bool, error) {
	result := r.db.WithContext(ctx).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Delete(&Follow{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (r *Repository) GetFollowState(ctx context.Context, followerID, followeeID uint) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&Follow{}).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Count(&count).Error
	return count > 0, err
}

func (r *Repository) GetFollowerCount(ctx context.Context, followeeID uint) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("user_follows AS follows").
		Joins("JOIN users AS followers ON followers.id = follows.follower_id AND followers.deleted_at IS NULL").
		Where("follows.followee_id = ?", followeeID).
		Count(&count).Error
	return count, err
}

func (r *Repository) GetFollowingCount(ctx context.Context, followerID uint) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("user_follows AS follows").
		Joins("JOIN users AS followees ON followees.id = follows.followee_id AND followees.deleted_at IS NULL").
		Where("follows.follower_id = ?", followerID).
		Count(&count).Error
	return count, err
}

func (r *Repository) CreateComment(ctx context.Context, comment *Comment) error {
	return r.db.WithContext(ctx).Create(comment).Error
}

func (r *Repository) GetComment(ctx context.Context, id uint) (*Comment, error) {
	var comment Comment
	if err := r.db.WithContext(ctx).First(&comment, id).Error; err != nil {
		return nil, err
	}
	return &comment, nil
}

func (r *Repository) RemoveComment(ctx context.Context, id, authorID uint) (bool, error) {
	result := r.db.WithContext(ctx).
		Where("id = ? AND author_id = ?", id, authorID).
		Delete(&Comment{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (r *Repository) GetCommentList(ctx context.Context, videoID uint, cursor *CommentCursor, limit int) ([]CommentItem, error) {
	type commentRow struct {
		ID             uint
		VideoID        uint
		AuthorID       uint
		Content        string
		CreatedAt      time.Time
		AuthorUsername string
		AuthorAvatar   string
		AuthorBio      string
	}

	query := r.db.WithContext(ctx).Table("video_comments AS comments").
		Select(
			"comments.id, comments.video_id, comments.author_id, comments.content, comments.created_at, "+
				"COALESCE(authors.username, ?) AS author_username, "+
				"COALESCE(authors.avatar_url, '') AS author_avatar, "+
				"COALESCE(authors.bio, '') AS author_bio",
			deletedUsername,
		).
		Joins("LEFT JOIN users AS authors ON authors.id = comments.author_id AND authors.deleted_at IS NULL").
		Where("comments.video_id = ? AND comments.deleted_at IS NULL", videoID)
	if cursor != nil {
		query = query.Where(
			"(comments.created_at < ?) OR (comments.created_at = ? AND comments.id < ?)",
			cursor.CreatedAt,
			cursor.CreatedAt,
			cursor.ID,
		)
	}

	var rows []commentRow
	if err := query.Order("comments.created_at DESC, comments.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]CommentItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, CommentItem{
			ID:      row.ID,
			VideoID: row.VideoID,
			Author: PublicUser{
				ID:        row.AuthorID,
				Username:  row.AuthorUsername,
				AvatarURL: row.AuthorAvatar,
				Bio:       row.AuthorBio,
			},
			Content:   row.Content,
			CreatedAt: row.CreatedAt,
		})
	}
	return items, nil
}

func (r *Repository) GetFollowerList(ctx context.Context, followeeID uint, cursor *FollowCursor, limit int) ([]FollowListItem, error) {
	return r.getFollowUserList(ctx, followeeID, cursor, limit, "followee_id", "follower_id")
}

func (r *Repository) GetFollowingList(ctx context.Context, followerID uint, cursor *FollowCursor, limit int) ([]FollowListItem, error) {
	return r.getFollowUserList(ctx, followerID, cursor, limit, "follower_id", "followee_id")
}

func (r *Repository) getFollowUserList(ctx context.Context, targetID uint, cursor *FollowCursor, limit int, targetColumn, accountColumn string) ([]FollowListItem, error) {
	type followRow struct {
		RelationID uint
		CreatedAt  time.Time
		ID         uint
		Username   string
		AvatarURL  string
		Bio        string
	}

	query := r.db.WithContext(ctx).Table("user_follows AS follows").
		Select(
			"follows.id AS relation_id, follows.created_at, accounts.id, accounts.username, accounts.avatar_url, accounts.bio",
		).
		Joins("JOIN users AS accounts ON accounts.id = follows."+accountColumn+" AND accounts.deleted_at IS NULL").
		Where("follows."+targetColumn+" = ?", targetID)
	if cursor != nil {
		query = query.Where(
			"(follows.created_at < ?) OR (follows.created_at = ? AND follows.id < ?)",
			cursor.CreatedAt,
			cursor.CreatedAt,
			cursor.ID,
		)
	}

	var rows []followRow
	if err := query.Order("follows.created_at DESC, follows.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]FollowListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, FollowListItem{
			User: PublicUser{
				ID:        row.ID,
				Username:  row.Username,
				AvatarURL: row.AvatarURL,
				Bio:       row.Bio,
			},
			FollowedAt: row.CreatedAt,
			RelationID: row.RelationID,
		})
	}
	return items, nil
}

func (r *Repository) GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]video.EngagementCounts, error) {
	counts := make(map[uint]video.EngagementCounts, len(videoIDs))
	if len(videoIDs) == 0 {
		return counts, nil
	}
	for _, id := range videoIDs {
		counts[id] = video.EngagementCounts{}
	}

	type countRow struct {
		VideoID uint
		Count   int64
	}
	var likes []countRow
	if err := r.db.WithContext(ctx).Model(&VideoLike{}).
		Select("video_id, COUNT(*) AS count").
		Where("video_id IN ?", videoIDs).
		Group("video_id").
		Scan(&likes).Error; err != nil {
		return nil, err
	}
	for _, row := range likes {
		value := counts[row.VideoID]
		value.LikesCount = row.Count
		counts[row.VideoID] = value
	}

	var comments []countRow
	if err := r.db.WithContext(ctx).Model(&Comment{}).
		Select("video_id, COUNT(*) AS count").
		Where("video_id IN ?", videoIDs).
		Group("video_id").
		Scan(&comments).Error; err != nil {
		return nil, err
	}
	for _, row := range comments {
		value := counts[row.VideoID]
		value.CommentsCount = row.Count
		counts[row.VideoID] = value
	}
	return counts, nil
}

func (r *Repository) GetProfileMetrics(ctx context.Context, accountID uint) (user.ProfileMetrics, error) {
	metrics := user.ProfileMetrics{}
	if accountID == 0 {
		return metrics, nil
	}
	if err := r.db.WithContext(ctx).Table("video_likes AS likes").
		Joins("JOIN videos AS videos ON videos.id = likes.video_id").
		Where("videos.author_id = ? AND videos.status = ? AND videos.deleted_at IS NULL", accountID, video.VideoStatusPublished).
		Count(&metrics.TotalLikes).Error; err != nil {
		return user.ProfileMetrics{}, err
	}
	count, err := r.GetFollowerCount(ctx, accountID)
	if err != nil {
		return user.ProfileMetrics{}, err
	}
	metrics.FollowerCount = count
	count, err = r.GetFollowingCount(ctx, accountID)
	if err != nil {
		return user.ProfileMetrics{}, err
	}
	metrics.VloggerCount = count
	return metrics, nil
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
