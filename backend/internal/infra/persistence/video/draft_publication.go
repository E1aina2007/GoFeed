package infravideo

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
)

type draftPublishSource interface {
	UpdateDraftPublication(ctx context.Context, draftID, authorID uint) (*Video, error)
}

type draftPublisher struct {
	videos draftPublishSource
}

func NewDraftPublisher(videos draftPublishSource) domainvideo.DraftPublisher {
	if videos == nil {
		return nil
	}
	return &draftPublisher{videos: videos}
}

func (r *draftPublisher) UpdateDraftPublication(ctx context.Context, draftID, authorID uint) (*domainvideo.DraftSnapshot, error) {
	row, err := r.videos.UpdateDraftPublication(ctx, draftID, authorID)
	if err != nil {
		return nil, readError(err)
	}
	draft := draftSnapshot(*row)
	return &draft, nil
}
