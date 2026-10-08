package video

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
)

func NewMyVideoList(repository domainvideo.AuthorVideoListReader, authorReader domainvideo.AuthorReader, engagementReaders ...domainvideo.EngagementReader) *Service {
	service := New(nil, authorReader, engagementReaders...)
	service.authorVideos = repository
	return service
}

func (s *Service) GetMyVideoList(ctx context.Context, authorID uint, encodedCursor string, limit int) (ListResult, error) {
	if authorID == 0 {
		return ListResult{}, domainvideo.ErrInvalidVideoID
	}
	if s.authorVideos == nil {
		return ListResult{}, domainvideo.ErrRepositoryUnavailable
	}

	limit, err := normalizeLimit(limit)
	if err != nil {
		return ListResult{}, err
	}
	scope := mineCursorScope(authorID)
	cursor, err := decodeCursor(encodedCursor)
	if err != nil {
		return ListResult{}, err
	}
	if err := validateCursorScope(cursor, scope); err != nil {
		return ListResult{}, err
	}

	var position *domainvideo.ListPosition
	if cursor != nil {
		position = &domainvideo.ListPosition{PublishedAt: cursor.PublishedAt, ID: cursor.ID}
	}
	videos, err := s.authorVideos.GetAuthorVideoList(ctx, authorID, position, limit+1)
	if err != nil {
		return ListResult{}, err
	}
	return s.buildListResponse(ctx, videos, limit, scope)
}
