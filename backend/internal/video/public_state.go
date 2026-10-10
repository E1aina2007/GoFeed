package video

import (
	"context"

	infravideo "gofeed/internal/infra/persistence/video"
)

// GetPublicVideoStates 使用完整公开作用域，只读取标识与排序字段
func (r *Repository) GetPublicVideoStates(ctx context.Context, ids []uint) ([]infravideo.PublicVideoState, error) {
	queried := make([]uint, 0, min(len(ids), infravideo.MaxPublishedVideoBatchSize))
	seen := make(map[uint]struct{}, cap(queried))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		if len(queried) == infravideo.MaxPublishedVideoBatchSize {
			return nil, infravideo.ErrInvalidPublishedVideoBatch
		}
		seen[id] = struct{}{}
		queried = append(queried, id)
	}
	states := make([]infravideo.PublicVideoState, 0, len(queried))
	if len(queried) == 0 {
		return states, nil
	}
	if err := infravideo.PublicVideoQuery(r.db.WithContext(ctx)).Select("id", "author_id", "published_at").
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
