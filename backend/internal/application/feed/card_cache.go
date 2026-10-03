package applicationfeed

import (
	"context"
	"errors"

	domainfeed "gofeed/internal/domain/feed"
)

var (
	ErrCardCacheUnavailable = errors.New("feed card cache unavailable")
	ErrInvalidCachedCard    = errors.New("invalid cached feed card")
)

type CachedCards struct {
	Cards        map[uint]domainfeed.FeedCard
	InvalidCount int
}

type CardCacheWrite struct {
	Stored           int
	SkippedOversized int
}

type CardCache interface {
	GetCards(context.Context, []uint) (CachedCards, error)
	SetCards(context.Context, []domainfeed.FeedCard) (CardCacheWrite, error)
	DeleteCards(context.Context, []uint) error
}

// NormalizeCardIDs 忽略零标识并保序去重，最多接受 51 个有效标识
func NormalizeCardIDs(ids []uint) ([]uint, error) {
	unique := make([]uint, 0, min(len(ids), domainfeed.MaxCardBatchSize))
	seen := make(map[uint]struct{}, cap(unique))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		if len(unique) == domainfeed.MaxCardBatchSize {
			return nil, domainfeed.ErrInvalidCardBatch
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique, nil
}

func ValidateCachedCard(card domainfeed.FeedCard) error {
	if card.VideoID == 0 || card.PublishedAt.IsZero() || card.PlayURL == "" ||
		card.PlayFileName == "" || card.PlayOriginalName == "" || card.CoverURL == "" ||
		card.CoverFileName == "" || card.CoverOriginalName == "" {
		return ErrInvalidCachedCard
	}
	return nil
}
