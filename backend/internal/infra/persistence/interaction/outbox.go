package infrainteraction

import (
	"context"
	"time"

	domaininteraction "gofeed/internal/domain/interaction"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxOutboxClaimBatch = 32

var _ domaininteraction.OutboxStore = (*Repository)(nil)

// ClaimPendingEvents 在短事务中领取已提交事实，过期租约接管时递增围栏
func (r *Repository) ClaimPendingEvents(ctx context.Context, limit int, lease time.Duration) ([]domaininteraction.OutboxDispatch, error) {
	if !r.available() {
		return nil, domaininteraction.ErrUnavailable
	}
	if lease <= 0 {
		return nil, domaininteraction.ErrInvalidOutboxLease
	}
	if limit <= 0 {
		return nil, nil
	}
	if limit > maxOutboxClaimBatch {
		limit = maxOutboxClaimBatch
	}
	var claimed []OutboxEvent
	takenOver := make(map[uint]bool)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []OutboxEvent
		if err := tx.Clauses(clause.Locking{
			Strength: "UPDATE",
			Options:  "SKIP LOCKED",
		}).
			Where("(status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= NOW(3))) OR (status = ? AND (locked_until IS NULL OR locked_until <= NOW(3)))", eventStatusPending, eventStatusPublishing).
			Order("id ASC").Limit(limit).Find(&candidates).Error; err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		ids := make([]uint, 0, len(candidates))
		for _, row := range candidates {
			ids = append(ids, row.ID)
			takenOver[row.ID] = row.Status == eventStatusPublishing
		}
		result := tx.Model(&OutboxEvent{}).Where("id IN ?", ids).Updates(map[string]any{
			"status":          eventStatusPublishing,
			"attempt":         gorm.Expr("attempt + 1"),
			"next_attempt_at": nil,
			"locked_until":    outboxDeadline(lease),
			"last_attempt_at": gorm.Expr("NOW(3)"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return errUnexpectedRows
		}
		if err := tx.Where("id IN ?", ids).Order("id ASC").Find(&claimed).Error; err != nil {
			return err
		}
		if len(claimed) != len(ids) {
			return errUnexpectedRows
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	dispatches := make([]domaininteraction.OutboxDispatch, 0, len(claimed))
	for _, row := range claimed {
		dispatches = append(dispatches, domaininteraction.OutboxDispatch{
			ID:             row.ID,
			Attempt:        row.Attempt,
			LeaseTakenOver: takenOver[row.ID],
			Event: domaininteraction.ChangedEvent{
				EventID:              row.EventID,
				SchemaVersion:        row.SchemaVersion,
				EventType:            row.EventType,
				VideoID:              row.VideoID,
				Kind:                 domaininteraction.Kind(row.Kind),
				InteractionID:        row.InteractionID,
				Delta:                row.Delta,
				OccurredAt:           time.UnixMilli(row.OccurredAtMs).UTC(),
				InteractionCreatedAt: time.UnixMilli(row.InteractionCreatedAtMs).UTC(),
			},
		})
	}
	return dispatches, nil
}

// MarkDispatched 仅允许当前未过期租约在发布确认后结束派发
func (r *Repository) MarkDispatched(ctx context.Context, id uint, attempt int) (bool, error) {
	if !r.available() {
		return false, domaininteraction.ErrUnavailable
	}
	if id == 0 || attempt <= 0 {
		return false, domaininteraction.ErrInvalidDispatch
	}
	result := r.db.WithContext(ctx).Model(&OutboxEvent{}).
		Where("id = ? AND status = ? AND attempt = ? AND locked_until > NOW(3)", id, eventStatusPublishing, attempt).
		Updates(map[string]any{
			"status":          eventStatusDispatched,
			"dispatched_at":   gorm.Expr("NOW(3)"),
			"locked_until":    nil,
			"next_attempt_at": nil,
			"last_error":      "",
		})
	return changedOutboxRow(result)
}

// ReleaseRetry 在围栏有效时恢复待派发并按数据库时钟退避，不改写事实载荷
func (r *Repository) ReleaseRetry(ctx context.Context, id uint, attempt int, backoff time.Duration, reason string) (bool, error) {
	if !r.available() {
		return false, domaininteraction.ErrUnavailable
	}
	if id == 0 || attempt <= 0 {
		return false, domaininteraction.ErrInvalidDispatch
	}
	if backoff < 0 {
		return false, domaininteraction.ErrInvalidOutboxLease
	}
	runes := []rune(reason)
	if len(runes) > 255 {
		reason = string(runes[:255])
	}
	result := r.db.WithContext(ctx).Model(&OutboxEvent{}).
		Where("id = ? AND status = ? AND attempt = ? AND locked_until > NOW(3)", id, eventStatusPublishing, attempt).
		Updates(map[string]any{
			"status":          eventStatusPending,
			"locked_until":    nil,
			"next_attempt_at": outboxDeadline(backoff),
			"last_error":      reason,
		})
	return changedOutboxRow(result)
}

func outboxDeadline(delay time.Duration) clause.Expr {
	millis := delay / time.Millisecond
	if delay%time.Millisecond != 0 {
		millis++
	}
	return gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, NOW(3))", int64(millis)*1000)
}

func changedOutboxRow(result *gorm.DB) (bool, error) {
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected > 1 {
		return false, errUnexpectedRows
	}
	return result.RowsAffected == 1, nil
}
