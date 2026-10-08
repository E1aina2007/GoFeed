package infravideo

import (
	"context"
	"errors"
	"io"
	"time"

	domainvideo "gofeed/internal/domain/video"
	inframedia "gofeed/internal/infra/storage/media"
	legacyvideo "gofeed/internal/video"
)

type mediaStorage struct {
	storage domainvideo.MediaStorage
}

type removableMediaStorage struct {
	*mediaStorage
	legacyvideo.MediaRemover
}

type mediaRemover struct {
	remover domainvideo.MediaRemover
}

type mediaCandidateLister struct {
	candidates domainvideo.MediaCandidateLister
}

// NewMediaStorage 只在原存储提供删除能力时保留上传失败后的可选清理
func NewMediaStorage(storage domainvideo.MediaStorage) legacyvideo.MediaStorage {
	if storage == nil {
		return nil
	}
	adapter := &mediaStorage{storage: storage}
	if remover, ok := storage.(domainvideo.MediaRemover); ok {
		return &removableMediaStorage{mediaStorage: adapter, MediaRemover: NewMediaRemover(remover)}
	}
	return adapter
}

func (s *mediaStorage) Save(ctx context.Context, ownerID uint, kind legacyvideo.MediaKind, filename string, src io.Reader) (legacyvideo.SavedFile, error) {
	saved, err := s.storage.Save(ctx, ownerID, domainvideo.MediaKind(kind), filename, src)
	return legacyvideo.SavedFile{PublicURL: saved.PublicURL, FileName: saved.FileName}, mediaError(err)
}

func NewMediaRemover(remover domainvideo.MediaRemover) legacyvideo.MediaRemover {
	if remover == nil {
		return nil
	}
	return &mediaRemover{remover: remover}
}

func (s *mediaRemover) Remove(ctx context.Context, publicURL string) error {
	return mediaError(s.remover.Remove(ctx, publicURL))
}

func NewMediaCandidateLister(candidates domainvideo.MediaCandidateLister) legacyvideo.MediaCandidateLister {
	if candidates == nil {
		return nil
	}
	return &mediaCandidateLister{candidates: candidates}
}

func (s *mediaCandidateLister) ListMediaCandidates(ctx context.Context, cutoff time.Time, limit int) ([]string, error) {
	candidates, err := s.candidates.ListMediaCandidates(ctx, cutoff, limit)
	return candidates, mediaError(err)
}

func ValidatePublishedMedia(root, playURL, coverURL string) error {
	return mediaError(inframedia.ValidatePublishedMedia(root, playURL, coverURL))
}

// mediaError 保留旧消费者的错误身份、原文案与新存储的完整错误链
func mediaError(err error) error {
	var kinds []error
	for _, pair := range [][2]error{
		{domainvideo.ErrInvalidMedia, legacyvideo.ErrInvalidMedia},
		{domainvideo.ErrMediaTooLarge, legacyvideo.ErrMediaTooLarge},
		{domainvideo.ErrInvalidMediaURL, legacyvideo.ErrInvalidMediaURL},
		{domainvideo.ErrInvalidMediaPath, legacyvideo.ErrInvalidMediaPath},
	} {
		if errors.Is(err, pair[0]) {
			kinds = append(kinds, pair[1])
		}
	}
	if len(kinds) == 0 {
		return err
	}
	return mediaStorageError{cause: err, chain: append(kinds, err)}
}

type mediaStorageError struct {
	cause error
	chain []error
}

func (e mediaStorageError) Error() string   { return e.cause.Error() }
func (e mediaStorageError) Unwrap() []error { return e.chain }
