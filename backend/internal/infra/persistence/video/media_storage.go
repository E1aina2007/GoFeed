package infravideo

import (
	"context"
	"errors"
	"time"

	domainvideo "gofeed/internal/domain/video"
	inframedia "gofeed/internal/infra/storage/media"
	legacyvideo "gofeed/internal/video"
)

type mediaRemover struct {
	remover domainvideo.MediaRemover
}

type mediaCandidateLister struct {
	candidates domainvideo.MediaCandidateLister
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
