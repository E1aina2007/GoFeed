package infrafeed

import (
	"context"
	"fmt"

	domainfeed "gofeed/internal/domain/feed"
	domainvideo "gofeed/internal/domain/video"
	infravideo "gofeed/internal/infra/persistence/video"
)

// 以下窄接口只存在于外层适配器，既有实体不会泄漏到 Feed 的 Domain/Application
type PublishedVideoReader interface {
	GetPublishedVideoList(ctx context.Context, authorID uint, cursor *domainvideo.ListPosition, limit int) ([]infravideo.Video, error)
}

type AuthorReader interface {
	GetPublicAuthors(ctx context.Context, authorIDs []uint) (map[uint]domainvideo.Author, error)
}

type EngagementReader interface {
	GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]domainvideo.EngagementCounts, error)
}

// Repository 适配既有仓储和批量读能力，保留 SQL 与公开过滤的唯一实现
// 此过渡实现不调用 video.Service，也不进行新旧外部游标转换
type Repository struct {
	videos  PublishedVideoReader
	authors AuthorReader
	stats   EngagementReader
}

var _ domainfeed.Repository = (*Repository)(nil)

func New(videos PublishedVideoReader, authors AuthorReader, stats EngagementReader) *Repository {
	return &Repository{videos: videos, authors: authors, stats: stats}
}

func (r *Repository) ListTimelinePage(ctx context.Context, cursor *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
	if r.videos == nil {
		return domainfeed.TimelinePage{}, domainfeed.ErrUnavailable
	}
	var position *domainvideo.ListPosition
	if cursor != nil {
		position = &domainvideo.ListPosition{PublishedAt: cursor.PublishedAt, ID: cursor.VideoID}
	}
	rows, err := r.videos.GetPublishedVideoList(ctx, 0, position, limit)
	if err != nil {
		return domainfeed.TimelinePage{}, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	page := domainfeed.TimelinePage{
		Items: make([]domainfeed.FeedPageItem, 0, len(rows)),
		Cards: make(map[uint]domainfeed.FeedCard, len(rows)),
	}
	for _, row := range rows {
		if !infravideo.IsPublicVideo(row) {
			continue
		}
		page.Items = append(page.Items, domainfeed.FeedPageItem{
			VideoID: row.ID, AuthorID: row.AuthorID, PublishedAt: *row.PublishedAt,
		})
		page.Cards[row.ID] = feedCardFromVideo(row)
	}
	return page, nil
}

func (r *Repository) BatchGetAuthors(ctx context.Context, authorIDs []uint) (map[uint]domainfeed.Author, error) {
	result := make(map[uint]domainfeed.Author, len(authorIDs))
	if len(authorIDs) == 0 {
		return result, nil
	}
	if r.authors == nil {
		return nil, domainfeed.ErrUnavailable
	}
	rows, err := r.authors.GetPublicAuthors(ctx, authorIDs)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	for id, row := range rows {
		result[id] = domainfeed.Author{ID: row.ID, Username: row.Username, AvatarURL: row.AvatarURL}
	}
	return result, nil
}

func (r *Repository) BatchGetStats(ctx context.Context, videoIDs []uint) (map[uint]domainfeed.FeedStat, error) {
	result := make(map[uint]domainfeed.FeedStat, len(videoIDs))
	if len(videoIDs) == 0 {
		return result, nil
	}
	if r.stats == nil {
		return nil, domainfeed.ErrUnavailable
	}
	rows, err := r.stats.GetEngagementCounts(ctx, videoIDs)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	for id, row := range rows {
		result[id] = domainfeed.FeedStat{LikesCount: row.LikesCount, CommentsCount: row.CommentsCount}
	}
	return result, nil
}
