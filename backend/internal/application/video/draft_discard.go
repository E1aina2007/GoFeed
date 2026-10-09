package video

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
)

type DraftDiscardService struct {
	discarder domainvideo.DraftDiscarder
}

func NewDraftDiscard(discarder domainvideo.DraftDiscarder) *DraftDiscardService {
	return &DraftDiscardService{discarder: discarder}
}

func (s *DraftDiscardService) DiscardDraft(ctx context.Context, draftID, authorID uint) (domainvideo.DraftItem, error) {
	if draftID == 0 || authorID == 0 {
		return domainvideo.DraftItem{}, domainvideo.ErrInvalidVideoID
	}
	if s.discarder == nil {
		return domainvideo.DraftItem{}, domainvideo.ErrRepositoryUnavailable
	}

	draft, err := s.discarder.UpdateDraftDiscard(ctx, draftID, authorID)
	if err != nil {
		return domainvideo.DraftItem{}, err
	}
	return domainvideo.DraftItemFrom(*draft), nil
}
