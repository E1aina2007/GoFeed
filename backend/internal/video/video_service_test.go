package video

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// fakeVideoReader 记录服务层传入的查询参数并返回预设视频数据
type fakeVideoReader struct {
	getVideo     *Video
	getErr       error
	listVideos   []Video
	listErr      error
	listAuthorID uint
	listCursor   *Cursor
	listLimit    int
	getAny       *Video
	created      *Video
	createErr    error
	mineVideos   []Video
	deletedID    uint
}

// GetPublishedByID 返回预设的视频详情查询结果
func (r *fakeVideoReader) GetPublishedByID(_ context.Context, _ uint) (*Video, error) {
	return r.getVideo, r.getErr
}

// ListPublished 记录列表查询参数并返回预设的视频列表
func (r *fakeVideoReader) ListPublished(_ context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error) {
	r.listAuthorID = authorID
	r.listCursor = cursor
	r.listLimit = limit
	return r.listVideos, r.listErr
}

// Create 记录待发布的视频并模拟仓储回填 ID
func (r *fakeVideoReader) Create(_ context.Context, video *Video) error {
	r.created = video
	if r.createErr == nil && video != nil {
		video.ID = 7
	}
	return r.createErr
}

// GetByID 返回预设的任意状态视频
func (r *fakeVideoReader) GetByID(_ context.Context, _ uint) (*Video, error) {
	return r.getAny, r.getErr
}

// ListByAuthor 返回预设的作者视频列表
func (r *fakeVideoReader) ListByAuthor(_ context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error) {
	r.listAuthorID = authorID
	r.listCursor = cursor
	r.listLimit = limit
	return r.mineVideos, r.listErr
}

// Delete 记录被删除的视频 ID
func (r *fakeVideoReader) Delete(_ context.Context, id uint) error {
	r.deletedID = id
	return nil
}

// fakeAuthorReader 返回预设作者资料并记录每位作者的查询次数
type fakeAuthorReader struct {
	authors map[uint]Author
	err     error
	calls   map[uint]int
}

// GetPublicAuthor 返回预设的作者公开资料
func (r *fakeAuthorReader) GetPublicAuthor(_ context.Context, id uint) (Author, error) {
	if r.calls == nil {
		r.calls = make(map[uint]int)
	}
	r.calls[id]++
	if r.err != nil {
		return Author{}, r.err
	}
	return r.authors[id], nil
}

func TestServiceGetPublished(t *testing.T) {
	// 1 准备一条已发布视频和对应作者资料
	// 2 调用服务层详情查询
	// 3 验证视频字段和作者资料被正确组装
	publishedAt := time.Date(2026, time.August, 4, 8, 0, 0, 0, time.UTC)
	service := NewService(
		&fakeVideoReader{getVideo: &Video{
			ID: 1, AuthorID: 2, Title: "title", PlayURL: "play", CoverURL: "cover", PublishedAt: publishedAt,
			PlayFileName: "clip.mp4", PlayOriginalName: "我的 clip.mp4",
			CoverFileName: "cover.png", CoverOriginalName: "封面.png",
		}},
		&fakeAuthorReader{authors: map[uint]Author{2: {ID: 2, Username: "author"}}},
	)

	item, err := service.GetPublished(context.Background(), 1)
	if err != nil {
		t.Fatalf("获取视频详情失败 服务层应返回视频及作者资料 error=%v", err)
	}
	if item.ID != 1 || item.Author.Username != "author" || !item.PublishedAt.Equal(publishedAt) {
		t.Fatalf("视频详情组装错误 got=%#v want id=1 author=author publishedAt=%s", item, publishedAt)
	}
	if item.PlayFileName != "clip.mp4" || item.PlayOriginalName != "我的 clip.mp4" ||
		item.CoverFileName != "cover.png" || item.CoverOriginalName != "封面.png" {
		t.Fatalf("媒体文件名未透出 got=%#v", item)
	}
}

func TestServiceListPublishedUsesExtraRecordForCursor(t *testing.T) {
	// 1 准备三条按发布时间倒序排列的视频
	// 2 使用 limit=2 调用列表查询
	// 3 验证仓储接收 limit+1 以判断是否存在下一页
	// 4 验证响应只返回两项且游标指向第二项
	// 5 验证同一作者资料在一次列表查询中只读取一次
	publishedAt := time.Date(2026, time.August, 4, 8, 0, 0, 0, time.UTC)
	repository := &fakeVideoReader{listVideos: []Video{
		{ID: 3, AuthorID: 2, PublishedAt: publishedAt},
		{ID: 2, AuthorID: 2, PublishedAt: publishedAt.Add(-time.Second)},
		{ID: 1, AuthorID: 4, PublishedAt: publishedAt.Add(-2 * time.Second)},
	}}
	authors := &fakeAuthorReader{authors: map[uint]Author{
		2: {ID: 2, Username: "first"},
		4: {ID: 4, Username: "second"},
	}}
	service := NewService(repository, authors)

	response, err := service.ListPublished(context.Background(), 9, "", 2)
	if err != nil {
		t.Fatalf("查询视频列表失败 服务层应生成下一页游标 error=%v", err)
	}
	if repository.listAuthorID != 9 || repository.listLimit != 3 {
		t.Fatalf("仓储查询参数错误 got authorID=%d limit=%d want authorID=9 limit=3", repository.listAuthorID, repository.listLimit)
	}
	if len(response.Items) != 2 || response.Items[1].ID != 2 || response.NextCursor == "" {
		t.Fatalf("分页响应错误 got items=%#v nextCursor=%q want two items ending at id=2 with next cursor", response.Items, response.NextCursor)
	}
	cursor, err := decodeCursor(response.NextCursor)
	if err != nil || cursor.ID != 2 || !cursor.PublishedAt.Equal(publishedAt.Add(-time.Second)) {
		t.Fatalf("下一页游标错误 got cursor=%#v error=%v want id=2 publishedAt=%s", cursor, err, publishedAt.Add(-time.Second))
	}
	if authors.calls[2] != 1 {
		t.Fatalf("作者资料重复查询 got calls=%d want calls=1", authors.calls[2])
	}
}

func TestServiceListPublishedPopulatesAuthor(t *testing.T) {
	// 1 准备一条视频与对应作者资料
	// 2 查询列表
	// 3 验证作者资料透出到列表项（防止局部变量遮蔽回归）
	publishedAt := time.Date(2026, time.August, 4, 8, 0, 0, 0, time.UTC)
	service := NewService(
		&fakeVideoReader{listVideos: []Video{{ID: 1, AuthorID: 2, PublishedAt: publishedAt}}},
		&fakeAuthorReader{authors: map[uint]Author{2: {ID: 2, Username: "author"}}},
	)

	response, err := service.ListPublished(context.Background(), 0, "", 10)
	if err != nil {
		t.Fatalf("查询视频列表失败 error=%v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Author.ID != 2 || response.Items[0].Author.Username != "author" {
		t.Fatalf("列表项应透出作者资料 got=%#v", response.Items)
	}
}

func TestServiceListPublishedRejectsInvalidInput(t *testing.T) {
	// 1 传入无法解码的游标
	// 2 传入超过最大值的 limit
	// 3 传入值为零的视频 ID
	// 4 验证服务层分别返回对应的输入错误
	service := NewService(&fakeVideoReader{}, &fakeAuthorReader{})

	if _, err := service.ListPublished(context.Background(), 0, "not-a-cursor", 20); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("非法游标未被拒绝 got error=%v want error=%v", err, ErrInvalidCursor)
	}
	if _, err := service.ListPublished(context.Background(), 0, "", MaxListLimit+1); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("非法 limit 未被拒绝 got error=%v want error=%v", err, ErrInvalidLimit)
	}
	if _, err := service.GetPublished(context.Background(), 0); !errors.Is(err, ErrInvalidVideoID) {
		t.Fatalf("非法视频 ID 未被拒绝 got error=%v want error=%v", err, ErrInvalidVideoID)
	}
}

func TestServicePublishSetsDefaultsAndValidatesURL(t *testing.T) {
	// 1 准备仓储与作者资料
	// 2 使用属于当前用户上传目录的 URL 调用发布
	// 3 验证默认状态/发布时间/计数字段与响应组装
	repository := &fakeVideoReader{}
	authors := &fakeAuthorReader{authors: map[uint]Author{2: {ID: 2, Username: "author"}}}
	service := NewService(repository, authors)

	item, err := service.Publish(context.Background(), 2, PublishRequest{
		Title:             "  title  ",
		PlayURL:           "/static/videos/2/20260810/a1b2.mp4",
		PlayFileName:      "a1b2.mp4",
		PlayOriginalName:  "我的视频.mp4",
		CoverURL:          "/static/covers/2/20260810/c3d4.png",
		CoverFileName:     "c3d4.png",
		CoverOriginalName: "封面.png",
	})
	if err != nil {
		t.Fatalf("发布失败 error=%v", err)
	}
	if repository.created == nil {
		t.Fatal("仓储未收到待发布的视频")
	}
	if repository.created.Status != VideoStatusPublished || repository.created.AuthorID != 2 {
		t.Fatalf("发布默认值错误 got=%#v", repository.created)
	}
	if repository.created.PublishedAt.IsZero() {
		t.Fatal("发布时间未设置")
	}
	if repository.created.Title != "title" {
		t.Fatalf("标题未去除首尾空白 got=%q", repository.created.Title)
	}
	if repository.created.PlayFileName != "a1b2.mp4" || repository.created.PlayOriginalName != "我的视频.mp4" ||
		repository.created.CoverFileName != "c3d4.png" || repository.created.CoverOriginalName != "封面.png" {
		t.Fatalf("媒体文件名未写入仓储 got=%#v", repository.created)
	}
	if item.ID != 7 || item.Author.Username != "author" {
		t.Fatalf("发布响应组装错误 got=%#v", item)
	}
	if item.PlayFileName != "a1b2.mp4" || item.PlayOriginalName != "我的视频.mp4" {
		t.Fatalf("发布响应未透出媒体文件名 got=%#v", item)
	}
}

func TestServicePublishRejectsForeignMediaURL(t *testing.T) {
	// 1 使用其他用户上传目录的 URL 必须被拒绝
	// 2 使用任意外链作为封面也必须被拒绝
	service := NewService(&fakeVideoReader{}, &fakeAuthorReader{})

	_, err := service.Publish(context.Background(), 2, PublishRequest{
		Title:             "title",
		PlayURL:           "/static/videos/3/20260810/a1b2.mp4",
		PlayFileName:      "a1b2.mp4",
		PlayOriginalName:  "a.mp4",
		CoverURL:          "/static/covers/2/20260810/c3d4.png",
		CoverFileName:     "c3d4.png",
		CoverOriginalName: "c.png",
	})
	if !errors.Is(err, ErrInvalidMediaURL) {
		t.Fatalf("跨用户 URL 未被拒绝 got error=%v want error=%v", err, ErrInvalidMediaURL)
	}

	_, err = service.Publish(context.Background(), 2, PublishRequest{
		Title:             "title",
		PlayURL:           "/static/videos/2/20260810/a1b2.mp4",
		PlayFileName:      "a1b2.mp4",
		PlayOriginalName:  "a.mp4",
		CoverURL:          "http://evil.example.com/c3d4.png",
		CoverFileName:     "c3d4.png",
		CoverOriginalName: "c.png",
	})
	if !errors.Is(err, ErrInvalidMediaURL) {
		t.Fatalf("任意外链未被拒绝 got error=%v want error=%v", err, ErrInvalidMediaURL)
	}
}

func TestServicePublishRejectsMismatchedFileName(t *testing.T) {
	// 请求中的实际存储文件名与媒体 URL 最后一段不一致时必须被拒绝
	service := NewService(&fakeVideoReader{}, &fakeAuthorReader{})

	_, err := service.Publish(context.Background(), 2, PublishRequest{
		Title:             "title",
		PlayURL:           "/static/videos/2/20260810/a1b2.mp4",
		PlayFileName:      "other.mp4",
		PlayOriginalName:  "a.mp4",
		CoverURL:          "/static/covers/2/20260810/c3d4.png",
		CoverFileName:     "c3d4.png",
		CoverOriginalName: "c.png",
	})
	if !errors.Is(err, ErrInvalidPublishRequest) {
		t.Fatalf("存储文件名不匹配未被拒绝 got error=%v want error=%v", err, ErrInvalidPublishRequest)
	}
}

func TestServicePublishStoresRelativePath(t *testing.T) {
	// 1 发布时提交完整 URL
	// 2 入库前必须归一化为 /static/... 相对路径，响应同样只返回相对路径
	repository := &fakeVideoReader{}
	authors := &fakeAuthorReader{authors: map[uint]Author{2: {ID: 2, Username: "author"}}}
	service := NewService(repository, authors)

	item, err := service.Publish(context.Background(), 2, PublishRequest{
		Title:             "title",
		PlayURL:           "http://localhost:8080/static/videos/2/20260810/a1b2.mp4",
		PlayFileName:      "a1b2.mp4",
		PlayOriginalName:  "a.mp4",
		CoverURL:          "https://cdn.example.com/static/covers/2/20260810/c3d4.png",
		CoverFileName:     "c3d4.png",
		CoverOriginalName: "c.png",
	})
	if err != nil {
		t.Fatalf("发布失败 error=%v", err)
	}
	if repository.created.PlayURL != "/static/videos/2/20260810/a1b2.mp4" {
		t.Fatalf("play_url 未归一化为相对路径 got=%q", repository.created.PlayURL)
	}
	if repository.created.CoverURL != "/static/covers/2/20260810/c3d4.png" {
		t.Fatalf("cover_url 未归一化为相对路径 got=%q", repository.created.CoverURL)
	}
	if item.PlayURL != "/static/videos/2/20260810/a1b2.mp4" || item.CoverURL != "/static/covers/2/20260810/c3d4.png" {
		t.Fatalf("响应未返回相对路径 got=%#v", item)
	}
}

func TestServiceDeleteChecksAuthor(t *testing.T) {
	// 1 非作者删除返回权限错误
	// 2 作者本人删除成功
	repository := &fakeVideoReader{getAny: &Video{ID: 1, AuthorID: 2}}
	service := NewService(repository, &fakeAuthorReader{})

	if err := service.Delete(context.Background(), 1, 3); !errors.Is(err, ErrNotAuthor) {
		t.Fatalf("非作者删除未被拒绝 got error=%v want error=%v", err, ErrNotAuthor)
	}
	if err := service.Delete(context.Background(), 1, 2); err != nil {
		t.Fatalf("作者删除失败 error=%v", err)
	}
	if repository.deletedID != 1 {
		t.Fatalf("删除 ID 错误 got=%d want=1", repository.deletedID)
	}
}

func TestServiceDeleteNotFound(t *testing.T) {
	// 仓储返回记录不存在时，服务层应转换为 ErrVideoNotFound
	service := NewService(&fakeVideoReader{getErr: gorm.ErrRecordNotFound}, &fakeAuthorReader{})

	if err := service.Delete(context.Background(), 99, 1); !errors.Is(err, ErrVideoNotFound) {
		t.Fatalf("删除不存在视频的映射错误 got error=%v want error=%v", err, ErrVideoNotFound)
	}
}

func TestServiceListMinePassesExtraRecordForCursor(t *testing.T) {
	// 与公开列表一致：仓储接收 limit+1 用于判断下一页
	publishedAt := time.Date(2026, time.August, 4, 8, 0, 0, 0, time.UTC)
	repository := &fakeVideoReader{mineVideos: []Video{
		{ID: 3, AuthorID: 2, PublishedAt: publishedAt},
		{ID: 2, AuthorID: 2, PublishedAt: publishedAt.Add(-time.Second)},
		{ID: 1, AuthorID: 2, PublishedAt: publishedAt.Add(-2 * time.Second)},
	}}
	authors := &fakeAuthorReader{authors: map[uint]Author{2: {ID: 2, Username: "author"}}}
	service := NewService(repository, authors)

	response, err := service.ListMine(context.Background(), 2, "", 2)
	if err != nil {
		t.Fatalf("查询我的视频失败 error=%v", err)
	}
	if repository.listLimit != 3 {
		t.Fatalf("仓储 limit 错误 got=%d want=3", repository.listLimit)
	}
	if len(response.Items) != 2 || response.Items[1].ID != 2 || response.NextCursor == "" {
		t.Fatalf("我的视频分页错误 got=%#v", response)
	}
}
