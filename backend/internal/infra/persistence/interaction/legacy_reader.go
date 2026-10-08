package infrainteraction

import (
	"context"

	domainaccount "gofeed/internal/domain/account"
	domaininteraction "gofeed/internal/domain/interaction"
	domainrelation "gofeed/internal/domain/relation"
	"gofeed/internal/video"
)

type EngagementReader struct {
	reader domaininteraction.EngagementReader
}

type ProfileMetricsReader struct {
	likes   domaininteraction.TotalLikesReader
	follows domainrelation.CountReader
}

var (
	_ video.EngagementReader             = (*EngagementReader)(nil)
	_ domainaccount.ProfileMetricsReader = (*ProfileMetricsReader)(nil)
)

func NewEngagementReader(reader domaininteraction.EngagementReader) *EngagementReader {
	return &EngagementReader{reader: reader}
}

func NewProfileMetricsReader(likes domaininteraction.TotalLikesReader, follows domainrelation.CountReader) *ProfileMetricsReader {
	return &ProfileMetricsReader{likes: likes, follows: follows}
}

// GetEngagementCounts 将领域统计转换为旧 Video 与 Feed 消费方的结果
func (r *EngagementReader) GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]video.EngagementCounts, error) {
	counts := make(map[uint]video.EngagementCounts, len(videoIDs))
	if len(videoIDs) == 0 {
		return counts, nil
	}
	rows, err := r.reader.GetEngagementCounts(ctx, videoIDs)
	if err != nil {
		return nil, err
	}
	for id, row := range rows {
		counts[id] = video.EngagementCounts{LikesCount: row.LikesCount, CommentsCount: row.CommentsCount}
	}
	return counts, nil
}

// GetProfileMetrics 按获赞、粉丝、关注顺序组合账户领域统计
func (r *ProfileMetricsReader) GetProfileMetrics(ctx context.Context, accountID uint) (domainaccount.ProfileMetrics, error) {
	if accountID == 0 {
		return domainaccount.ProfileMetrics{}, nil
	}
	likes, err := r.likes.GetTotalLikes(ctx, accountID)
	if err != nil {
		return domainaccount.ProfileMetrics{}, err
	}
	followers, err := r.follows.GetFollowerCount(ctx, accountID)
	if err != nil {
		return domainaccount.ProfileMetrics{}, err
	}
	following, err := r.follows.GetFollowingCount(ctx, accountID)
	if err != nil {
		return domainaccount.ProfileMetrics{}, err
	}
	return domainaccount.ProfileMetrics{TotalLikes: likes, FollowerCount: followers, VloggerCount: following}, nil
}
