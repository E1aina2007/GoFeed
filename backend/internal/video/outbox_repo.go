package video

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OutboxDispatch 是一条待派发事件及其视频媒体快照
type OutboxDispatch struct {
	Event          OutboxEvent
	Video          Video
	HasVideo       bool
	LeaseTakenOver bool
}

// OutboxSnapshot 汇总仍待派发的 outbox 状态，供 worker 运维观测使用
type OutboxSnapshot struct {
	PendingCount       int64
	PublishingCount    int64
	OldestPendingAt    *time.Time
	OldestPublishingAt *time.Time
}

// ErrInvalidOutboxLease 表示 claim 或续约传入的租约时长不可用
var ErrInvalidOutboxLease = errors.New("invalid outbox lease")

// ClaimPendingOutboxEvents 在短事务内把到期的 pending 事件租约为 publishing 并返回
// 行锁与批量状态更新保证同一事件同时只被一个 relay 持有；租约过期的 publishing 事件可被接管，
// 因此多实例 relay 不会同时派发同一条事件。租约接管仍可能造成重复投递，
// 消费端按 processing CAS 幂等兜底，符合至少一次投递语义
func (r *Repository) ClaimPendingOutboxEvents(ctx context.Context, limit int, lease time.Duration) ([]OutboxDispatch, error) {
	leaseExpr, err := outboxLeaseInterval(lease)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}

	var claimed []OutboxEvent
	takenOver := make(map[uint]bool)
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []OutboxEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where(
				"(status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= NOW(3))) OR (status = ? AND (locked_until IS NULL OR locked_until <= NOW(3)))",
				OutboxEventStatusPending,
				OutboxEventStatusPublishing,
			).
			Order("id ASC").
			Limit(limit).
			Find(&candidates).Error; err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}

		ids := make([]uint, 0, len(candidates))
		for _, event := range candidates {
			ids = append(ids, event.ID)
			takenOver[event.ID] = event.Status == OutboxEventStatusPublishing
		}
		result := tx.Model(&OutboxEvent{}).
			Where("id IN ?", ids).
			Updates(map[string]any{
				"status":          OutboxEventStatusPublishing,
				"attempt":         gorm.Expr("attempt + 1"),
				"next_attempt_at": nil,
				"locked_until":    leaseExpr,
				"last_attempt_at": gorm.Expr("NOW(3)"),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return errors.New("outbox claim changed an unexpected number of rows")
		}
		return tx.Where("id IN ?", ids).Order("id ASC").Find(&claimed).Error
	})
	if err != nil {
		return nil, err
	}
	if len(claimed) == 0 {
		return nil, nil
	}

	ids := make([]uint, 0, len(claimed))
	for _, event := range claimed {
		ids = append(ids, event.VideoID)
	}
	var videos []Video
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&videos).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]Video, len(videos))
	for _, item := range videos {
		byID[item.ID] = item
	}

	dispatches := make([]OutboxDispatch, 0, len(claimed))
	for _, event := range claimed {
		row, ok := byID[event.VideoID]
		if !ok {
			// 视频行已软删除或缺失：仍返回事件并留空快照，由调用方按不一致释放租约，
			// 避免事件在 publishing 与过期接管之间无限churn
			dispatches = append(dispatches, OutboxDispatch{Event: event, LeaseTakenOver: takenOver[event.ID]})
			continue
		}
		dispatches = append(dispatches, OutboxDispatch{
			Event:          event,
			Video:          row,
			HasVideo:       true,
			LeaseTakenOver: takenOver[event.ID],
		})
	}
	return dispatches, nil
}

// GetOutboxSnapshot 返回 pending、publishing 数量及各自最老事件时间
func (r *Repository) GetOutboxSnapshot(ctx context.Context) (OutboxSnapshot, error) {
	var snapshot OutboxSnapshot
	err := r.db.WithContext(ctx).Model(&OutboxEvent{}).
		Select(`
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS pending_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS publishing_count,
			MIN(CASE WHEN status = ? THEN created_at END) AS oldest_pending_at,
			MIN(CASE WHEN status = ? THEN created_at END) AS oldest_publishing_at
		`, OutboxEventStatusPending, OutboxEventStatusPublishing, OutboxEventStatusPending, OutboxEventStatusPublishing).
		Scan(&snapshot).Error
	return snapshot, err
}

// MarkOutboxDispatched 将确认发布成功且仍持有该次租约的事件标记为已派发；返回是否发生变更
// attempt 作为围栏令牌：租约被接管后 attempt 已递增，旧持有者无法覆盖新状态
func (r *Repository) MarkOutboxDispatched(ctx context.Context, id uint, attempt int) (bool, error) {
	result := r.db.WithContext(ctx).Model(&OutboxEvent{}).
		Where("id = ? AND status = ? AND attempt = ? AND locked_until > NOW(3)", id, OutboxEventStatusPublishing, attempt).
		Updates(map[string]any{
			"status":        OutboxEventStatusDispatched,
			"dispatched_at": time.Now(),
			"locked_until":  nil,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ReleaseOutboxRetry 在发布失败时把事件写回 pending 并安排下一次尝试；返回是否发生变更
// 同样以 attempt 作为围栏令牌，避免旧持有者覆盖已被接管的事件
func (r *Repository) ReleaseOutboxRetry(ctx context.Context, id uint, attempt int, nextAttempt time.Duration, cause error) (bool, error) {
	if nextAttempt < 0 {
		return false, ErrInvalidOutboxLease
	}
	seconds := durationSecondsCeil(nextAttempt)
	result := r.db.WithContext(ctx).Model(&OutboxEvent{}).
		Where("id = ? AND status = ? AND attempt = ? AND locked_until > NOW(3)", id, OutboxEventStatusPublishing, attempt).
		Updates(map[string]any{
			"status":          OutboxEventStatusPending,
			"next_attempt_at": gorm.Expr("TIMESTAMPADD(SECOND, ?, NOW(3))", seconds),
			"locked_until":    nil,
			"last_error":      truncateOutboxError(cause),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// outboxLeaseInterval 把租约时长转换为数据库侧到期时间表达式
func outboxLeaseInterval(lease time.Duration) (clause.Expr, error) {
	expr, ok := databaseLeaseInterval(lease)
	if !ok {
		return clause.Expr{}, ErrInvalidOutboxLease
	}
	return expr, nil
}

// truncateOutboxError 将失败原因截断到数据列可容纳的长度，避免写入超长文本
func truncateOutboxError(cause error) string {
	if cause == nil {
		return ""
	}
	return truncateUTF8Bytes(cause.Error(), 255)
}

// CompleteVideoProcessing 将处理中的视频发布；返回是否发生状态变更
// RowsAffected 为 0 表示视频不处于 processing，由调用方按重复消息确认
func (r *Repository) CompleteVideoProcessing(ctx context.Context, videoID uint) (bool, error) {
	result := r.db.WithContext(ctx).Model(&Video{}).
		Where("id = ? AND status = ?", videoID, VideoStatusProcessing).
		Update("status", VideoStatusPublished)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// RejectVideoProcessing 将处理中的视频标记为拒绝并记录原因与时间；返回是否发生状态变更
func (r *Repository) RejectVideoProcessing(ctx context.Context, videoID uint, reason string) (bool, error) {
	result := r.db.WithContext(ctx).Model(&Video{}).
		Where("id = ? AND status = ?", videoID, VideoStatusProcessing).
		Updates(map[string]any{
			"status":          VideoStatusRejected,
			"rejected_reason": truncateUTF8Bytes(reason, 255),
			"rejected_at":     time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}
