package infravideo

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
	legacyvideo "gofeed/internal/video"
)

type draftMediaBindSource interface {
	UpdateDraftMedia(ctx context.Context, draftID, authorID uint, kind legacyvideo.MediaKind, saved legacyvideo.SavedFile, originalName string) error
}

type draftMediaBinder struct {
	videos draftMediaBindSource
}

func NewDraftMediaBinder(videos draftMediaBindSource) domainvideo.DraftMediaBinder {
	if videos == nil {
		return nil
	}
	return &draftMediaBinder{videos: videos}
}

func (r *draftMediaBinder) UpdateDraftMedia(ctx context.Context, draftID, authorID uint, kind domainvideo.MediaKind, saved domainvideo.SavedFile, originalName string) error {
	return readError(r.videos.UpdateDraftMedia(ctx, draftID, authorID, legacyvideo.MediaKind(kind),
		legacyvideo.SavedFile{PublicURL: saved.PublicURL, FileName: saved.FileName}, originalName))
}
