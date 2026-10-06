package infrainteraction

import (
	"context"
	"errors"
	"time"

	domaininteraction "gofeed/internal/domain/interaction"
	"gofeed/internal/video"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const deletedUsername = "已注销用户"

// RequireActiveUser 读取活动账户，并在外层转换不存在结果
func (r *Repository) RequireActiveUser(ctx context.Context, userID uint) error {
	if !r.available() {
		return domaininteraction.ErrUnavailable
	}
	if userID == 0 {
		return domaininteraction.ErrUserNotFound
	}
	var count int64
	if err := r.db.WithContext(ctx).Table("users").
		Where("id = ? AND deleted_at IS NULL", userID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return domaininteraction.ErrUserNotFound
	}
	return nil
}

func (r *Repository) RequirePublicVideo(ctx context.Context, videoID uint) error {
	if !r.available() {
		return domaininteraction.ErrUnavailable
	}
	if videoID == 0 {
		return domaininteraction.ErrVideoNotFound
	}
	var count int64
	if err := video.PublicVideoQuery(r.db.WithContext(ctx)).
		Where(clause.Eq{Column: clause.PrimaryColumn, Value: videoID}).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return domaininteraction.ErrVideoNotFound
	}
	return nil
}

func (r *Repository) GetLikeCount(ctx context.Context, videoID uint) (int64, error) {
	if !r.available() {
		return 0, domaininteraction.ErrUnavailable
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&VideoLike{}).
		Where("video_id = ?", videoID).
		Count(&count).Error
	return count, err
}

func (r *Repository) GetLikeState(ctx context.Context, videoID, userID uint) (bool, error) {
	if !r.available() {
		return false, domaininteraction.ErrUnavailable
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&VideoLike{}).
		Where("video_id = ? AND user_id = ?", videoID, userID).
		Count(&count).Error
	return count > 0, err
}

// GetCommentList 用一次作者 JOIN 读取倒序评论，位置由 Application 的 v1 游标解析
func (r *Repository) GetCommentList(ctx context.Context, videoID uint, position *domaininteraction.CommentPosition, limit int) ([]domaininteraction.CommentWithAuthor, error) {
	if !r.available() {
		return nil, domaininteraction.ErrUnavailable
	}
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
	if position != nil {
		query = query.Where(
			"(comments.created_at < ?) OR (comments.created_at = ? AND comments.id < ?)",
			position.CreatedAt,
			position.CreatedAt,
			position.ID,
		)
	}
	var rows []commentRow
	if err := query.Order("comments.created_at DESC, comments.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]domaininteraction.CommentWithAuthor, 0, len(rows))
	for _, row := range rows {
		items = append(items, domaininteraction.CommentWithAuthor{
			Comment: domaininteraction.Comment{ID: row.ID, VideoID: row.VideoID, AuthorID: row.AuthorID, Content: row.Content, CreatedAt: row.CreatedAt},
			Author:  domaininteraction.Author{ID: row.AuthorID, Username: row.AuthorUsername, AvatarURL: row.AuthorAvatar, Bio: row.AuthorBio},
		})
	}
	return items, nil
}

// GetAuthor 只读取活动作者的展示字段
func (r *Repository) GetAuthor(ctx context.Context, userID uint) (domaininteraction.Author, error) {
	if !r.available() {
		return domaininteraction.Author{}, domaininteraction.ErrUnavailable
	}
	var row struct {
		ID        uint
		Username  string
		AvatarURL string
		Bio       string
	}
	err := r.db.WithContext(ctx).Table("users").
		Select("id, username, avatar_url, bio").
		Where("id = ? AND deleted_at IS NULL", userID).
		Take(&row).Error
	if err != nil {
		return domaininteraction.Author{}, notFoundAs(err, domaininteraction.ErrUserNotFound)
	}
	return domaininteraction.Author{
		ID:        row.ID,
		Username:  row.Username,
		AvatarURL: row.AvatarURL,
		Bio:       row.Bio,
	}, nil
}

func notFoundAs(err, missing error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return missing
	}
	return err
}
