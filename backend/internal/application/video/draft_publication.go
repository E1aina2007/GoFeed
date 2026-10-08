package video

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
)

type DraftPublicationService struct {
	publisher domainvideo.DraftPublisher
}

func NewDraftPublication(publisher domainvideo.DraftPublisher) *DraftPublicationService {
	return &DraftPublicationService{publisher: publisher}
}

func (s *DraftPublicationService) UpdateDraftPublication(ctx context.Context, draftID, authorID uint) (domainvideo.DraftItem, error) {
	if draftID == 0 || authorID == 0 {
		return domainvideo.DraftItem{}, domainvideo.ErrInvalidVideoID
	}
	if s.publisher == nil {
		return domainvideo.DraftItem{}, domainvideo.ErrRepositoryUnavailable
	}

	draft, err := s.publisher.UpdateDraftPublication(ctx, draftID, authorID)
	if err != nil {
		return domainvideo.DraftItem{}, err
	}
	return domainvideo.DraftItemFrom(*draft), nil
}
