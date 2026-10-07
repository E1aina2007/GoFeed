package infrainteraction

import (
	"context"

	domaininteraction "gofeed/internal/domain/interaction"
	"gofeed/internal/video"

	"gorm.io/gorm/clause"
)

var (
	_ domaininteraction.EngagementReader = (*Repository)(nil)
	_ domaininteraction.TotalLikesReader = (*Repository)(nil)
)

// GetEngagementCounts 用两条聚合查询批量读取点赞和未删除评论计数
func (r *Repository) GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]domaininteraction.EngagementCounts, error) {
	counts := make(map[uint]domaininteraction.EngagementCounts, len(videoIDs))
	if len(videoIDs) == 0 {
		return counts, nil
	}
	if !r.available() {
		return nil, domaininteraction.ErrUnavailable
	}
	for _, id := range videoIDs {
		counts[id] = domaininteraction.EngagementCounts{}
	}

	type countRow struct {
		VideoID uint
		Count   int64
	}
	var likes []countRow
	if err := r.db.WithContext(ctx).Model(&VideoLike{}).
		Select("video_id, COUNT(*) AS count").
		Where("video_id IN ?", videoIDs).
		Group("video_id").
		Scan(&likes).Error; err != nil {
		return nil, err
	}
	for _, row := range likes {
		value := counts[row.VideoID]
		value.LikesCount = row.Count
		counts[row.VideoID] = value
	}

	var comments []countRow
	if err := r.db.WithContext(ctx).Model(&Comment{}).
		Select("video_id, COUNT(*) AS count").
		Where("video_id IN ?", videoIDs).
		Group("video_id").
		Scan(&comments).Error; err != nil {
		return nil, err
	}
	for _, row := range comments {
		value := counts[row.VideoID]
		value.CommentsCount = row.Count
		counts[row.VideoID] = value
	}
	return counts, nil
}

// GetTotalLikes 只统计作者完整公开视频获得的点赞
func (r *Repository) GetTotalLikes(ctx context.Context, accountID uint) (int64, error) {
	if accountID == 0 {
		return 0, nil
	}
	if !r.available() {
		return 0, domaininteraction.ErrUnavailable
	}
	var count int64
	err := video.PublicVideoQuery(r.db.WithContext(ctx)).
		Joins("JOIN video_likes AS likes ON likes.video_id = videos.id").
		Where(clause.Eq{
			Column: clause.Column{Table: clause.CurrentTable, Name: "author_id"},
			Value:  accountID,
		}).
		Count(&count).Error
	return count, err
}
