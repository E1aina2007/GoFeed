package applicationfeed

import (
	"context"
	"errors"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

const TimelinePageSortVersion = timelineSortVersion

var (
	ErrInvalidPageCacheQuery = errors.New("invalid feed page cache query")
	ErrInvalidCachedPage     = errors.New("invalid cached feed page")
	ErrPageCacheUnavailable  = errors.New("feed page cache unavailable")
)

type PageCacheQuery struct {
	Scene  domainfeed.Scene
	Cursor *domainfeed.TimelineCursor
	Limit  int
}

type CachedPage struct {
	Items []domainfeed.FeedPageItem // 可包含一条下一页探测记录
}

type PageCache interface {
	GetPage(ctx context.Context, query PageCacheQuery) (CachedPage, bool, error)
	SetPage(ctx context.Context, query PageCacheQuery, page CachedPage) error
}

func (q PageCacheQuery) Validate() error {
	if q.Scene != domainfeed.SceneTimeline || q.Limit < 1 || q.Limit > domainfeed.MaxLimit {
		return ErrInvalidPageCacheQuery
	}
	if q.Cursor != nil && (q.Cursor.VideoID == 0 || !validPageTime(q.Cursor.PublishedAt)) {
		return ErrInvalidPageCacheQuery
	}
	return nil
}

// Validate 检查页条目的数量、唯一性、排序和游标范围
func (p CachedPage) Validate(query PageCacheQuery) error {
	if err := query.Validate(); err != nil {
		return err
	}
	if len(p.Items) > query.Limit+1 {
		return ErrInvalidCachedPage
	}
	seen := make(map[uint]struct{}, len(p.Items))
	for i, item := range p.Items {
		if item.VideoID == 0 || !validPageTime(item.PublishedAt) {
			return ErrInvalidCachedPage
		}
		if _, exists := seen[item.VideoID]; exists {
			return ErrInvalidCachedPage
		}
		seen[item.VideoID] = struct{}{}
		if query.Cursor != nil && !pagePositionBefore(item, query.Cursor.PublishedAt, query.Cursor.VideoID) {
			return ErrInvalidCachedPage
		}
		if i > 0 && !pagePositionBefore(item, p.Items[i-1].PublishedAt, p.Items[i-1].VideoID) {
			return ErrInvalidCachedPage
		}
	}
	return nil
}

func pagePositionBefore(item domainfeed.FeedPageItem, publishedAt time.Time, videoID uint) bool {
	return item.PublishedAt.Before(publishedAt) ||
		(item.PublishedAt.Equal(publishedAt) && item.VideoID < videoID)
}

func validPageTime(value time.Time) bool {
	year := value.UTC().Year()
	return !value.IsZero() && year >= 1 && year <= 9999
}
