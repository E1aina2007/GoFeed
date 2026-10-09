package infravideo

import (
	"context"
	"errors"

	domainvideo "gofeed/internal/domain/video"
	legacyvideo "gofeed/internal/video"

	"gorm.io/gorm"
)

type draftDiscardSource interface {
	UpdateDraftDiscard(ctx context.Context, draftID, authorID uint) (*legacyvideo.Video, error)
}

type draftDiscarder struct {
	videos draftDiscardSource
}

func NewDraftDiscarder(videos draftDiscardSource) domainvideo.DraftDiscarder {
	if videos == nil {
		return nil
	}
	return &draftDiscarder{videos: videos}
}

func (r *draftDiscarder) UpdateDraftDiscard(ctx context.Context, draftID, authorID uint) (*domainvideo.DraftSnapshot, error) {
	row, err := r.videos.UpdateDraftDiscard(ctx, draftID, authorID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainvideo.ErrVideoNotFound
		}
		return nil, readError(err)
	}
	draft := draftSnapshot(*row)
	return &draft, nil
}
