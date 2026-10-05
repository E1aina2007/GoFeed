package domaininteraction

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidDispatch    = errors.New("invalid interaction outbox dispatch")
	ErrInvalidOutboxLease = errors.New("invalid interaction outbox lease")
)

type OutboxDispatch struct {
	ID             uint
	Attempt        int
	LeaseTakenOver bool
	Event          ChangedEvent
}

// OutboxStore 以租约和尝试次数围栏管理派发状态，保留原始互动事实
type OutboxStore interface {
	ClaimPendingEvents(ctx context.Context, limit int, lease time.Duration) ([]OutboxDispatch, error)
	MarkDispatched(ctx context.Context, id uint, attempt int) (bool, error)
	ReleaseRetry(ctx context.Context, id uint, attempt int, backoff time.Duration, reason string) (bool, error)
}
