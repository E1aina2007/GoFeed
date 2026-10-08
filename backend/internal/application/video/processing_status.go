package video

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
)

type ProcessingStatusService struct {
	reader domainvideo.ProcessingStatusReader
}

func NewProcessingStatus(reader domainvideo.ProcessingStatusReader) *ProcessingStatusService {
	return &ProcessingStatusService{reader: reader}
}

func (s *ProcessingStatusService) GetVideoStatus(ctx context.Context, videoID, viewerID uint) (domainvideo.ProcessingStatus, error) {
	if videoID == 0 || viewerID == 0 {
		return domainvideo.ProcessingStatus{}, domainvideo.ErrInvalidVideoID
	}
	if s.reader == nil {
		return domainvideo.ProcessingStatus{}, domainvideo.ErrRepositoryUnavailable
	}

	row, err := s.reader.GetProcessingSnapshot(ctx, videoID)
	if err != nil {
		return domainvideo.ProcessingStatus{}, err
	}
	if row.AuthorID != viewerID {
		return domainvideo.ProcessingStatus{}, domainvideo.ErrVideoNotFound
	}
	switch row.Status {
	case domainvideo.StatusProcessing, domainvideo.StatusPublished, domainvideo.StatusRejected:
		return domainvideo.ProcessingStatus{
			Status:         row.Status,
			PublishedAt:    row.PublishedAt,
			RejectedAt:     row.RejectedAt,
			RejectedReason: row.RejectedReason,
		}, nil
	default:
		return domainvideo.ProcessingStatus{}, domainvideo.ErrVideoNotFound
	}
}
