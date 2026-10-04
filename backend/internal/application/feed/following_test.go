package applicationfeed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	domainfeed "gofeed/internal/domain/feed"
)

type followingReadFunc func(context.Context, uint, *domainfeed.FollowingCursor, int) (domainfeed.TimelinePage, error)

func (f followingReadFunc) ListFollowingPage(ctx context.Context, viewer uint, cursor *domainfeed.FollowingCursor, limit int) (domainfeed.TimelinePage, error) {
	return f(ctx, viewer, cursor, limit)
}

// 测试目标：关注流截断探测行后批量组装，并绕过耗尽的 Timeline 缓存容量
// 预期效果：仅最终两条参与作者与统计读取，游标绑定观看者且续页支持改变 limit
func TestFollowingFeedTruncatesAndBypassesTimelineCache(t *testing.T) {
	repo := &stubRepository{}
	page := buildPage(3, func(i int) uint { return uint(10 + i) })
	var seen *domainfeed.FollowingCursor
	reader := followingReadFunc(func(_ context.Context, viewer uint, cursor *domainfeed.FollowingCursor, limit int) (domainfeed.TimelinePage, error) {
		if viewer != 42 || limit < 2 || limit > 3 {
			t.Fatalf("读取参数 viewer=%d limit=%d", viewer, limit)
		}
		seen = cursor
		if cursor != nil {
			return domainfeed.TimelinePage{}, nil
		}
		return page, nil
	})
	s := New(repo, WithFollowingReader(reader))
	s.timelineCache = &timelineCache{readSlots: make(chan struct{}, 1), cacheSlots: make(chan struct{}, 1)}
	s.timelineCache.readSlots <- struct{}{}
	s.timelineCache.cacheSlots <- struct{}{}
	result, err := s.GetFeed(t.Context(), FeedRequest{Scene: domainfeed.SceneFollowing, ViewerID: 42, Limit: 2})
	if err != nil || len(result.Items) != 2 || result.NextCursor == "" {
		t.Fatalf("关注页 result=%+v err=%v", result, err)
	}
	if repo.listCallCount() != 0 || !reflect.DeepEqual(repo.statIDList(), []uint{900, 899}) || !reflect.DeepEqual(repo.authorIDList(), []uint{10, 11}) {
		t.Fatalf("探测行不得组装 timeline=%d stats=%v authors=%v", repo.listCallCount(), repo.statIDList(), repo.authorIDList())
	}
	last := page.Items[1]
	position, err := decodeFollowingCursor(result.NextCursor, 42)
	if err != nil || position.VideoID != last.VideoID || !position.PublishedAt.Equal(last.PublishedAt) {
		t.Fatalf("游标位置=%+v err=%v", position, err)
	}
	if _, err := decodeFollowingCursor(result.NextCursor, 43); !errors.Is(err, domainfeed.ErrInvalidCursor) {
		t.Fatalf("跨观看者游标=%v", err)
	}
	if _, err := decodeTimelineCursor(result.NextCursor); !errors.Is(err, domainfeed.ErrInvalidCursor) {
		t.Fatalf("跨场景游标=%v", err)
	}
	next, err := s.GetFeed(t.Context(), FeedRequest{Scene: domainfeed.SceneFollowing, ViewerID: 42, Limit: 1, Cursor: result.NextCursor})
	if err != nil || seen == nil || seen.VideoID != last.VideoID || next.Items == nil || len(next.Items) != 0 || next.NextCursor != "" {
		t.Fatalf("末页=%+v cursor=%+v err=%v", next, seen, err)
	}
	if len(s.timelineCache.readSlots) != 1 || len(s.timelineCache.cacheSlots) != 1 {
		t.Fatal("关注流不应取得或释放 Timeline 容量")
	}
}

// 测试目标：拒绝错误版本、观看者、场景、位置和编码格式的关注游标
// 预期效果：严格拒绝未知字段、尾随 JSON、填充或超长载荷，UTC 正常游标可读取
func TestFollowingCursorValidation(t *testing.T) {
	encoded, err := encodeFollowingCursor(pageItemAt(0, 7), 42)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || !strings.Contains(string(decoded), `"published_at":"2026-05-04T03:02:01Z"`) {
		t.Fatalf("UTC 编码=%s err=%v", decoded, err)
	}
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"version", "version", 2}, {"scene", "scene", "timeline"}, {"sort", "sort_version", 2},
		{"viewer_zero", "viewer_id", 0}, {"viewer_other", "viewer_id", 43}, {"id_zero", "video_id", 0},
		{"id_negative", "video_id", -1}, {"time_zero", "published_at", "0001-01-01T00:00:00Z"},
		{"time_bad", "published_at", "invalid"}, {"extra", "extra", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data map[string]any
			if err := json.Unmarshal(decoded, &data); err != nil {
				t.Fatal(err)
			}
			data[tc.field] = tc.value
			payload, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeFollowingCursor(base64.RawURLEncoding.EncodeToString(payload), 42); !errors.Is(err, domainfeed.ErrInvalidCursor) {
				t.Fatalf("应拒绝游标 err=%v", err)
			}
		})
	}
	for _, bad := range []string{"invalid!", encoded + "=", encoded + "\n", strings.Repeat("a", maxCursorLength+1), base64.RawURLEncoding.EncodeToString(append(decoded, []byte(` {}`)...)), base64.RawURLEncoding.EncodeToString([]byte(`{}`))} {
		if _, err := decodeFollowingCursor(bad, 42); !errors.Is(err, domainfeed.ErrInvalidCursor) {
			t.Fatalf("应拒绝编码 err=%v", err)
		}
	}
	if cursor, err := decodeFollowingCursor("", 42); cursor != nil || err != nil {
		t.Fatalf("首屏 cursor=%v err=%v", cursor, err)
	}
}

// 测试目标：关注流的认证、空页、上下文与依赖故障不能回退为 Timeline 或伪成功
// 预期效果：缺观看者拒绝，缺依赖不可用，空页不组装，取消和异常读模型保留错误
func TestFollowingFeedFailureAndEmptyPage(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		viewer                uint
		readerErr, want       error
		noReader, invalidPage bool
	}{
		{name: "viewer", want: domainfeed.ErrUnauthenticated},
		{name: "dependency", viewer: 42, noReader: true, want: domainfeed.ErrUnavailable},
		{name: "deleted_viewer", viewer: 42, readerErr: domainfeed.ErrUnauthenticated, want: domainfeed.ErrUnauthenticated},
		{name: "cancelled", viewer: 42, readerErr: context.Canceled, want: domainfeed.ErrUnavailable},
		{name: "database", viewer: 42, readerErr: errors.New("injected database outage"), want: domainfeed.ErrUnavailable},
		{name: "invalid_page", viewer: 42, invalidPage: true, want: domainfeed.ErrUnavailable},
		{name: "empty", viewer: 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepository{}
			reader := followingReadFunc(func(context.Context, uint, *domainfeed.FollowingCursor, int) (domainfeed.TimelinePage, error) {
				if tc.invalidPage {
					return domainfeed.TimelinePage{Items: []domainfeed.FeedPageItem{pageItemAt(0, 7)}}, nil
				}
				return domainfeed.TimelinePage{}, tc.readerErr
			})
			s := New(repo, WithFollowingReader(reader))
			if tc.noReader {
				s.followingReader = nil
			}
			result, err := s.GetFeed(t.Context(), FeedRequest{Scene: domainfeed.SceneFollowing, ViewerID: tc.viewer})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if tc.want == nil && (result.Items == nil || len(result.Items) != 0 || result.NextCursor != "") {
				t.Fatalf("空页=%+v", result)
			}
			if repo.listCallCount() != 0 || repo.statCalls != 0 || repo.authorCalls != 0 {
				t.Fatal("失败或空页不应查询 Timeline 或组装数据")
			}
		})
	}
}
