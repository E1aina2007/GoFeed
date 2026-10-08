package infravideo

import (
	"context"
	"errors"

	domainvideo "gofeed/internal/domain/video"
	legacyvideo "gofeed/internal/video"

	"gorm.io/gorm"
)

type draftCreateSource interface {
	Create(ctx context.Context, video *legacyvideo.Video) error
}

type draftReadSource interface {
	GetByID(ctx context.Context, id uint) (*legacyvideo.Video, error)
}

type draftCreator struct {
	videos draftCreateSource
}

type draftReader struct {
	videos draftReadSource
}

func NewDraftCreator(videos draftCreateSource) domainvideo.DraftCreator {
	if videos == nil {
		return nil
	}
	return &draftCreator{videos: videos}
}

func NewDraftReader(videos draftReadSource) domainvideo.DraftReader {
	if videos == nil {
		return nil
	}
	return &draftReader{videos: videos}
}

func (r *draftCreator) CreateDraft(ctx context.Context, draft *domainvideo.DraftSnapshot) error {
	row := &legacyvideo.Video{
		AuthorID:    draft.AuthorID,
		Title:       draft.Title,
		Description: draft.Description,
		Status:      draft.Status,
	}
	if err := r.videos.Create(ctx, row); err != nil {
		return readError(err)
	}
	*draft = draftSnapshot(*row)
	return nil
}

func (r *draftReader) GetDraftSnapshot(ctx context.Context, id uint) (*domainvideo.DraftSnapshot, error) {
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
	draft := draftSnapshot(*row)
	return &draft, nil
}

func draftSnapshot(row legacyvideo.Video) domainvideo.DraftSnapshot {
	return domainvideo.DraftSnapshot{
		ID:                row.ID,
		AuthorID:          row.AuthorID,
		Title:             row.Title,
		Description:       row.Description,
		Status:            row.Status,
		PlayURL:           row.PlayURL,
		PlayFileName:      row.PlayFileName,
		PlayOriginalName:  row.PlayOriginalName,
		CoverURL:          row.CoverURL,
		CoverFileName:     row.CoverFileName,
		CoverOriginalName: row.CoverOriginalName,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}
