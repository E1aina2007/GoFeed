package interfaceshttpfeed

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"
)

// 测试目标：构造一条字段可区分的领域 Feed 条目
// 预期效果：卡片、作者与统计字段都能在 DTO 映射断言中独立比对
func dtoTestItem(videoID uint, author domainfeed.Author, publishedAt time.Time) domainfeed.FeedItem {
	return domainfeed.FeedItem{
		Card: domainfeed.FeedCard{
			VideoID:           videoID,
			AuthorID:          author.ID,
			Title:             fmt.Sprintf("标题 %d", videoID),
			Description:       fmt.Sprintf("描述 %d", videoID),
			PlayURL:           fmt.Sprintf("/static/play/%d.mp4", videoID),
			PlayFileName:      fmt.Sprintf("play_%d.mp4", videoID),
			PlayOriginalName:  fmt.Sprintf("原始 视频（第 %d 集）#副本.mp4", videoID),
			CoverURL:          fmt.Sprintf("/static/cover/%d.png", videoID),
			CoverFileName:     fmt.Sprintf("cover_%d.png", videoID),
			CoverOriginalName: fmt.Sprintf("封面 图（第 %d 集）!.png", videoID),
			PublishedAt:       publishedAt,
		},
		Author: author,
		Stat:   domainfeed.FeedStat{LikesCount: int64(videoID) * 2, CommentsCount: int64(videoID) * 7},
	}
}

// 测试目标：验证 FeedResult 到响应 DTO 的逐字段映射
// 预期效果：条目顺序、卡片字段、作者与统计一一对应且下一页游标原样保留
func TestFeedItemsResponseFromResultMapping(t *testing.T) {
	publishedAt := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	firstAuthor := domainfeed.Author{ID: 11, Username: "作者 甲", AvatarURL: "/static/avatars/甲.png"}
	secondAuthor := domainfeed.Author{ID: 22, Username: "作者 乙", AvatarURL: "/static/avatars/乙.png"}
	first := dtoTestItem(101, firstAuthor, publishedAt)
	second := dtoTestItem(202, secondAuthor, publishedAt.Add(-time.Hour))

	response := feedItemsResponseFromResult(applicationfeed.FeedResult{
		Items:      []domainfeed.FeedItem{first, second},
		NextCursor: "next-page-token",
	})

	want := feedItemsResponse{
		Items: []feedItemResponse{
			{
				ID:                first.Card.VideoID,
				Title:             first.Card.Title,
				Description:       first.Card.Description,
				PlayURL:           first.Card.PlayURL,
				PlayFileName:      first.Card.PlayFileName,
				PlayOriginalName:  first.Card.PlayOriginalName,
				CoverURL:          first.Card.CoverURL,
				CoverFileName:     first.Card.CoverFileName,
				CoverOriginalName: first.Card.CoverOriginalName,
				PublishedAt:       first.Card.PublishedAt,
				LikesCount:        first.Stat.LikesCount,
				CommentsCount:     first.Stat.CommentsCount,
				Author: authorResponse{
					ID: first.Author.ID, Username: first.Author.Username, AvatarURL: first.Author.AvatarURL,
				},
			},
			{
				ID:                second.Card.VideoID,
				Title:             second.Card.Title,
				Description:       second.Card.Description,
				PlayURL:           second.Card.PlayURL,
				PlayFileName:      second.Card.PlayFileName,
				PlayOriginalName:  second.Card.PlayOriginalName,
				CoverURL:          second.Card.CoverURL,
				CoverFileName:     second.Card.CoverFileName,
				CoverOriginalName: second.Card.CoverOriginalName,
				PublishedAt:       second.Card.PublishedAt,
				LikesCount:        second.Stat.LikesCount,
				CommentsCount:     second.Stat.CommentsCount,
				Author: authorResponse{
					ID: second.Author.ID, Username: second.Author.Username, AvatarURL: second.Author.AvatarURL,
				},
			},
		},
		NextCursor: "next-page-token",
	}

	if !reflect.DeepEqual(response, want) {
		t.Fatalf("DTO 映射错误 got=%+v want=%+v", response, want)
	}
	if response.Items[0].Author == response.Items[1].Author {
		t.Fatal("条目作者必须来自各自的作者数据")
	}
}

// 测试目标：验证空结果映射为非 nil 空切片并序列化为空数组
// 预期效果：JSON 为 items 空数组而不是 null，next_cursor 被省略
func TestFeedItemsResponseEmptyResult(t *testing.T) {
	response := feedItemsResponseFromResult(applicationfeed.FeedResult{})

	if response.Items == nil {
		t.Fatal("空结果必须映射为非 nil 切片")
	}
	if len(response.Items) != 0 {
		t.Fatalf("空结果条目数量错误 got=%d want=0", len(response.Items))
	}
	if response.NextCursor != "" {
		t.Fatalf("空结果不应携带下一页游标 got=%q", response.NextCursor)
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if got := string(data); got != `{"items":[]}` {
		t.Fatalf("空结果 JSON 错误 got=%s want=%s", got, `{"items":[]}`)
	}
}

// 测试目标：验证 next_cursor 的省略规则只由取值决定
// 预期效果：空游标省略字段，非空游标按原值原样输出
func TestFeedItemsResponseNextCursorOmission(t *testing.T) {
	cases := []struct {
		name       string
		nextCursor string
		wantJSON   string
	}{
		{name: "空游标省略字段", nextCursor: "", wantJSON: `{"items":[]}`},
		{name: "非空游标输出字段", nextCursor: "cursor-token-1", wantJSON: `{"items":[],"next_cursor":"cursor-token-1"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := feedItemsResponseFromResult(applicationfeed.FeedResult{NextCursor: testCase.nextCursor})

			data, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("序列化失败: %v", err)
			}
			if got := string(data); got != testCase.wantJSON {
				t.Fatalf("JSON 契约错误 got=%s want=%s", got, testCase.wantJSON)
			}
		})
	}
}
