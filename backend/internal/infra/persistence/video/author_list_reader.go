package infravideo

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
)

type authorVideoListSource interface {
	GetAuthorVideoList(ctx context.Context, authorID uint, cursor *domainvideo.ListPosition, limit int) ([]Video, error)
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
	var cursor *domainvideo.ListPosition
	if position != nil {
		cursor = &domainvideo.ListPosition{PublishedAt: position.PublishedAt, ID: position.ID}
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
