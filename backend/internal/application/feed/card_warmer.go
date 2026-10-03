package applicationfeed

import (
	"context"
	"fmt"

	domainfeed "gofeed/internal/domain/feed"
)

type CardWarmupResult string

const (
	CardWarmed           CardWarmupResult = "warmed"
	CardSkippedNotPublic CardWarmupResult = "skipped_not_public"
	CardSkippedOversized CardWarmupResult = "skipped_oversized"
)

type CardWarmer struct {
	reader domainfeed.CardReader
	cache  CardCache
}

func NewCardWarmer(reader domainfeed.CardReader, cache CardCache) (*CardWarmer, error) {
	if reader == nil || cache == nil {
		return nil, domainfeed.ErrUnavailable
	}
	return &CardWarmer{reader: reader, cache: cache}, nil
}

// WarmCard 每次从事实源读取当前卡片，可重复覆盖同一视频键
func (w *CardWarmer) WarmCard(ctx context.Context, videoID uint) (CardWarmupResult, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if videoID == 0 {
		return "", domainfeed.ErrInvalidCardBatch
	}
	cards, err := w.reader.BatchGetCards(ctx, []uint{videoID})
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	card, visible := cards[videoID]
	if !visible {
		if err := w.cache.DeleteCards(ctx, []uint{videoID}); err != nil {
			return "", err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return CardSkippedNotPublic, nil
	}
	if card.VideoID != videoID || ValidateCachedCard(card) != nil {
		return "", domainfeed.ErrInvalidReadResult
	}
	written, err := w.cache.SetCards(ctx, []domainfeed.FeedCard{card})
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if written.Stored == 1 && written.SkippedOversized == 0 {
		return CardWarmed, nil
	}
	if written.Stored == 0 && written.SkippedOversized == 1 {
		return CardSkippedOversized, nil
	}
	return "", fmt.Errorf("%w: unexpected card warmup write result", ErrCardCacheUnavailable)
}
