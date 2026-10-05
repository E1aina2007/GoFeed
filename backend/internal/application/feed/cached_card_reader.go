package applicationfeed

import (
	"context"
	"fmt"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

type CardCacheObservation struct {
	Result   string
	Count    int
	Duration time.Duration
}

type CardCacheObserver func(CardCacheObservation)

type CachedCardReader struct {
	source   domainfeed.CardReader            // 卡片未命中时批量回源
	states   domainfeed.PublicCardStateReader // 缓存命中也须校验当前公开状态
	cache    CardCache
	observer CardCacheObserver
	slots    chan struct{} // 缓存操作并发名额，可与页缓存共享
}

var _ domainfeed.CardReader = (*CachedCardReader)(nil)

func NewCachedCardReader(source domainfeed.CardReader, states domainfeed.PublicCardStateReader, cache CardCache, observer CardCacheObserver) (*CachedCardReader, error) {
	if source == nil || states == nil || cache == nil {
		return nil, domainfeed.ErrUnavailable
	}
	return &CachedCardReader{source: source, states: states, cache: cache, observer: observer, slots: make(chan struct{}, maxCacheOperations)}, nil
}

// WithCardCache 只装配已启用的页缓存路径，并共享缓存操作名额
func WithCardCache(cache CardCache, states domainfeed.PublicCardStateReader, observer CardCacheObserver) Option {
	return func(s *Service) {
		if s.timelineCache == nil {
			return
		}
		reader, err := NewCachedCardReader(s.timelineCache.cards, states, cache, observer)
		if err != nil {
			return
		}
		reader.slots = s.timelineCache.cacheSlots
		s.timelineCache.cards = reader
	}
}

// BatchGetCards 先验证当前公开状态，缓存缺失时仅批量读取缺失卡片
func (r *CachedCardReader) BatchGetCards(ctx context.Context, ids []uint) (map[uint]domainfeed.FeedCard, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	unique, err := NormalizeCardIDs(ids)
	if err != nil {
		return nil, err
	}
	result := make(map[uint]domainfeed.FeedCard, len(unique))
	if len(unique) == 0 {
		return result, nil
	}
	states, err := r.states.BatchGetPublicCardStates(ctx, unique)
	if err != nil {
		return nil, err
	}
	visible := make([]uint, 0, len(unique))
	for _, id := range unique {
		if state, ok := states[id]; ok {
			if state.VideoID != id || state.PublishedAt.IsZero() {
				return nil, domainfeed.ErrInvalidReadResult
			}
			visible = append(visible, id)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	if len(visible) == 0 {
		return result, nil
	}
	started := time.Now()
	var cached CachedCards
	err = r.operation(ctx, func() error {
		var readErr error
		cached, readErr = r.cache.GetCards(ctx, visible)
		return readErr
	})
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, ctx.Err())
	}
	refill := err == nil
	if err != nil {
		r.observe("read_failed", len(visible), started)
	} else if cached.InvalidCount > 0 {
		r.observe("invalid_payload", cached.InvalidCount, started)
	}
	missing := make([]uint, 0, len(visible))
	for _, id := range visible {
		card, ok := cached.Cards[id]
		if err == nil && ok && cardMatchesState(card, states[id]) {
			// 保持 MySQL 直读的时间表示，避免缓存 UTC 编码改变 HTTP 响应
			card.PublishedAt = states[id].PublishedAt
			result[id] = card
		} else {
			missing = append(missing, id)
		}
	}
	if len(result) > 0 {
		r.observe("hit", len(result), started)
	}
	if len(missing) == 0 {
		return result, nil
	}
	r.observe("miss", len(missing), started)
	started = time.Now()
	fresh, err := r.source.BatchGetCards(ctx, missing)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, ctx.Err())
	}
	r.observe("mysql_read", len(missing), started)
	toStore := make([]domainfeed.FeedCard, 0, len(fresh))
	for _, id := range missing {
		if card, ok := fresh[id]; ok && cardMatchesState(card, states[id]) {
			result[id] = card
			toStore = append(toStore, card)
		}
	}
	if refill && len(toStore) > 0 {
		started = time.Now()
		var written CardCacheWrite
		err := r.operation(ctx, func() error {
			var writeErr error
			written, writeErr = r.cache.SetCards(ctx, toStore)
			return writeErr
		})
		if err != nil {
			r.observe("write_failed", len(toStore), started)
		} else {
			if written.Stored > 0 {
				r.observe("write_ok", written.Stored, started)
			}
			if written.SkippedOversized > 0 {
				r.observe("skipped_oversized", written.SkippedOversized, started)
			}
		}
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, ctx.Err())
	}
	return result, nil
}

func cardMatchesState(card domainfeed.FeedCard, state domainfeed.FeedPageItem) bool {
	return ValidateCachedCard(card) == nil && card.VideoID == state.VideoID && card.AuthorID == state.AuthorID && card.PublishedAt.Equal(state.PublishedAt)
}

func (r *CachedCardReader) operation(ctx context.Context, operation func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
		return operation()
	default:
		r.observe("cache_busy", 1, time.Now())
		return ErrCardCacheUnavailable
	}
}

func (r *CachedCardReader) observe(result string, count int, started time.Time) {
	if r.observer != nil {
		r.observer(CardCacheObservation{Result: result, Count: count, Duration: time.Since(started)})
	}
}
