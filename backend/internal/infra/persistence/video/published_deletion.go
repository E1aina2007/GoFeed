package infravideo

import (
	"context"
	"errors"

	domainvideo "gofeed/internal/domain/video"
	legacyvideo "gofeed/internal/video"

	"gorm.io/gorm"
)

type publishedDeletionSource interface {
	GetByID(ctx context.Context, id uint) (*legacyvideo.Video, error)
	DeletePublishedVideo(ctx context.Context, id, authorID uint) error
}

type publishedVideoDeleter struct {
	videos publishedDeletionSource
}

func NewPublishedVideoDeleter(videos publishedDeletionSource) domainvideo.PublishedVideoDeleter {
	if videos == nil {
		return nil
	}
	return &publishedVideoDeleter{videos: videos}
}

func (r *publishedVideoDeleter) GetDeletionSnapshot(ctx context.Context, id uint) (*domainvideo.DeletionSnapshot, error) {
	row, err := r.videos.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainvideo.ErrVideoNotFound
		}
		return nil, readError(err)
	}
	return &domainvideo.DeletionSnapshot{AuthorID: row.AuthorID, Status: row.Status}, nil
}

func (r *publishedVideoDeleter) DeletePublishedVideo(ctx context.Context, id, authorID uint) error {
	if err := r.videos.DeletePublishedVideo(ctx, id, authorID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainvideo.ErrVideoNotFound
		}
		return readError(err)
	}
	return nil
}
