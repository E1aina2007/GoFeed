package video

import (
	"context"
	"fmt"
	"time"

	domainvideo "gofeed/internal/domain/video"
)

type ListResult struct {
	Items      []domainvideo.VideoItem
	NextCursor string
}

type Service struct {
	repository       domainvideo.Reader
	authorReader     domainvideo.AuthorReader
	engagementReader domainvideo.EngagementReader
}

func New(repository domainvideo.Reader, authorReader domainvideo.AuthorReader, engagementReaders ...domainvideo.EngagementReader) *Service {
	var engagementReader domainvideo.EngagementReader
	if len(engagementReaders) > 0 {
		engagementReader = engagementReaders[0]
	}
	return &Service{repository: repository, authorReader: authorReader, engagementReader: engagementReader}
}

func (s *Service) GetPublished(ctx context.Context, id uint) (domainvideo.VideoItem, error) {
	if id == 0 {
		return domainvideo.VideoItem{}, domainvideo.ErrInvalidVideoID
	}
	if s.repository == nil {
		return domainvideo.VideoItem{}, domainvideo.ErrRepositoryUnavailable
	}

	video, err := s.repository.GetPublishedByID(ctx, id)
	if err != nil {
		return domainvideo.VideoItem{}, err
	}
	return s.toVideoItem(ctx, video)
}

func (s *Service) GetPublishedVideoList(ctx context.Context, authorID uint, encodedCursor string, limit int) (ListResult, error) {
	if s.repository == nil {
		return ListResult{}, domainvideo.ErrRepositoryUnavailable
	}

	limit, err := normalizeLimit(limit)
	if err != nil {
		return ListResult{}, err
	}
	scope := publicCursorScope(authorID)
	cursor, err := decodeCursor(encodedCursor)
	if err != nil {
		return ListResult{}, err
	}
	if err := validateCursorScope(cursor, scope); err != nil {
		return ListResult{}, err
	}

	var position *domainvideo.ListPosition
	if cursor != nil {
		position = &domainvideo.ListPosition{PublishedAt: cursor.PublishedAt, ID: cursor.ID}
	}
	videos, err := s.repository.GetPublishedVideoList(ctx, authorID, position, limit+1)
	if err != nil {
		return ListResult{}, err
	}
	return s.buildListResponse(ctx, videos, limit, scope)
}

func (s *Service) buildListResponse(ctx context.Context, videos []domainvideo.PublicVideo, limit int, scope cursorScope) (ListResult, error) {
	// 仓储查询已按公开条件过滤，这里再做一次实体级检查，防止替代实现或并发快照
	// 把残缺记录映射成半完整的公开响应
	videos = filterPublicVideos(videos)
	hasMore := len(videos) > limit
	if hasMore {
		videos = videos[:limit]
	}

	engagements, err := s.engagements(ctx, videos)
	if err != nil {
		return ListResult{}, err
	}
	authors, err := s.listAuthors(ctx, videos)
	if err != nil {
		return ListResult{}, err
	}
	items := make([]domainvideo.VideoItem, 0, len(videos))
	for i := range videos {
		item := videoItem(videos[i], authors[videos[i].AuthorID])
		applyEngagement(&item, engagements[videos[i].ID])
		items = append(items, item)
	}

	response := ListResult{Items: items}
	if hasMore {
		last := videos[len(videos)-1]
		if last.PublishedAt == nil {
			return ListResult{}, fmt.Errorf("published video %d has no publication time", last.ID)
		}
		next, err := encodeCursor(&cursor{
			Version:     currentCursorVersion,
			Kind:        scope.kind,
			AuthorID:    scope.authorID,
			PublishedAt: *last.PublishedAt,
			ID:          last.ID,
		})
		if err != nil {
			return ListResult{}, err
		}
		response.NextCursor = next
	}
	return response, nil
}

func (s *Service) listAuthors(ctx context.Context, videos []domainvideo.PublicVideo) (map[uint]domainvideo.Author, error) {
	if len(videos) == 0 {
		return map[uint]domainvideo.Author{}, nil
	}
	if s.authorReader == nil {
		return nil, domainvideo.ErrAuthorReaderUnavailable
	}
	ids := make([]uint, 0, len(videos))
	seen := make(map[uint]struct{}, len(videos))
	for i := range videos {
		id := videos[i].AuthorID
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return s.authorReader.GetPublicAuthors(ctx, ids)
}

func (s *Service) toVideoItem(ctx context.Context, video *domainvideo.PublicVideo) (domainvideo.VideoItem, error) {
	if video == nil || !domainvideo.IsPublicVideo(*video) {
		return domainvideo.VideoItem{}, domainvideo.ErrVideoNotFound
	}
	if s.authorReader == nil {
		return domainvideo.VideoItem{}, domainvideo.ErrAuthorReaderUnavailable
	}

	author, err := s.authorReader.GetPublicAuthor(ctx, video.AuthorID)
	if err != nil {
		return domainvideo.VideoItem{}, err
	}
	engagements, err := s.engagements(ctx, []domainvideo.PublicVideo{*video})
	if err != nil {
		return domainvideo.VideoItem{}, err
	}
	item := videoItem(*video, author)
	applyEngagement(&item, engagements[video.ID])
	return item, nil
}

func filterPublicVideos(videos []domainvideo.PublicVideo) []domainvideo.PublicVideo {
	filtered := make([]domainvideo.PublicVideo, 0, len(videos))
	for _, item := range videos {
		if domainvideo.IsPublicVideo(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (s *Service) engagements(ctx context.Context, videos []domainvideo.PublicVideo) (map[uint]domainvideo.EngagementCounts, error) {
	// 计数值以互动关系表聚合为唯一事实源，实体计数值列已删除，未聚合到的视频保持零值
	counts := make(map[uint]domainvideo.EngagementCounts, len(videos))
	if s.engagementReader == nil || len(videos) == 0 {
		return counts, nil
	}
	ids := make([]uint, 0, len(videos))
	for _, item := range videos {
		ids = append(ids, item.ID)
	}
	actual, err := s.engagementReader.GetEngagementCounts(ctx, ids)
	if err != nil {
		// 统计查询失败按服务不可用整体失败，禁止用零计数或实体列兜底值伪装成功响应
		// 双重 %w 同时保留哨兵错误与底层原因，供状态映射与日志追溯
		return nil, fmt.Errorf("%w: %w", domainvideo.ErrEngagementUnavailable, err)
	}
	for _, id := range ids {
		if value, ok := actual[id]; ok {
			counts[id] = value
		}
	}
	return counts, nil
}

func applyEngagement(item *domainvideo.VideoItem, counts domainvideo.EngagementCounts) {
	item.LikesCount = counts.LikesCount
	item.CommentsCount = counts.CommentsCount
}

func videoItem(video domainvideo.PublicVideo, author domainvideo.Author) domainvideo.VideoItem {
	return domainvideo.VideoItem{
		ID:                video.ID,
		Title:             video.Title,
		Description:       video.Description,
		PlayURL:           video.PlayURL,
		PlayFileName:      video.PlayFileName,
		PlayOriginalName:  video.PlayOriginalName,
		CoverURL:          video.CoverURL,
		CoverFileName:     video.CoverFileName,
		CoverOriginalName: video.CoverOriginalName,
		PublishedAt:       valueOrZero(video.PublishedAt),
		Author:            author,
	}
}

func valueOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
