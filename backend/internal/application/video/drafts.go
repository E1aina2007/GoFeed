package video

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
)

type DraftService struct {
	creator domainvideo.DraftCreator
	reader  domainvideo.DraftReader
}

func NewDrafts(creator domainvideo.DraftCreator, reader domainvideo.DraftReader) *DraftService {
	return &DraftService{creator: creator, reader: reader}
}

func (s *DraftService) CreateDraft(ctx context.Context, authorID uint, req domainvideo.DraftInput) (domainvideo.DraftItem, error) {
	if authorID == 0 {
		return domainvideo.DraftItem{}, domainvideo.ErrInvalidVideoID
	}
	if s.creator == nil {
		return domainvideo.DraftItem{}, domainvideo.ErrRepositoryUnavailable
	}

	req, err := domainvideo.NormalizeDraft(req)
	if err != nil {
		return domainvideo.DraftItem{}, err
	}
	draft := &domainvideo.DraftSnapshot{
		AuthorID:    authorID,
		Title:       req.Title,
		Description: req.Description,
		Status:      domainvideo.StatusDraft,
	}
	if err := s.creator.CreateDraft(ctx, draft); err != nil {
		return domainvideo.DraftItem{}, err
	}
	return domainvideo.DraftItemFrom(*draft), nil
}

func (s *DraftService) GetDraft(ctx context.Context, draftID, authorID uint) (domainvideo.DraftItem, error) {
	if draftID == 0 || authorID == 0 {
		return domainvideo.DraftItem{}, domainvideo.ErrInvalidVideoID
	}
	if s.reader == nil {
		return domainvideo.DraftItem{}, domainvideo.ErrRepositoryUnavailable
	}

	draft, err := s.reader.GetDraftSnapshot(ctx, draftID)
	if err != nil {
		return domainvideo.DraftItem{}, err
	}
	if draft.AuthorID != authorID {
		return domainvideo.DraftItem{}, domainvideo.ErrNotAuthor
	}
	if draft.Status != domainvideo.StatusDraft && draft.Status != domainvideo.StatusPurging {
		return domainvideo.DraftItem{}, domainvideo.ErrVideoNotFound
	}
	return domainvideo.DraftItemFrom(*draft), nil
}
