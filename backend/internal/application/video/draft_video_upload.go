package video

import (
	"context"
	"io"

	domainvideo "gofeed/internal/domain/video"
)

type DraftVideoUploadService struct {
	storage domainvideo.MediaStorage
	binder  domainvideo.DraftMediaBinder
}

func NewDraftVideoUpload(storage domainvideo.MediaStorage, binder domainvideo.DraftMediaBinder) *DraftVideoUploadService {
	return &DraftVideoUploadService{storage: storage, binder: binder}
}

// UploadDraftVideo 保留保存后绑定及绑定失败时的可选删除顺序
func (s *DraftVideoUploadService) UploadDraftVideo(ctx context.Context, draftID, ownerID uint, filename string, src io.Reader) (domainvideo.DraftMediaUpload, error) {
	saved, err := s.storage.Save(ctx, ownerID, domainvideo.MediaVideo, filename, src)
	if err != nil {
		return domainvideo.DraftMediaUpload{}, err
	}
	originalName := domainvideo.OriginalName(filename)
	if err := s.bindDraftVideo(ctx, draftID, ownerID, saved, originalName); err != nil {
		if remover, ok := s.storage.(domainvideo.MediaRemover); ok {
			_ = remover.Remove(ctx, saved.PublicURL)
		}
		return domainvideo.DraftMediaUpload{}, err
	}
	return domainvideo.DraftMediaUpload{SavedFile: saved, OriginalName: originalName}, nil
}

func (s *DraftVideoUploadService) bindDraftVideo(ctx context.Context, draftID, ownerID uint, saved domainvideo.SavedFile, originalName string) error {
	if !domainvideo.IsValidDraftMedia(draftID, ownerID, domainvideo.MediaVideo, saved.PublicURL, saved.FileName) {
		return domainvideo.ErrInvalidMedia
	}
	if s.binder == nil {
		return domainvideo.ErrRepositoryUnavailable
	}
	if originalName == "" {
		originalName = saved.FileName
	}
	return s.binder.UpdateDraftMedia(ctx, draftID, ownerID, domainvideo.MediaVideo, saved, originalName)
}
