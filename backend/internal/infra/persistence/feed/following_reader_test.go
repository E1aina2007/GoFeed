package infrafeed

import (
	"context"
	"errors"
	"testing"
	"time"

	domainfeed "gofeed/internal/domain/feed"
	"gofeed/internal/video"
	"gorm.io/gorm"
)

type followingReaderStub struct {
	activeErr, videoErr            error
	rows                           []video.Video
	viewer                         uint
	cursor                         *video.Cursor
	limit, activeCalls, videoCalls int
	ctx                            context.Context
}

func (f *followingReaderStub) GetActiveUser(ctx context.Context, viewer uint) error {
	f.activeCalls++
	f.viewer = viewer
	f.ctx = ctx
	return f.activeErr
}

func (f *followingReaderStub) GetFollowingVideoList(ctx context.Context, viewer uint, cursor *video.Cursor, limit int) ([]video.Video, error) {
	f.videoCalls++
	f.viewer = viewer
	f.cursor = cursor
	f.limit = limit
	f.ctx = ctx
	return f.rows, f.videoErr
}

// 测试目标：先确认活动观看者再读取关注页，保留查询上下文、结构化游标与探测行
// 预期效果：缺失或注销观看者拒绝，数据库错误不可用，公开字段完整映射
func TestFollowingReaderIdentityAndMapping(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		activeErr, videoErr, want error
	}{
		{name: "ok"}, {name: "deleted", activeErr: gorm.ErrRecordNotFound, want: domainfeed.ErrUnauthenticated},
		{name: "viewer_database", activeErr: errors.New("injected viewer outage"), want: domainfeed.ErrUnavailable},
		{name: "video_database", videoErr: errors.New("injected video outage"), want: domainfeed.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := newPublicFeedVideoRow(11, 7, feedBaseTime)
			fake := &followingReaderStub{activeErr: tc.activeErr, videoErr: tc.videoErr, rows: []video.Video{row}}
			cursor := &domainfeed.FollowingCursor{PublishedAt: feedBaseTime.Add(time.Minute), VideoID: 12}
			page, err := NewFollowingReader(fake, fake).ListFollowingPage(t.Context(), 42, cursor, 3)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if fake.viewer != 42 || fake.ctx != t.Context() || fake.activeCalls != 1 {
				t.Fatalf("身份读取=%+v", fake)
			}
			if tc.activeErr != nil && fake.videoCalls != 0 {
				t.Fatal("非活动观看者不得查询视频")
			}
			if tc.want == nil && (len(page.Items) != 1 || page.Cards[11].Title != row.Title || fake.cursor.ID != 12 || fake.limit != 3) {
				t.Fatalf("映射 page=%+v reader=%+v", page, fake)
			}
		})
	}
}

// 测试目标：异常公开查询结果不能在 LIMIT 后静默丢行
// 预期效果：零值时间、缺媒体或不可见行均报不可用并保留异常读模型原因
func TestFollowingReaderRejectsInvalidRows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*video.Video)
	}{
		{"zero_time", func(v *video.Video) { v.PublishedAt = new(time.Time) }},
		{"nil_time", func(v *video.Video) { v.PublishedAt = nil }},
		{"missing_media", func(v *video.Video) { v.CoverOriginalName = "" }},
		{"draft", func(v *video.Video) { v.Status = video.VideoStatusDraft }},
		{"deleted", func(v *video.Video) { v.DeletedAt = gorm.DeletedAt{Time: feedBaseTime, Valid: true} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := newPublicFeedVideoRow(11, 7, feedBaseTime)
			tc.mutate(&row)
			fake := &followingReaderStub{rows: []video.Video{row}}
			_, err := NewFollowingReader(fake, fake).ListFollowingPage(t.Context(), 42, nil, 2)
			if !errors.Is(err, domainfeed.ErrUnavailable) || !errors.Is(err, domainfeed.ErrInvalidReadResult) {
				t.Fatalf("异常行 err=%v", err)
			}
		})
	}
}
