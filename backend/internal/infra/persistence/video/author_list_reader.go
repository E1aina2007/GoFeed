package infravideo

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
	legacyvideo "gofeed/internal/video"
)

type authorVideoListSource interface {
	GetAuthorVideoList(ctx context.Context, authorID uint, cursor *legacyvideo.Cursor, limit int) ([]legacyvideo.Video, error)
}

type authorVideoListReader struct {
	videos authorVideoListSource
}

func NewAuthorVideoListReader(videos authorVideoListSource) domainvideo.AuthorVideoListReader {
	if videos == nil {
		return nil
	}
	return &authorVideoListReader{videos: videos}
}

func (r *authorVideoListReader) GetAuthorVideoList(ctx context.Context, authorID uint, position *domainvideo.ListPosition, limit int) ([]domainvideo.PublicVideo, error) {
	var cursor *legacyvideo.Cursor
	if position != nil {
		cursor = &legacyvideo.Cursor{PublishedAt: position.PublishedAt, ID: position.ID}
	}
	rows, err := r.videos.GetAuthorVideoList(ctx, authorID, cursor, limit)
	if err != nil {
		return nil, readError(err)
	}
	items := make([]domainvideo.PublicVideo, 0, len(rows))
	for _, row := range rows {
		items = append(items, publicVideo(row))
	}
	return items, nil
}
