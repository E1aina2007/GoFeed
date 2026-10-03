package infrafeed

import (
	"context"
	"errors"
	"fmt"

	domainfeed "gofeed/internal/domain/feed"
	"gofeed/internal/video"
)

type PublicVideoStateReader interface {
	GetPublicVideoStates(context.Context, []uint) ([]video.PublicVideoState, error)
}

type PublicStateReader struct {
	videos PublicVideoStateReader
}

var _ domainfeed.PublicCardStateReader = (*PublicStateReader)(nil)

func NewPublicStateReader(videos PublicVideoStateReader) *PublicStateReader {
	return &PublicStateReader{videos: videos}
}

func (r *PublicStateReader) BatchGetPublicCardStates(ctx context.Context, ids []uint) (map[uint]domainfeed.FeedPageItem, error) {
	if r.videos == nil {
		return nil, domainfeed.ErrUnavailable
	}
	rows, err := r.videos.GetPublicVideoStates(ctx, ids)
	if err != nil {
		if errors.Is(err, video.ErrInvalidPublishedVideoBatch) {
			return nil, domainfeed.ErrInvalidCardBatch
		}
		return nil, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	states := make(map[uint]domainfeed.FeedPageItem, len(rows))
	for _, row := range rows {
		states[row.ID] = domainfeed.FeedPageItem{VideoID: row.ID, AuthorID: row.AuthorID, PublishedAt: row.PublishedAt}
	}
	return states, nil
}
