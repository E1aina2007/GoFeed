package video

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// 测试目标：模拟视频数据读取和写入依赖
// 预期效果：记录服务层查询参数并返回预设视频数据
type fakeVideoReader struct {
	getVideo          *Video
	getErr            error
	listVideos        []Video
	listErr           error
	listAuthorID      uint
	listCursor        *Cursor
	listLimit         int
	getAny            *Video
	created           *Video
	createErr         error
	drafts            map[uint]*Video
	attachErr         error
	publishErr        error
	discardErr        error
	discardedID       uint
	discardedAuthorID uint
	mineVideos        []Video
	deletedID         uint
	deletedAuthorID   uint
	deleteErr         error
}

// 测试目标：模拟已发布视频详情读取
// 预期效果：返回预设的视频和错误
func (r *fakeVideoReader) GetPublishedByID(_ context.Context, _ uint) (*Video, error) {
	return r.getVideo, r.getErr
}

// 测试目标：模拟已发布视频列表读取
// 预期效果：记录查询参数并返回预设的视频列表
func (r *fakeVideoReader) GetPublishedVideoList(_ context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error) {
	r.listAuthorID = authorID
	r.listCursor = cursor
	r.listLimit = limit
	return r.listVideos, r.listErr
}

// 测试目标：模拟视频创建操作
// 预期效果：记录待发布视频并在成功时回填视频标识
func (r *fakeVideoReader) Create(_ context.Context, video *Video) error {
	r.created = video
	if r.createErr == nil && video != nil {
		video.ID = 7
		if video.Status == VideoStatusDraft {
			if r.drafts == nil {
				r.drafts = make(map[uint]*Video)
			}
			r.drafts[video.ID] = video
		}
	}
	return r.createErr
}

// 测试目标：模拟将服务端保存的媒体绑定到草稿
// 预期效果：仅作者本人的 draft 可写入对应种类的元数据
func (r *fakeVideoReader) UpdateDraftMedia(_ context.Context, draftID, authorID uint, kind MediaKind, saved SavedFile, originalName string) error {
	if r.attachErr != nil {
		return r.attachErr
	}
	draft := r.drafts[draftID]
	if draft == nil {
		return gorm.ErrRecordNotFound
	}
	if draft.AuthorID != authorID {
		return ErrNotAuthor
	}
	if draft.Status != VideoStatusDraft {
		return ErrDraftNotWritable
	}
	switch kind {
	case MediaVideo:
		if draft.PlayURL != "" {
			return ErrDraftNotWritable
		}
		draft.PlayURL, draft.PlayFileName, draft.PlayOriginalName = saved.PublicURL, saved.FileName, originalName
	case MediaCover:
		if draft.CoverURL != "" {
			return ErrDraftNotWritable
		}
		draft.CoverURL, draft.CoverFileName, draft.CoverOriginalName = saved.PublicURL, saved.FileName, originalName
	default:
		return ErrInvalidMedia
	}
	return nil
}

// 测试目标：模拟草稿发布状态转换
// 预期效果：只有完整的作者草稿可转换为 published
func (r *fakeVideoReader) UpdateDraftPublication(_ context.Context, draftID, authorID uint) (*Video, error) {
	if r.publishErr != nil {
		return nil, r.publishErr
	}
	draft := r.drafts[draftID]
	if draft == nil {
		return nil, gorm.ErrRecordNotFound
	}
	if draft.AuthorID != authorID {
		return nil, ErrNotAuthor
	}
	if draft.Status != VideoStatusDraft {
		return nil, ErrDraftNotWritable
	}
	if draft.PlayURL == "" || draft.CoverURL == "" || draft.PlayFileName == "" || draft.CoverFileName == "" ||
		draft.PlayOriginalName == "" || draft.CoverOriginalName == "" {
		return nil, ErrDraftIncomplete
	}
	draft.Status = VideoStatusPublished
	draft.PublishedAt = timePtr(time.Now())
	return draft, nil
}

// 测试目标：模拟草稿丢弃状态转换
// 预期效果：仅作者的 draft 可进入 purging 且重试保持幂等
func (r *fakeVideoReader) UpdateDraftDiscard(_ context.Context, draftID, authorID uint) (*Video, error) {
	r.discardedID = draftID
	r.discardedAuthorID = authorID
	if r.discardErr != nil {
		return nil, r.discardErr
	}
	draft := r.drafts[draftID]
	if draft == nil {
		return nil, gorm.ErrRecordNotFound
	}
	if draft.AuthorID != authorID {
		return nil, ErrNotAuthor
	}
	switch draft.Status {
	case VideoStatusDraft:
		draft.Status = VideoStatusPurging
		draft.PurgeToken = nil
		draft.PurgeLeaseUntil = nil
		draft.PlayPurgedAt = nil
		draft.CoverPurgedAt = nil
	case VideoStatusPurging:
	default:
		return nil, ErrDraftNotWritable
	}
	return draft, nil
}

// 测试目标：模拟任意状态的视频读取
// 预期效果：返回预设的视频和错误
func (r *fakeVideoReader) GetByID(_ context.Context, id uint) (*Video, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.getAny != nil {
		return r.getAny, nil
	}
	if draft := r.drafts[id]; draft != nil {
		return draft, nil
	}
	return nil, gorm.ErrRecordNotFound
}

// 测试目标：模拟作者视频列表读取
// 预期效果：记录查询参数并返回预设的作者视频列表
func (r *fakeVideoReader) GetAuthorVideoList(_ context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error) {
	r.listAuthorID = authorID
	r.listCursor = cursor
	r.listLimit = limit
	return r.mineVideos, r.listErr
}

// 测试目标：模拟已发布视频软删除操作
// 预期效果：记录被删除的视频与作者标识并返回预设错误
func (r *fakeVideoReader) DeletePublishedVideo(_ context.Context, id, authorID uint) error {
	r.deletedID = id
	r.deletedAuthorID = authorID
	return r.deleteErr
}

// 测试目标：模拟作者资料读取依赖
// 预期效果：返回预设作者资料并记录每位作者的查询次数
type fakeAuthorReader struct {
	authors map[uint]Author
	err     error
	calls   map[uint]int
}

// 测试目标：模拟公开作者资料读取
// 预期效果：返回预设作者资料并累计查询次数
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

// 测试目标：验证服务层组装已发布视频详情和作者资料
// 预期效果：返回完整视频字段及正确作者资料
func TestServiceGetPublished(t *testing.T) {
	// 1 准备一条已发布视频和对应作者资料
	// 2 调用服务层详情查询
	// 3 验证视频字段和作者资料被正确组装
	publishedAt := time.Date(2026, time.August, 4, 8, 0, 0, 0, time.UTC)
	service := NewService(
		&fakeVideoReader{getVideo: &Video{
			ID: 1, AuthorID: 2, Title: "title", PlayURL: "play", CoverURL: "cover", PublishedAt: timePtr(publishedAt),
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

// 测试目标：验证公开视频列表使用额外记录生成分页游标
// 预期效果：仓储多取一条记录，响应截断到指定数量并复用作者资料
func TestServiceListPublishedUsesExtraRecordForCursor(t *testing.T) {
	// 1 准备三条按发布时间倒序排列的视频
	// 2 使用每页数量为 2 调用列表查询
	// 3 验证仓储多取一条记录以判断是否存在下一页
	// 4 验证响应只返回两项且游标指向第二项
	// 5 验证同一作者资料在一次列表查询中只读取一次
	publishedAt := time.Date(2026, time.August, 4, 8, 0, 0, 0, time.UTC)
	repository := &fakeVideoReader{listVideos: []Video{
		{ID: 3, AuthorID: 2, PublishedAt: timePtr(publishedAt)},
		{ID: 2, AuthorID: 2, PublishedAt: timePtr(publishedAt.Add(-time.Second))},
		{ID: 1, AuthorID: 4, PublishedAt: timePtr(publishedAt.Add(-2 * time.Second))},
	}}
	authors := &fakeAuthorReader{authors: map[uint]Author{
		2: {ID: 2, Username: "first"},
		4: {ID: 4, Username: "second"},
	}}
	service := NewService(repository, authors)

	response, err := service.GetPublishedVideoList(context.Background(), 9, "", 2)
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

// 测试目标：验证公开视频列表填充作者资料
// 预期效果：每个列表项均带有对应作者的公开信息
func TestServiceListPublishedPopulatesAuthor(t *testing.T) {
	// 1 准备一条视频与对应作者资料
	// 2 查询列表
	// 3 验证作者资料透出到列表项（防止局部变量遮蔽回归）
	publishedAt := time.Date(2026, time.August, 4, 8, 0, 0, 0, time.UTC)
	service := NewService(
		&fakeVideoReader{listVideos: []Video{{ID: 1, AuthorID: 2, PublishedAt: timePtr(publishedAt)}}},
		&fakeAuthorReader{authors: map[uint]Author{2: {ID: 2, Username: "author"}}},
	)

	response, err := service.GetPublishedVideoList(context.Background(), 0, "", 10)
	if err != nil {
		t.Fatalf("查询视频列表失败 error=%v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Author.ID != 2 || response.Items[0].Author.Username != "author" {
		t.Fatalf("列表项应透出作者资料 got=%#v", response.Items)
	}
}

// 测试目标：验证公开视频列表拒绝非法游标、数量和视频标识
// 预期效果：每种非法输入均返回对应的参数错误
func TestServiceListPublishedRejectsInvalidInput(t *testing.T) {
	// 1 传入无法解码的游标
	// 2 传入超过最大值的每页数量
	// 3 传入值为零的视频标识
	// 4 验证服务层分别返回对应的输入错误
	service := NewService(&fakeVideoReader{}, &fakeAuthorReader{})

	if _, err := service.GetPublishedVideoList(context.Background(), 0, "not-a-cursor", 20); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("非法游标未被拒绝 got error=%v want error=%v", err, ErrInvalidCursor)
	}
	if _, err := service.GetPublishedVideoList(context.Background(), 0, "", MaxListLimit+1); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("非法 limit 未被拒绝 got error=%v want error=%v", err, ErrInvalidLimit)
	}
	if _, err := service.GetPublished(context.Background(), 0); !errors.Is(err, ErrInvalidVideoID) {
		t.Fatalf("非法视频 ID 未被拒绝 got error=%v want error=%v", err, ErrInvalidVideoID)
	}
}

// 测试目标：验证服务层创建草稿时只保存可编辑元数据
// 预期效果：草稿归属当前用户、状态为 draft，媒体字段保持为空
func TestServiceCreateDraft(t *testing.T) {
	repository := &fakeVideoReader{}
	service := NewService(repository, &fakeAuthorReader{})

	draft, err := service.CreateDraft(context.Background(), 2, DraftRequest{Title: "  title  ", Description: "  description  "})
	if err != nil {
		t.Fatalf("创建草稿失败 error=%v", err)
	}
	if draft.ID != 7 || draft.Status != VideoStatusDraft || repository.created == nil {
		t.Fatalf("草稿创建结果错误 draft=%#v created=%#v", draft, repository.created)
	}
	if repository.created.Title != "title" || repository.created.Description != "description" || repository.created.PlayURL != "" || repository.created.CoverURL != "" {
		t.Fatalf("草稿字段错误 got=%#v", repository.created)
	}
}

// 测试目标：验证作者可读取草稿媒体完成快照而不取得媒体地址
// 预期效果：draft 和 purging 均返回完成标识，其他作者和非草稿状态被拒绝
func TestServiceGetDraft(t *testing.T) {
	draft := &Video{
		ID: 7, AuthorID: 2, Title: "草稿", Description: "说明", Status: VideoStatusDraft,
		PlayURL: "/static/videos/2/20260810/a.mp4", PlayFileName: "a.mp4", PlayOriginalName: "我的视频.mp4",
	}
	repository := &fakeVideoReader{drafts: map[uint]*Video{7: draft, 8: {ID: 8, AuthorID: 2, Status: VideoStatusPurging}, 9: {ID: 9, AuthorID: 2, Status: VideoStatusPublished}}}
	service := NewService(repository, &fakeAuthorReader{})

	item, err := service.GetDraft(context.Background(), 7, 2)
	if err != nil {
		t.Fatalf("查询草稿失败 error=%v", err)
	}
	if item.ID != 7 || item.Status != VideoStatusDraft || !item.HasVideo || item.HasCover || item.PlayOriginalName != "我的视频.mp4" {
		t.Fatalf("草稿快照错误 got=%#v", item)
	}
	if item, err := service.GetDraft(context.Background(), 8, 2); err != nil || item.Status != VideoStatusPurging {
		t.Fatalf("清扫中的草稿应可查询 item=%#v error=%v", item, err)
	}
	if _, err := service.GetDraft(context.Background(), 7, 3); !errors.Is(err, ErrNotAuthor) {
		t.Fatalf("跨作者查询未被拒绝 error=%v", err)
	}
	if _, err := service.GetDraft(context.Background(), 9, 2); !errors.Is(err, ErrVideoNotFound) {
		t.Fatalf("已发布视频不应通过草稿查询读取 error=%v", err)
	}
}

// 测试目标：验证丢弃草稿会持久化转入 purging 并允许重试
// 预期效果：媒体仍由清扫器处理，跨作者和已发布状态均不会进入清扫
func TestServiceDiscardDraft(t *testing.T) {
	lease := time.Now().Add(time.Hour)
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	draft := &Video{
		ID: 7, AuthorID: 2, Status: VideoStatusDraft,
		PlayURL: "/static/videos/2/20260810/a.mp4", PlayFileName: "a.mp4", PlayOriginalName: "我的视频.mp4",
		PurgeToken: &token, PurgeLeaseUntil: &lease, PlayPurgedAt: &lease,
	}
	published := &Video{ID: 8, AuthorID: 2, Status: VideoStatusPublished}
	repository := &fakeVideoReader{drafts: map[uint]*Video{7: draft, 8: published}}
	service := NewService(repository, &fakeAuthorReader{})

	item, err := service.DiscardDraft(context.Background(), 7, 2)
	if err != nil {
		t.Fatalf("丢弃草稿失败 error=%v", err)
	}
	if item.Status != VideoStatusPurging || !item.HasVideo || item.HasCover || draft.PurgeToken != nil || draft.PurgeLeaseUntil != nil || draft.PlayPurgedAt != nil {
		t.Fatalf("草稿丢弃状态错误 item=%#v draft=%#v", item, draft)
	}
	if repository.discardedID != 7 || repository.discardedAuthorID != 2 {
		t.Fatalf("草稿丢弃参数错误 id=%d author=%d", repository.discardedID, repository.discardedAuthorID)
	}
	if item, err := service.DiscardDraft(context.Background(), 7, 2); err != nil || item.Status != VideoStatusPurging {
		t.Fatalf("重复丢弃应保持成功 item=%#v error=%v", item, err)
	}
	if _, err := service.DiscardDraft(context.Background(), 7, 3); !errors.Is(err, ErrNotAuthor) {
		t.Fatalf("跨作者丢弃未被拒绝 error=%v", err)
	}
	if _, err := service.DiscardDraft(context.Background(), 8, 2); !errors.Is(err, ErrDraftNotWritable) {
		t.Fatalf("已发布视频不应进入草稿清扫 error=%v", err)
	}
}

// 测试目标：验证草稿媒体只能由服务端保存结果绑定
// 预期效果：绑定后写入保存结果的地址和名称，跨用户草稿被拒绝
func TestServiceAttachDraftMedia(t *testing.T) {
	draft := &Video{ID: 7, AuthorID: 2, Status: VideoStatusDraft}
	repository := &fakeVideoReader{drafts: map[uint]*Video{7: draft}}
	service := NewService(repository, &fakeAuthorReader{})
	saved := SavedFile{PublicURL: "/static/videos/2/20260810/a.mp4", FileName: "a.mp4"}

	if err := service.UpdateDraftMedia(context.Background(), 7, 2, MediaVideo, saved, "我的 视频.mp4"); err != nil {
		t.Fatalf("绑定视频失败 error=%v", err)
	}
	if draft.PlayURL != saved.PublicURL || draft.PlayFileName != saved.FileName || draft.PlayOriginalName != "我的 视频.mp4" {
		t.Fatalf("视频媒体元数据错误 got=%#v", draft)
	}
	if err := service.UpdateDraftMedia(context.Background(), 7, 3, MediaVideo, saved, "a.mp4"); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("伪造当前用户目录的跨用户绑定未被拒绝 error=%v", err)
	}
}

// 测试目标：验证完整草稿可原子发布且不完整草稿被拒绝
// 预期效果：发布只切换草稿状态，缺少任一媒体时返回草稿不完整错误
func TestServicePublishDraft(t *testing.T) {
	complete := &Video{
		ID: 7, AuthorID: 2, Status: VideoStatusDraft,
		PlayURL: "/static/videos/2/20260810/a.mp4", PlayFileName: "a.mp4", PlayOriginalName: "我的视频.mp4",
		CoverURL: "/static/covers/2/20260810/c.png", CoverFileName: "c.png", CoverOriginalName: "封面.png",
	}
	repository := &fakeVideoReader{drafts: map[uint]*Video{7: complete, 8: {ID: 8, AuthorID: 2, Status: VideoStatusDraft}}}
	authors := &fakeAuthorReader{authors: map[uint]Author{2: {ID: 2, Username: "author"}}}
	service := NewService(repository, authors)

	item, err := service.UpdateDraftPublication(context.Background(), 7, 2)
	if err != nil {
		t.Fatalf("发布草稿失败 error=%v", err)
	}
	if complete.Status != VideoStatusPublished || complete.PublishedAt == nil || complete.PublishedAt.IsZero() || item.PlayOriginalName != "我的视频.mp4" {
		t.Fatalf("草稿发布结果错误 draft=%#v item=%#v", complete, item)
	}
	_, err = service.UpdateDraftPublication(context.Background(), 8, 2)
	if !errors.Is(err, ErrDraftIncomplete) {
		t.Fatalf("不完整草稿未被拒绝 got error=%v want error=%v", err, ErrDraftIncomplete)
	}
}

// 测试目标：验证视频删除服务检查作者权限
// 预期效果：非作者删除被拒绝，作者本人删除并传递正确视频标识
func TestServiceDeleteChecksAuthor(t *testing.T) {
	// 1 非作者删除返回权限错误
	// 2 作者本人删除成功
	repository := &fakeVideoReader{getAny: &Video{ID: 1, AuthorID: 2, Status: VideoStatusPublished}}
	service := NewService(repository, &fakeAuthorReader{})

	if err := service.DeleteVideo(context.Background(), 1, 3); !errors.Is(err, ErrNotAuthor) {
		t.Fatalf("非作者删除未被拒绝 got error=%v want error=%v", err, ErrNotAuthor)
	}
	if err := service.DeleteVideo(context.Background(), 1, 2); err != nil {
		t.Fatalf("作者删除失败 error=%v", err)
	}
	if repository.deletedID != 1 || repository.deletedAuthorID != 2 {
		t.Fatalf("删除参数错误 id=%d author=%d", repository.deletedID, repository.deletedAuthorID)
	}
}

// 测试目标：验证草稿和清扫中草稿不能进入已发布视频删除路径
// 预期效果：服务层返回视频不存在且不调用已发布视频软删除
func TestServiceDeleteRejectsNonPublishedVideo(t *testing.T) {
	for _, status := range []string{VideoStatusDraft, VideoStatusPurging} {
		t.Run(status, func(t *testing.T) {
			repository := &fakeVideoReader{getAny: &Video{ID: 1, AuthorID: 2, Status: status}}
			service := NewService(repository, &fakeAuthorReader{})

			if err := service.DeleteVideo(context.Background(), 1, 2); !errors.Is(err, ErrVideoNotFound) {
				t.Fatalf("非公开视频删除错误 got=%v", err)
			}
			if repository.deletedID != 0 {
				t.Fatalf("非公开视频不应调用软删除 id=%d", repository.deletedID)
			}
		})
	}
}

// 测试目标：验证删除服务转换仓储的记录不存在错误
// 预期效果：删除不存在的视频时返回统一的视频不存在错误
func TestServiceDeleteNotFound(t *testing.T) {
	// 仓储返回记录不存在时，服务层应转换为统一的视频不存在错误
	service := NewService(&fakeVideoReader{getErr: gorm.ErrRecordNotFound}, &fakeAuthorReader{})

	if err := service.DeleteVideo(context.Background(), 99, 1); !errors.Is(err, ErrVideoNotFound) {
		t.Fatalf("删除不存在视频的映射错误 got error=%v want error=%v", err, ErrVideoNotFound)
	}
}

// 测试目标：验证个人视频列表使用额外记录生成分页游标
// 预期效果：仓储多取一条记录，响应返回指定数量和下一页游标
func TestServiceListMinePassesExtraRecordForCursor(t *testing.T) {
	// 与公开列表一致，仓储多取一条记录用于判断下一页
	publishedAt := time.Date(2026, time.August, 4, 8, 0, 0, 0, time.UTC)
	repository := &fakeVideoReader{mineVideos: []Video{
		{ID: 3, AuthorID: 2, PublishedAt: timePtr(publishedAt)},
		{ID: 2, AuthorID: 2, PublishedAt: timePtr(publishedAt.Add(-time.Second))},
		{ID: 1, AuthorID: 2, PublishedAt: timePtr(publishedAt.Add(-2 * time.Second))},
	}}
	authors := &fakeAuthorReader{authors: map[uint]Author{2: {ID: 2, Username: "author"}}}
	service := NewService(repository, authors)

	response, err := service.GetMyVideoList(context.Background(), 2, "", 2)
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
