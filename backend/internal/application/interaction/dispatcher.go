package applicationinteraction

import (
	"context"
	"errors"
	"time"

	domaininteraction "gofeed/internal/domain/interaction"
)

const (
	dispatchBatchSize      = 32
	dispatchLease          = 30 * time.Second
	dispatchTimeout        = 5 * time.Second
	dispatchMaxBackoff     = 5 * time.Minute
	dispatchInvalidBackoff = 5 * time.Minute
	DispatchConfirmed      = "dispatched"
	DispatchRetryScheduled = "retry_scheduled"
	DispatchLeaseLost      = "lease_lost"
	DispatchMarkFailed     = "mark_failed"
	DispatchReleaseFailed  = "release_failed"
)

type ChangedEventPublisher interface {
	PublishChanged(ctx context.Context, event domaininteraction.ChangedEvent) error
}

type DispatchResult struct {
	EventID        string
	VideoID        uint
	Attempt        int
	LeaseTakenOver bool
	Outcome        string
	Err            error
}

type Dispatcher struct {
	store     domaininteraction.OutboxStore
	publisher ChangedEventPublisher
}

func NewDispatcher(store domaininteraction.OutboxStore, publisher ChangedEventPublisher) (*Dispatcher, error) {
	if store == nil || publisher == nil {
		return nil, errors.New("interaction dispatcher requires store and publisher")
	}
	return &Dispatcher{
		store:     store,
		publisher: publisher,
	}, nil
}

// DispatchRound 逐条领取有界数量的事件，避免等待同批发布时提前耗尽租约
func (d *Dispatcher) DispatchRound(ctx context.Context) ([]DispatchResult, error) {
	var results []DispatchResult
	for count := 0; count < dispatchBatchSize; count++ {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		claimCtx, cancel := context.WithTimeout(ctx, dispatchTimeout)
		dispatches, err := d.store.ClaimPendingEvents(claimCtx, 1, dispatchLease)
		cancel()
		if err != nil {
			return results, err
		}
		if len(dispatches) == 0 {
			return results, nil
		}
		if len(dispatches) != 1 {
			return results, domaininteraction.ErrInvalidDispatch
		}
		results = append(results, d.dispatch(ctx, dispatches[0]))
	}
	return results, nil
}

// dispatch 只在发布确认后标记完成，确认或落库失败均保留同事件重发的可能
func (d *Dispatcher) dispatch(ctx context.Context, dispatch domaininteraction.OutboxDispatch) DispatchResult {
	result := DispatchResult{
		EventID:        dispatch.Event.EventID,
		VideoID:        dispatch.Event.VideoID,
		Attempt:        dispatch.Attempt,
		LeaseTakenOver: dispatch.LeaseTakenOver,
	}
	if dispatch.ID == 0 || dispatch.Attempt <= 0 {
		result.Outcome, result.Err = DispatchReleaseFailed, domaininteraction.ErrInvalidDispatch
		return result
	}
	if err := dispatch.Event.Validate(); err != nil {
		return d.release(ctx, dispatch, result, dispatchInvalidBackoff, "invalid_payload", err)
	}
	publishCtx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	err := d.publisher.PublishChanged(publishCtx, dispatch.Event)
	cancel()
	if err != nil {
		return d.release(ctx, dispatch, result, dispatchBackoff(dispatch.Attempt), "publish_failed", err)
	}
	markCtx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	marked, err := d.store.MarkDispatched(markCtx, dispatch.ID, dispatch.Attempt)
	cancel()
	switch {
	case err != nil:
		result.Outcome, result.Err = DispatchMarkFailed, err
	case !marked:
		result.Outcome = DispatchLeaseLost
	default:
		result.Outcome = DispatchConfirmed
	}
	return result
}

func (d *Dispatcher) release(ctx context.Context, dispatch domaininteraction.OutboxDispatch, result DispatchResult, backoff time.Duration, reason string, cause error) DispatchResult {
	releaseCtx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	released, err := d.store.ReleaseRetry(releaseCtx, dispatch.ID, dispatch.Attempt, backoff, reason)
	cancel()
	result.Err = cause
	switch {
	case err != nil:
		result.Outcome, result.Err = DispatchReleaseFailed, errors.Join(cause, err)
	case !released:
		result.Outcome = DispatchLeaseLost
	default:
		result.Outcome = DispatchRetryScheduled
	}
	return result
}

func dispatchBackoff(attempt int) time.Duration {
	wait := time.Second
	for current := 1; current < attempt; current++ {
		if wait >= dispatchMaxBackoff/2 {
			return dispatchMaxBackoff
		}
		wait *= 2
	}
	return wait
}
