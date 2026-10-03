package video

import (
	"context"
	"time"
)

type PublicVideoState struct {
	ID          uint
	AuthorID    uint
	PublishedAt time.Time
}

// GetPublicVideoStates 使用完整公开作用域，只读取标识与排序字段
func (r *Repository) GetPublicVideoStates(ctx context.Context, ids []uint) ([]PublicVideoState, error) {
	queried := make([]uint, 0, min(len(ids), MaxPublishedVideoBatchSize))
	seen := make(map[uint]struct{}, cap(queried))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		if len(queried) == MaxPublishedVideoBatchSize {
			return nil, ErrInvalidPublishedVideoBatch
		}
		seen[id] = struct{}{}
		queried = append(queried, id)
	}
	states := make([]PublicVideoState, 0, len(queried))
	if len(queried) == 0 {
		return states, nil
	}
	if err := PublicVideoQuery(r.db.WithContext(ctx)).Select("id", "author_id", "published_at").
		Where("id IN ?", queried).Find(&states).Error; err != nil {
		return nil, err
	}
	valid := states[:0]
	for _, state := range states {
		if !state.PublishedAt.IsZero() {
			valid = append(valid, state)
		}
	}
	return valid, nil
}
