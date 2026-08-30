package video

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OutboxDispatch 是一条待派发事件及其视频媒体快照
type OutboxDispatch struct {
	Event OutboxEvent
	Video Video
}

// ClaimPendingOutboxEvents 读取待派发事件并附带视频媒体快照
// 行级锁加 SKIP LOCKED 允许多实例 relay 同时轮询而不重复取到同一批行；
// 事件引用的视频行已被删除时跳过该事件，由调用方记录不一致
func (r *Repository) ClaimPendingOutboxEvents(ctx context.Context, limit int) ([]OutboxDispatch, error) {
	var events []OutboxEvent
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ?", OutboxEventStatusPending).
			Order("id ASC").
			Limit(limit).
			Find(&events).Error
	})
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}

	ids := make([]uint, 0, len(events))
	for _, event := range events {
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

	dispatches := make([]OutboxDispatch, 0, len(events))
	for _, event := range events {
		row, ok := byID[event.VideoID]
		if !ok {
			continue
		}
		dispatches = append(dispatches, OutboxDispatch{Event: event, Video: row})
	}
	return dispatches, nil
}

// MarkOutboxDispatched 将确认发布成功的事件标记为已派发；返回是否发生变更
// 条件更新保证并发 relay 下同一事件只被标记一次
func (r *Repository) MarkOutboxDispatched(ctx context.Context, id uint) (bool, error) {
	result := r.db.WithContext(ctx).Model(&OutboxEvent{}).
		Where("id = ? AND status = ?", id, OutboxEventStatusPending).
		Updates(map[string]any{
			"status":        OutboxEventStatusDispatched,
			"dispatched_at": time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
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

// RejectVideoProcessing 将处理中的视频标记为拒绝并记录原因；返回是否发生状态变更
func (r *Repository) RejectVideoProcessing(ctx context.Context, videoID uint, reason string) (bool, error) {
	result := r.db.WithContext(ctx).Model(&Video{}).
		Where("id = ? AND status = ?", videoID, VideoStatusProcessing).
		Updates(map[string]any{
			"status":          VideoStatusRejected,
			"rejected_reason": reason,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}
