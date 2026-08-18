package video

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create 写入一条视频记录
func (r *Repository) Create(ctx context.Context, video *Video) error {
	return r.db.WithContext(ctx).Create(video).Error
}

// GetByID 查询任意状态的视频（GORM 自动过滤已软删除记录）
func (r *Repository) GetByID(ctx context.Context, id uint) (*Video, error) {
	if id == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	var video Video
	if err := r.db.WithContext(ctx).First(&video, id).Error; err != nil {
		return nil, err
	}
	return &video, nil
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

// ListByAuthor 按作者查询视频（不限制状态，用于作者自己的管理列表）
func (r *Repository) ListByAuthor(ctx context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error) {
	if authorID == 0 || limit <= 0 {
		return []Video{}, nil
	}

	query := r.db.WithContext(ctx).Where("author_id = ?", authorID)
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

// Delete 软删除视频
func (r *Repository) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return gorm.ErrRecordNotFound
	}
	result := r.db.WithContext(ctx).Delete(&Video{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ListExpiredDeleted 返回已软删除且宽限期届满的视频，供 sweeper 在删除媒体文件前读取。
func (r *Repository) ListExpiredDeleted(ctx context.Context, cutoff time.Time) ([]Video, error) {
	var videos []Video
	if err := r.db.WithContext(ctx).Unscoped().
		Where("deleted_at IS NOT NULL AND deleted_at <= ?", cutoff).
		Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

// HardDeleteExpired 删除指定的、仍处于到期软删除状态的视频记录。
// 返回 false 表示该记录已被其他清扫任务处理或不再符合清扫条件。
func (r *Repository) HardDeleteExpired(ctx context.Context, id uint, cutoff time.Time) (bool, error) {
	if id == 0 {
		return false, nil
	}
	result := r.db.WithContext(ctx).Unscoped().
		Where("id = ? AND deleted_at IS NOT NULL AND deleted_at <= ?", id, cutoff).
		Delete(&Video{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}
