package video

import (
	"context"
	"io"

	domainvideo "gofeed/internal/domain/video"
)

type DraftMediaUploadService struct {
	storage domainvideo.MediaStorage
	binder  domainvideo.DraftMediaBinder
}

func NewDraftMediaUpload(storage domainvideo.MediaStorage, binder domainvideo.DraftMediaBinder) *DraftMediaUploadService {
	return &DraftMediaUploadService{storage: storage, binder: binder}
}

// UploadDraftMedia 保留保存后绑定及绑定失败时的可选删除顺序
func (s *DraftMediaUploadService) UploadDraftMedia(ctx context.Context, draftID, ownerID uint, kind domainvideo.MediaKind, filename string, src io.Reader) (domainvideo.DraftMediaUpload, error) {
	saved, err := s.storage.Save(ctx, ownerID, kind, filename, src)
	if err != nil {
		return domainvideo.DraftMediaUpload{}, err
	}
	originalName := domainvideo.OriginalName(filename)
	if err := s.bindDraftMedia(ctx, draftID, ownerID, kind, saved, originalName); err != nil {
		if remover, ok := s.storage.(domainvideo.MediaRemover); ok {
			_ = remover.Remove(ctx, saved.PublicURL)
		}
		return domainvideo.DraftMediaUpload{}, err
	}
	return domainvideo.DraftMediaUpload{SavedFile: saved, OriginalName: originalName}, nil
}

func (s *DraftMediaUploadService) bindDraftMedia(ctx context.Context, draftID, ownerID uint, kind domainvideo.MediaKind, saved domainvideo.SavedFile, originalName string) error {
	if !domainvideo.IsValidDraftMedia(draftID, ownerID, kind, saved.PublicURL, saved.FileName) {
		return domainvideo.ErrInvalidMedia
	}
	if s.binder == nil {
		return domainvideo.ErrRepositoryUnavailable
	}
	if originalName == "" {
		originalName = saved.FileName
	}
	return s.binder.UpdateDraftMedia(ctx, draftID, ownerID, kind, saved, originalName)
}
