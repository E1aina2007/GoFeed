package infrafeed

import (
	"context"
	"errors"
	"fmt"

	domainfeed "gofeed/internal/domain/feed"
	infravideo "gofeed/internal/infra/persistence/video"
	"gofeed/internal/video"
)

// PublishedVideoBatchReader 只读取当前公开视频，既有实体仅用于外层适配
type PublishedVideoBatchReader interface {
	GetPublishedByIDs(ctx context.Context, ids []uint) ([]infravideo.Video, error)
}

type CardReader struct {
	videos PublishedVideoBatchReader
}

var (
	_ PublishedVideoBatchReader = (*video.Repository)(nil)
	_ domainfeed.CardReader     = (*CardReader)(nil)
)

func NewCardReader(videos PublishedVideoBatchReader) *CardReader {
	return &CardReader{videos: videos}
}

func (r *CardReader) BatchGetCards(ctx context.Context, videoIDs []uint) (map[uint]domainfeed.FeedCard, error) {
	capacity := min(len(videoIDs), domainfeed.MaxCardBatchSize)
	queried := make([]uint, 0, capacity)
	requested := make(map[uint]struct{}, capacity)
	for _, id := range videoIDs {
		if id == 0 {
			continue
		}
		if _, ok := requested[id]; ok {
			continue
		}
		if len(queried) == domainfeed.MaxCardBatchSize {
			return nil, domainfeed.ErrInvalidCardBatch
		}
		requested[id] = struct{}{}
		queried = append(queried, id)
	}
	cards := make(map[uint]domainfeed.FeedCard, len(queried))
	if len(queried) == 0 {
		return cards, nil
	}
	if r.videos == nil {
		return nil, domainfeed.ErrUnavailable
	}
	rows, err := r.videos.GetPublishedByIDs(ctx, queried)
	if err != nil {
		if errors.Is(err, infravideo.ErrInvalidPublishedVideoBatch) {
			return nil, domainfeed.ErrInvalidCardBatch
		}
		return nil, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	for _, row := range rows {
		if _, ok := requested[row.ID]; !ok || !infravideo.IsPublicVideo(row) {
			continue
		}
		cards[row.ID] = feedCardFromVideo(row)
	}
	return cards, nil
}

// feedCardFromVideo 只转换已通过公开视频判断的实体，由调用方保证 PublishedAt 非空
func feedCardFromVideo(row infravideo.Video) domainfeed.FeedCard {
	return domainfeed.FeedCard{
		VideoID:           row.ID,
		AuthorID:          row.AuthorID,
		Title:             row.Title,
		Description:       row.Description,
		PlayURL:           row.PlayURL,
		PlayFileName:      row.PlayFileName,
		PlayOriginalName:  row.PlayOriginalName,
		CoverURL:          row.CoverURL,
		CoverFileName:     row.CoverFileName,
		CoverOriginalName: row.CoverOriginalName,
		PublishedAt:       *row.PublishedAt,
	}
}
