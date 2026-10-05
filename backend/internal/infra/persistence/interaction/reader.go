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
