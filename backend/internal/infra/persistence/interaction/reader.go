package infrainteraction

import (
	"context"
	"errors"

	domaininteraction "gofeed/internal/domain/interaction"
	"gofeed/internal/social"

	"gorm.io/gorm"
)

// RequireActiveUser 适配现有活动账户查询，并在外层转换数据库错误
func (r *Repository) RequireActiveUser(ctx context.Context, userID uint) error {
	if !r.available() {
		return domaininteraction.ErrUnavailable
	}
	return notFoundAs(social.NewRepository(r.db).GetActiveUser(ctx, userID), domaininteraction.ErrUserNotFound)
}

func (r *Repository) RequirePublicVideo(ctx context.Context, videoID uint) error {
	if !r.available() {
		return domaininteraction.ErrUnavailable
	}
	return notFoundAs(social.NewRepository(r.db).GetPublishedVideo(ctx, videoID), domaininteraction.ErrVideoNotFound)
}

func (r *Repository) GetLikeCount(ctx context.Context, videoID uint) (int64, error) {
	if !r.available() {
		return 0, domaininteraction.ErrUnavailable
	}
	return social.NewRepository(r.db).GetLikeCount(ctx, videoID)
}

func (r *Repository) GetLikeState(ctx context.Context, videoID, userID uint) (bool, error) {
	if !r.available() {
		return false, domaininteraction.ErrUnavailable
	}
	return social.NewRepository(r.db).GetLikeState(ctx, videoID, userID)
}

// GetCommentList 仅在适配层转换旧游标与行模型，复用带作者 JOIN 的批量查询
func (r *Repository) GetCommentList(ctx context.Context, videoID uint, position *domaininteraction.CommentPosition, limit int) ([]domaininteraction.CommentWithAuthor, error) {
	if !r.available() {
		return nil, domaininteraction.ErrUnavailable
	}
	var cursor *social.CommentCursor
	if position != nil {
		cursor = &social.CommentCursor{Version: 1, Kind: social.CursorKindComments, VideoID: videoID, CreatedAt: position.CreatedAt, ID: position.ID}
	}
	rows, err := social.NewRepository(r.db).GetCommentList(ctx, videoID, cursor, limit)
	if err != nil {
		return nil, err
	}
	items := make([]domaininteraction.CommentWithAuthor, 0, len(rows))
	for _, row := range rows {
		items = append(items, domaininteraction.CommentWithAuthor{
			Comment: domaininteraction.Comment{ID: row.ID, VideoID: row.VideoID, AuthorID: row.Author.ID, Content: row.Content, CreatedAt: row.CreatedAt},
			Author:  domaininteraction.Author{ID: row.Author.ID, Username: row.Author.Username, AvatarURL: row.Author.AvatarURL, Bio: row.Author.Bio},
		})
	}
	return items, nil
}

// GetAuthor 将既有用户展示结果转换为独立的领域读模型
func (r *Repository) GetAuthor(ctx context.Context, userID uint) (domaininteraction.Author, error) {
	if !r.available() {
		return domaininteraction.Author{}, domaininteraction.ErrUnavailable
	}
	row, err := social.NewRepository(r.db).GetPublicUser(ctx, userID)
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
