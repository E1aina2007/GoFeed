package video

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
)

type PublishedDeletionService struct {
	deleter domainvideo.PublishedVideoDeleter
}

func NewPublishedDeletion(deleter domainvideo.PublishedVideoDeleter) *PublishedDeletionService {
	return &PublishedDeletionService{deleter: deleter}
}

func (s *PublishedDeletionService) DeleteVideo(ctx context.Context, id, authorID uint) error {
	if id == 0 {
		return domainvideo.ErrInvalidVideoID
	}
	if s.deleter == nil {
		return domainvideo.ErrRepositoryUnavailable
	}

	video, err := s.deleter.GetDeletionSnapshot(ctx, id)
	if err != nil {
		return err
	}
	if video.AuthorID != authorID {
		return domainvideo.ErrNotAuthor
	}
	if video.Status != domainvideo.StatusPublished {
		return domainvideo.ErrVideoNotFound
	}
	if err := s.deleter.DeletePublishedVideo(ctx, id, authorID); err != nil {
		return err
	}
	return nil
}
