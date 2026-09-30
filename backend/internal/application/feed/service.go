package applicationfeed

import (
	"context"

	domainfeed "gofeed/internal/domain/feed"
)

const defaultFeedLimit = 20

type Service struct {
	repo domainfeed.Repository
}

func New(repo domainfeed.Repository) *Service {
	return &Service{repo: repo}
}

type FeedRequest struct {
	Scene  domainfeed.Scene
	Cursor string
	Limit  int
}

type FeedResult struct {
	Items      []domainfeed.FeedItem
	NextCursor string
}

// GetFeed 负责场景、分页、批量数据组装及下一页游标，仓储只提供结构化读模型
func (s *Service) GetFeed(ctx context.Context, req FeedRequest) (FeedResult, error) {
	if req.Scene == "" {
		req.Scene = domainfeed.DefaultScene
	}
	switch req.Scene {
	case domainfeed.SceneTimeline:
	case domainfeed.SceneFollowing, domainfeed.SceneHot, domainfeed.SceneRecommend:
		return FeedResult{}, domainfeed.ErrSceneNotEnabled
	default:
		return FeedResult{}, domainfeed.ErrInvalidScene
	}
	if req.Limit == 0 {
		req.Limit = defaultFeedLimit
	}
	if req.Limit < 1 || req.Limit > domainfeed.MaxLimit {
		return FeedResult{}, domainfeed.ErrInvalidLimit
	}
	cursor, err := decodeTimelineCursor(req.Cursor)
	if err != nil {
		return FeedResult{}, err
	}
	if s.repo == nil {
		return FeedResult{}, domainfeed.ErrUnavailable
	}
	page, err := s.repo.ListTimelinePage(ctx, cursor, req.Limit+1)
	if err != nil {
		return FeedResult{}, err
	}
	hasMore := len(page.Items) > req.Limit
	if hasMore {
		page.Items = page.Items[:req.Limit]
	}
	items, err := s.assembleFeedItems(ctx, page)
	if err != nil {
		return FeedResult{}, err
	}
	result := FeedResult{Items: items}
	if hasMore {
		last := page.Items[len(page.Items)-1]
		result.NextCursor, err = encodeTimelineCursor(&domainfeed.TimelineCursor{
			PublishedAt: last.PublishedAt,
			VideoID:     last.VideoID,
		})
		if err != nil {
			return FeedResult{}, err
		}
	}
	return result, nil
}

func (s *Service) assembleFeedItems(ctx context.Context, page domainfeed.TimelinePage) ([]domainfeed.FeedItem, error) {
	items := make([]domainfeed.FeedItem, 0, len(page.Items))
	if len(page.Items) == 0 {
		return items, nil
	}
	videoIDs := make([]uint, 0, len(page.Items))
	authorIDs := make([]uint, 0, len(page.Items))
	seenVideos := make(map[uint]struct{}, len(page.Items))
	seenAuthors := make(map[uint]struct{}, len(page.Items))
	for _, entry := range page.Items {
		card, ok := page.Cards[entry.VideoID]
		if !ok || entry.VideoID == 0 || entry.PublishedAt.IsZero() ||
			card.VideoID != entry.VideoID || card.AuthorID != entry.AuthorID || !card.PublishedAt.Equal(entry.PublishedAt) {
			return nil, domainfeed.ErrInvalidReadResult
		}
		if _, ok := seenVideos[entry.VideoID]; !ok {
			seenVideos[entry.VideoID] = struct{}{}
			videoIDs = append(videoIDs, entry.VideoID)
		}
		if _, ok := seenAuthors[entry.AuthorID]; !ok {
			seenAuthors[entry.AuthorID] = struct{}{}
			authorIDs = append(authorIDs, entry.AuthorID)
		}
	}
	stats, err := s.repo.BatchGetStats(ctx, videoIDs)
	if err != nil {
		return nil, err
	}
	authors, err := s.repo.BatchGetAuthors(ctx, authorIDs)
	if err != nil {
		return nil, err
	}
	for _, entry := range page.Items {
		author, ok := authors[entry.AuthorID]
		if !ok {
			return nil, domainfeed.ErrInvalidReadResult
		}
		items = append(items, domainfeed.FeedItem{
			Card:   page.Cards[entry.VideoID],
			Author: author,
			Stat:   stats[entry.VideoID],
		})
	}
	return items, nil
}
