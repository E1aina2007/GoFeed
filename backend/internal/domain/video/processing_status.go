package video

import (
	"context"
	"time"
)

type ProcessingSnapshot struct {
	AuthorID       uint
	Status         string
	PublishedAt    *time.Time
	RejectedAt     *time.Time
	RejectedReason string
}

type ProcessingStatus struct {
	Status         string
	PublishedAt    *time.Time
	RejectedAt     *time.Time
	RejectedReason string
}

type ProcessingStatusReader interface {
	GetProcessingSnapshot(ctx context.Context, id uint) (*ProcessingSnapshot, error)
}
