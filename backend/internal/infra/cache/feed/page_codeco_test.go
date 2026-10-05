package infracachefeed

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"
)

// payloadItem 构造带作者标识的载荷条目
func payloadItem(videoID uint, authorID uint, publishedAt time.Time) pagePayloadItem {
	return pagePayloadItem{VideoID: videoID, AuthorID: &authorID, PublishedAt: publishedAt}
}

// payloadBefore 生成严格早于游标且按时间倒序的载荷条目
func payloadBefore(count int, cursor time.Time) []pagePayloadItem {
	items := make([]pagePayloadItem, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, payloadItem(uint(100-i), uint(1+i), cursor.Add(-time.Duration(i+1)*time.Minute)))
	}
	return items
}

// marshalPayload 将载荷编码为缓存 JSON 字符串
func marshalPayload(t *testing.T, payload pagePayload) string {
	t.Helper()
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("编码载荷失败: %v", err)
	}
	return string(value)
}

// comparePageItems 逐条比较页条目并检查发布时间已归一为 UTC
func comparePageItems(t *testing.T, got, want []domainfeed.FeedPageItem) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("条目数 got=%d want=%d", len(got), len(want))
	}
	for i, item := range got {
		if item.VideoID != want[i].VideoID || item.AuthorID != want[i].AuthorID {
			t.Fatalf("item[%d]=%+v want=%+v", i, item, want[i])
		}
		if !item.PublishedAt.Equal(want[i].PublishedAt) || item.PublishedAt.Location() != time.UTC {
			t.Fatalf("item[%d] published=%v want=%v", i, item.PublishedAt, want[i].PublishedAt)
		}
	}
}

// 测试目标：解码校验载荷版本、条数上限、标识唯一与游标范围
// 预期效果：合法载荷通过，其余载荷全部归类为无效缓存页
func TestDecodePageValidatesPayloadShape(t *testing.T) {
	instant := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	cursor := &domainfeed.TimelineCursor{PublishedAt: instant, VideoID: 100}
	query := timelineQuery(cursor, 3)
	valid := payloadBefore(4, instant)
	validJSON := marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: valid})
	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "valid at limit plus one", payload: validJSON},
		{name: "valid empty items", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{}})},
		{name: "over limit", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: payloadBefore(5, instant)}), wantErr: true},
		{name: "items null", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion}), wantErr: true},
		{name: "items missing", payload: `{"version":1,"sort_version":1}`, wantErr: true},
		{name: "unknown field", payload: `{"version":1,"sort_version":1,"items":[],"extra":1}`, wantErr: true},
		{name: "trailing json", payload: validJSON + `{"version":1}`, wantErr: true},
		{name: "invalid json", payload: `{"version":1,`, wantErr: true},
		{name: "version mismatch", payload: marshalPayload(t, pagePayload{Version: 2, SortVersion: applicationfeed.TimelinePageSortVersion, Items: valid}), wantErr: true},
		{name: "sort version mismatch", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion + 1, Items: valid}), wantErr: true},
		{name: "author id null", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{{VideoID: 90, PublishedAt: instant.Add(-time.Minute)}}}), wantErr: true},
		{name: "author id missing", payload: `{"version":1,"sort_version":1,"items":[{"video_id":90,"published_at":"2024-03-04T05:05:07Z"}]}`, wantErr: true},
		{name: "video id zero", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(0, 1, instant.Add(-time.Minute))}}), wantErr: true},
		{name: "duplicate video id", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(90, 1, instant.Add(-2*time.Minute)), payloadItem(90, 2, instant.Add(-time.Minute))}}), wantErr: true},
		{name: "ascending order", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{valid[1], valid[0]}}), wantErr: true},
		{name: "same time ascending video", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(90, 1, instant.Add(-time.Minute)), payloadItem(91, 1, instant.Add(-time.Minute))}}), wantErr: true},
		{name: "cursor position inclusive", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(cursor.VideoID, 1, instant)}}), wantErr: true},
		{name: "cursor time passed", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(1, 1, instant.Add(time.Minute))}}), wantErr: true},
		{name: "zero published at", payload: marshalPayload(t, pagePayload{Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion, Items: []pagePayloadItem{payloadItem(90, 1, time.Time{})}}), wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			page, err := decodePage(testCase.payload, query, defaultMaxPayloadSize)
			if testCase.wantErr {
				if !errors.Is(err, applicationfeed.ErrInvalidCachedPage) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if page.Items == nil {
				t.Fatalf("items=%#v", page.Items)
			}
		})
	}
}

// 测试目标：解码在字节上限处放行且超过一字节即拒绝
// 预期效果：等于载荷长度通过，长度减一返回无效缓存页
func TestDecodePageByteLimitBoundary(t *testing.T) {
	query := timelineQuery(nil, 20)
	value, err := encodePage(applicationfeed.CachedPage{Items: descendingItems(3, time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC))}, maxPayloadSize)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if _, err := decodePage(value, query, len(value)); err != nil {
		t.Fatalf("恰好等于上限应通过: %v", err)
	}
	if _, err := decodePage(value, query, len(value)-1); !errors.Is(err, applicationfeed.ErrInvalidCachedPage) {
		t.Fatalf("超过上限应失败: %v", err)
	}
}

// 测试目标：解码在超出字节上限时先于 JSON 语法判断拒绝
// 预期效果：超限返回无效缓存页且放宽上限后才出现 JSON 语法错误
func TestDecodePageRejectsOversizedPayload(t *testing.T) {
	query := timelineQuery(nil, 20)
	oversized := strings.Repeat("x", defaultMaxPayloadSize+1)
	var syntaxErr *json.SyntaxError
	_, err := decodePage(oversized, query, defaultMaxPayloadSize)
	if !errors.Is(err, applicationfeed.ErrInvalidCachedPage) || errors.As(err, &syntaxErr) {
		t.Fatalf("超限载荷应先按字节上限拒绝: %v", err)
	}
	_, err = decodePage(oversized, query, defaultMaxPayloadSize+1)
	if !errors.Is(err, applicationfeed.ErrInvalidCachedPage) || !errors.As(err, &syntaxErr) {
		t.Fatalf("放宽上限后应转为 JSON 语法错误: %v", err)
	}
}

// 测试目标：编码在字节上限处放行且超过一字节即拒绝
// 预期效果：等于编码长度通过，长度减一返回无效缓存页
func TestEncodePageByteLimitBoundary(t *testing.T) {
	page := applicationfeed.CachedPage{Items: descendingItems(4, time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC))}
	value, err := encodePage(page, maxPayloadSize)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if _, err := encodePage(page, len(value)); err != nil {
		t.Fatalf("恰好等于上限应通过: %v", err)
	}
	if _, err := encodePage(page, len(value)-1); !errors.Is(err, applicationfeed.ErrInvalidCachedPage) {
		t.Fatalf("超过上限应失败: %v", err)
	}
}
