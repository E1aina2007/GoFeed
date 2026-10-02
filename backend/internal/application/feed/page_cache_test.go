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

// 测试目标：页缓存查询只接受 Timeline 场景、合法页大小与合法游标位置
// 预期效果：非法场景、越界页大小与损坏游标都返回 ErrInvalidPageCacheQuery
func TestPageCacheQueryValidate(t *testing.T) {
	valid := []struct {
		name  string
		query PageCacheQuery
	}{
		{"首屏查询", PageCacheQuery{Scene: domainfeed.SceneTimeline, Limit: 1}},
		{"最大页大小", PageCacheQuery{Scene: domainfeed.SceneTimeline, Limit: domainfeed.MaxLimit}},
		{"带游标查询", PageCacheQuery{Scene: domainfeed.SceneTimeline, Cursor: cursorAt(0), Limit: 20}},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.query.Validate(); err != nil {
				t.Fatalf("合法查询被拒绝: %v", err)
			}
		})
	}

	invalid := []struct {
		name  string
		query PageCacheQuery
	}{
		{"未启用场景", PageCacheQuery{Scene: domainfeed.SceneFollowing, Limit: 20}},
		{"页大小为零", PageCacheQuery{Scene: domainfeed.SceneTimeline, Limit: 0}},
		{"页大小为负", PageCacheQuery{Scene: domainfeed.SceneTimeline, Limit: -1}},
		{"页大小超上限", PageCacheQuery{Scene: domainfeed.SceneTimeline, Limit: domainfeed.MaxLimit + 1}},
		{"游标视频 ID 为零", PageCacheQuery{Scene: domainfeed.SceneTimeline, Cursor: &domainfeed.TimelineCursor{PublishedAt: originTime}, Limit: 20}},
		{"游标时间为零值", PageCacheQuery{Scene: domainfeed.SceneTimeline, Cursor: &domainfeed.TimelineCursor{VideoID: 900}, Limit: 20}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.query.Validate(); !errors.Is(err, ErrInvalidPageCacheQuery) {
				t.Fatalf("got error=%v want=%v", err, ErrInvalidPageCacheQuery)
			}
		})
	}
}

// 测试目标：有效空页是合法缓存载荷
// 预期效果：空条目列表通过校验而不是被当成损坏数据
func TestCachedPageValidateAcceptsEmptyPage(t *testing.T) {
	query := PageCacheQuery{Scene: domainfeed.SceneTimeline, Cursor: cursorAt(0), Limit: 20}
	if err := (CachedPage{Items: []domainfeed.FeedPageItem{}}).Validate(query); err != nil {
		t.Fatalf("有效空页被拒绝: %v", err)
	}
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

// 测试目标：分页比较规则同时覆盖时间不同与时间相同的破平依据
// 预期效果：条目严格早于游标位置时判定为属于下一页
func TestPagePositionBefore(t *testing.T) {
	base := pageItemAt(0, 7)

	cases := []struct {
		name      string
		published time.Time
		videoID   uint
		want      bool
	}{
		{"游标更早则条目属于下一页", base.PublishedAt.Add(-time.Second), 999, false},
		{"游标更晚则条目属于下一页", base.PublishedAt.Add(time.Second), 1, true},
		{"同时间游标 ID 更小则条目属于下一页", base.PublishedAt, base.VideoID - 1, false},
		{"同时间游标 ID 更大则条目属于下一页", base.PublishedAt, base.VideoID + 1, true},
		{"同时间同 ID 不属于下一页", base.PublishedAt, base.VideoID, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pagePositionBefore(base, tc.published, tc.videoID); got != tc.want {
				t.Fatalf("got=%v want=%v", got, tc.want)
			}
		})
	}
}

// 测试目标：缓存时间字段限定在数据库可表示的年份范围内
// 预期效果：零值（即公元 1 年 1 月 1 日零点）、年份 0 与年份 10000 判为非法，年份 1 与 9999 的有效时刻判为合法
func TestValidPageTimeBoundary(t *testing.T) {
	cases := []struct {
		name  string
		value time.Time
		want  bool
	}{
		{"零值时间", time.Time{}, false},
		{"年份零", time.Date(0, time.January, 1, 0, 0, 0, 0, time.UTC), false},
		{"年份一的零点即零值", time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC), false},
		{"年份一的非零时刻", time.Date(1, time.January, 1, 0, 0, 1, 0, time.UTC), true},
		{"年份九九九九", time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC), true},
		{"年份一万", time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validPageTime(tc.value); got != tc.want {
				t.Fatalf("got=%v want=%v", got, tc.want)
			}
		})
	}
}

// 测试目标：缓存页校验在查询本身非法时先报告查询错误
// 预期效果：非法查询返回 ErrInvalidPageCacheQuery 而不是 ErrInvalidCachedPage
func TestCachedPageValidateChecksQueryFirst(t *testing.T) {
	query := PageCacheQuery{Scene: domainfeed.SceneTimeline, Limit: 0}
	page := CachedPage{Items: []domainfeed.FeedPageItem{}}
	if err := page.Validate(query); !errors.Is(err, ErrInvalidPageCacheQuery) {
		t.Fatalf("got error=%v want=%v", err, ErrInvalidPageCacheQuery)
	}
}
