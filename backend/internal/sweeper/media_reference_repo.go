package sweeper

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var ErrMediaReferenceRepositoryUnavailable = errors.New("media reference repository unavailable")

// MediaReferenceRepository 从媒体事实来源读取全部仍被引用的对象地址
// 查询不应用软删除作用域：保留期内的用户和视频仍拥有其媒体对象
type MediaReferenceRepository struct {
	db *gorm.DB
}

func NewMediaReferenceRepository(db *gorm.DB) *MediaReferenceRepository {
	return &MediaReferenceRepository{db: db}
}

func (r *MediaReferenceRepository) ListReferencedMediaURLs(ctx context.Context) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, ErrMediaReferenceRepositoryUnavailable
	}

	type referenceRow struct {
		URL string `gorm:"column:url"`
	}
	var rows []referenceRow
	const query = `
SELECT play_url AS url FROM videos WHERE play_url <> ''
UNION
SELECT cover_url AS url FROM videos WHERE cover_url <> ''
UNION
SELECT avatar_url AS url FROM users WHERE avatar_url <> ''`
	if err := r.db.WithContext(ctx).Raw(query).Scan(&rows).Error; err != nil {
		return nil, err
	}

	urls := make([]string, 0, len(rows))
	for _, row := range rows {
		urls = append(urls, row.URL)
	}
	return urls, nil
}
