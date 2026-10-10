package infrafeed

import (
	"context"
	"errors"
	"fmt"
	"log"

	domainfeed "gofeed/internal/domain/feed"
	domainrelation "gofeed/internal/domain/relation"
	domainvideo "gofeed/internal/domain/video"
	"gofeed/internal/video"
)

type FollowingVideoReader interface {
	GetFollowingVideoList(context.Context, uint, *domainvideo.ListPosition, int) ([]video.Video, error)
}

type ActiveViewerReader interface {
	RequireActiveUser(context.Context, uint) error
}

type FollowingReader struct {
	videos  FollowingVideoReader
	viewers ActiveViewerReader
}

var (
	_ domainfeed.FollowingReader = (*FollowingReader)(nil)
	_ FollowingVideoReader       = (*video.Repository)(nil)
)

func NewFollowingReader(videos FollowingVideoReader, viewers ActiveViewerReader) *FollowingReader {
	return &FollowingReader{videos: videos, viewers: viewers}
}

// ListFollowingPage 校验观看者有效并将关注视频转换为 Feed 页数据
func (r *FollowingReader) ListFollowingPage(ctx context.Context, viewerID uint, cursor *domainfeed.FollowingCursor, fetchLimit int) (domainfeed.TimelinePage, error) {
	if viewerID == 0 {
		return domainfeed.TimelinePage{}, domainfeed.ErrUnauthenticated
	}
	if r.videos == nil || r.viewers == nil {
		return domainfeed.TimelinePage{}, domainfeed.ErrUnavailable
	}
	if err := r.viewers.RequireActiveUser(ctx, viewerID); err != nil {
		if errors.Is(err, domainrelation.ErrUserNotFound) {
			return domainfeed.TimelinePage{}, domainfeed.ErrUnauthenticated
		}
		return domainfeed.TimelinePage{}, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	var position *domainvideo.ListPosition
	if cursor != nil {
		position = &domainvideo.ListPosition{PublishedAt: cursor.PublishedAt, ID: cursor.VideoID}
	}
	rows, err := r.videos.GetFollowingVideoList(ctx, viewerID, position, fetchLimit)
	if err != nil {
		return domainfeed.TimelinePage{}, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	page := domainfeed.TimelinePage{Items: make([]domainfeed.FeedPageItem, 0, len(rows)), Cards: make(map[uint]domainfeed.FeedCard, len(rows))}
	for _, row := range rows {
		if !video.IsPublicVideo(row) || row.ID == 0 || row.AuthorID == 0 {
			log.Printf("event=feed_following result=invalid_read")
			return domainfeed.TimelinePage{}, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, domainfeed.ErrInvalidReadResult)
		}
		page.Items = append(page.Items, domainfeed.FeedPageItem{VideoID: row.ID, AuthorID: row.AuthorID, PublishedAt: *row.PublishedAt})
		page.Cards[row.ID] = feedCardFromVideo(row)
	}
	return page, nil
}
