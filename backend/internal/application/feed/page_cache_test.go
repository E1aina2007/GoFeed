package applicationfeed

import (
	"errors"
	"testing"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

// cursorAt 取第 index 条页条目作为游标位置
func cursorAt(index int) *domainfeed.TimelineCursor {
	item := pageItemAt(index, 7)
	return &domainfeed.TimelineCursor{PublishedAt: item.PublishedAt, VideoID: item.VideoID}
}

// cachedItemsFrom 取从 start 开始的 count 条页条目
func cachedItemsFrom(start, count int) []domainfeed.FeedPageItem {
	items := make([]domainfeed.FeedPageItem, 0, count)
	for index := start; index < start+count; index++ {
		items = append(items, pageItemAt(index, 7))
	}
	return items
}

// 测试目标：缓存页条目数量上限为页大小加一条探测记录
// 预期效果：恰好 limit+1 条通过，多一条返回 ErrInvalidCachedPage
func TestCachedPageValidateItemCountBoundary(t *testing.T) {
	query := PageCacheQuery{Scene: domainfeed.SceneTimeline, Cursor: cursorAt(0), Limit: 3}
	atLimit := CachedPage{Items: cachedItemsFrom(1, 4)}
	if err := atLimit.Validate(query); err != nil {
		t.Fatalf("恰好 limit+1 条被拒绝: %v", err)
	}

	overLimit := CachedPage{Items: cachedItemsFrom(1, 5)}
	if err := overLimit.Validate(query); !errors.Is(err, ErrInvalidCachedPage) {
		t.Fatalf("got error=%v want=%v", err, ErrInvalidCachedPage)
	}
}

// 测试目标：缓存页必须携带有效且唯一的位置信息
// 预期效果：零视频 ID、零发布时间与重复 ID 都返回 ErrInvalidCachedPage
func TestCachedPageValidateRejectsInvalidItems(t *testing.T) {
	query := PageCacheQuery{Scene: domainfeed.SceneTimeline, Cursor: cursorAt(0), Limit: 5}

	cases := []struct {
		name  string
		items []domainfeed.FeedPageItem
	}{
		{"视频 ID 为零", []domainfeed.FeedPageItem{{AuthorID: 7, PublishedAt: originTime.Add(-time.Minute)}}},
		{"发布时间为零值", []domainfeed.FeedPageItem{{VideoID: 899, AuthorID: 7}}},
		{"视频 ID 重复", []domainfeed.FeedPageItem{
			pageItemAt(1, 7),
			{VideoID: pageItemAt(1, 7).VideoID, AuthorID: 7, PublishedAt: pageItemAt(2, 7).PublishedAt},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := (CachedPage{Items: tc.items}).Validate(query); !errors.Is(err, ErrInvalidCachedPage) {
				t.Fatalf("got error=%v want=%v", err, ErrInvalidCachedPage)
			}
		})
	}
}

// 测试目标：缓存页位置必须严格早于请求游标
// 预期效果：与游标重合或晚于游标的条目返回 ErrInvalidCachedPage
func TestCachedPageValidateRejectsOutOfCursorRange(t *testing.T) {
	query := PageCacheQuery{Scene: domainfeed.SceneTimeline, Cursor: cursorAt(0), Limit: 5}

	t.Run("与游标重合", func(t *testing.T) {
		page := CachedPage{Items: []domainfeed.FeedPageItem{pageItemAt(0, 7), pageItemAt(1, 7)}}
		if err := page.Validate(query); !errors.Is(err, ErrInvalidCachedPage) {
			t.Fatalf("got error=%v want=%v", err, ErrInvalidCachedPage)
		}
	})

	t.Run("晚于游标", func(t *testing.T) {
		newer := pageItemAt(0, 7)
		newer.PublishedAt = newer.PublishedAt.Add(time.Hour)
		page := CachedPage{Items: []domainfeed.FeedPageItem{newer}}
		if err := page.Validate(query); !errors.Is(err, ErrInvalidCachedPage) {
			t.Fatalf("got error=%v want=%v", err, ErrInvalidCachedPage)
		}
	})
}

// 测试目标：缓存页必须严格按发布时间与视频 ID 倒序排列
// 预期效果：同时间升序 ID 或时间递增的条目返回 ErrInvalidCachedPage
func TestCachedPageValidateRequiresStrictDescendingOrder(t *testing.T) {
	query := PageCacheQuery{Scene: domainfeed.SceneTimeline, Cursor: cursorAt(0), Limit: 5}

	t.Run("乱序", func(t *testing.T) {
		page := CachedPage{Items: []domainfeed.FeedPageItem{pageItemAt(2, 7), pageItemAt(1, 7)}}
		if err := page.Validate(query); !errors.Is(err, ErrInvalidCachedPage) {
			t.Fatalf("got error=%v want=%v", err, ErrInvalidCachedPage)
		}
	})

	t.Run("同时间视频 ID 递增", func(t *testing.T) {
		same := pageItemAt(3, 7).PublishedAt
		page := CachedPage{Items: []domainfeed.FeedPageItem{
			{VideoID: 100, AuthorID: 7, PublishedAt: same},
			{VideoID: 101, AuthorID: 7, PublishedAt: same},
		}}
		if err := page.Validate(query); !errors.Is(err, ErrInvalidCachedPage) {
			t.Fatalf("got error=%v want=%v", err, ErrInvalidCachedPage)
		}
	})

	t.Run("同时间视频 ID 递减", func(t *testing.T) {
		same := pageItemAt(3, 7).PublishedAt
		page := CachedPage{Items: []domainfeed.FeedPageItem{
			{VideoID: 102, AuthorID: 7, PublishedAt: same},
			{VideoID: 101, AuthorID: 7, PublishedAt: same},
			{VideoID: 100, AuthorID: 7, PublishedAt: same},
		}}
		if err := page.Validate(query); err != nil {
			t.Fatalf("同时间倒序 ID 应通过 got error=%v", err)
		}
	})
}
