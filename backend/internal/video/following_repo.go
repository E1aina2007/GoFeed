package video

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
)

// GetFollowingVideoList 在同一查询内限制当前关注关系、活动作者与公开视频
func (r *Repository) GetFollowingVideoList(ctx context.Context, viewerID uint, cursor *domainvideo.ListPosition, fetchLimit int) ([]Video, error) {
	if viewerID == 0 {
		return nil, ErrInvalidVideoID
	}
	if fetchLimit < 1 || fetchLimit > MaxPublishedVideoBatchSize {
		return nil, ErrInvalidLimit
	}
	query := PublicVideoQuery(r.db.WithContext(ctx)).Select("videos.*").
		Joins("JOIN user_follows AS following ON following.followee_id = videos.author_id AND following.follower_id = ?", viewerID).
		Joins("JOIN users AS following_author ON following_author.id = videos.author_id AND following_author.deleted_at IS NULL")
	if cursor != nil {
		query = query.Where("videos.published_at < ? OR (videos.published_at = ? AND videos.id < ?)", cursor.PublishedAt, cursor.PublishedAt, cursor.ID)
	}
	var rows []Video
	if err := query.Order("videos.published_at DESC, videos.id DESC").Limit(fetchLimit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
