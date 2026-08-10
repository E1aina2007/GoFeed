package video

import (
	"context"
	"errors"
	"testing"
	"time"
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
