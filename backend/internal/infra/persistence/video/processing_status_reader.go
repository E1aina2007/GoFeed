package infravideo

import (
	"context"
	"errors"

	domainvideo "gofeed/internal/domain/video"
	legacyvideo "gofeed/internal/video"

	"gorm.io/gorm"
)

type processingStatusSource interface {
	GetByID(ctx context.Context, id uint) (*legacyvideo.Video, error)
}

type processingStatusReader struct {
	videos processingStatusSource
}

func NewProcessingStatusReader(videos processingStatusSource) domainvideo.ProcessingStatusReader {
	if videos == nil {
		return nil
	}
	return &processingStatusReader{videos: videos}
}

func (r *processingStatusReader) GetProcessingSnapshot(ctx context.Context, id uint) (*domainvideo.ProcessingSnapshot, error) {
	row, err := r.videos.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainvideo.ErrVideoNotFound
		}
		return nil, readError(err)
	}
	if row == nil {
		return nil, nil
	}
	return &domainvideo.ProcessingSnapshot{
		AuthorID:       row.AuthorID,
		Status:         row.Status,
		PublishedAt:    row.PublishedAt,
		RejectedAt:     row.RejectedAt,
		RejectedReason: row.RejectedReason,
	}, nil
}
