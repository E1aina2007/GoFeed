package video

import (
	"context"

	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// 查询已发布视频详情
func (r *Repository) GetPublishedByID(ctx context.Context, id uint) (*Video, error) {
	if id == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	var video Video
	err := r.db.WithContext(ctx).
		Where("status = ?", VideoStatusPublished).
		First(&video, id).Error
	if err != nil {
		return nil, err
	}
	return &video, nil
}

// 按发布时间查询已发布视频
func (r *Repository) ListPublished(ctx context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error) {
	if limit <= 0 {
		return []Video{}, nil
	}

	query := r.db.WithContext(ctx).Where("status = ?", VideoStatusPublished)
	if authorID != 0 {
		query = query.Where("author_id = ?", authorID)
	}
	if cursor != nil {
		query = query.Where(
			"(published_at < ?) OR (published_at = ? AND id < ?)",
			cursor.PublishedAt,
			cursor.PublishedAt,
			cursor.ID,
		)
	}

	var videos []Video
	if err := query.Order("published_at DESC, id DESC").Limit(limit).Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}
