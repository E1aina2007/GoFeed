package applicationfeed

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

const (
	maxCachedFeedReads = 32
	maxCacheOperations = 16
)

var errPageCacheBusy = errors.New("feed page cache capacity exhausted")

type CacheObservation struct {
	Result   string
	Duration time.Duration
}

type CacheObserver func(CacheObservation)

type timelineCache struct {
	cache      PageCache
	cards      domainfeed.CardReader
	observer   CacheObserver
	readSlots  chan struct{}
	cacheSlots chan struct{}
}

// WithPageCache 接入可选页缓存和当前公开卡片读取
func WithPageCache(cache PageCache, cards domainfeed.CardReader, observer CacheObserver) Option {
	return func(service *Service) {
		if cache == nil || cards == nil {
			return
		}
		service.timelineCache = &timelineCache{
			cache: cache, cards: cards, observer: observer,
			readSlots:  make(chan struct{}, maxCachedFeedReads),
			cacheSlots: make(chan struct{}, maxCacheOperations),
		}
	}
}

func (c *timelineCache) acquireRead(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	select {
	case c.readSlots <- struct{}{}:
		return nil
	default:
		c.observe("read_busy", time.Now())
		return fmt.Errorf("%w: feed read capacity exhausted", domainfeed.ErrUnavailable)
	}
}

func (c *timelineCache) releaseRead() {
	<-c.readSlots
}

// readTimelinePage 对缓存页校验当前公开状态，失效时按原游标整页回源
func (s *Service) readTimelinePage(ctx context.Context, cursor *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
	c := s.timelineCache
	if c == nil {
		return s.repo.ListTimelinePage(ctx, cursor, limit+1)
	}
	query := PageCacheQuery{Scene: domainfeed.SceneTimeline, Cursor: cursor, Limit: limit}
	refill := cursor != nil
	if cursor != nil {
		started := time.Now()
		cached, hit, err := c.getPage(ctx, query)
		if ctx.Err() != nil {
			return domainfeed.TimelinePage{}, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, ctx.Err())
		}
		switch {
		case err != nil:
			result := "read_failed"
			refill = false
			if errors.Is(err, ErrInvalidCachedPage) {
				result, refill = "invalid_payload", true
			} else if errors.Is(err, errPageCacheBusy) {
				result = "cache_busy"
			}
			c.observe(result, started)
		case !hit:
			c.observe("miss", started)
		default:
			if err := cached.Validate(query); err != nil {
				c.observe("invalid_payload", started)
				break
			}
			ids := make([]uint, 0, len(cached.Items))
			for _, item := range cached.Items {
				ids = append(ids, item.VideoID)
			}
			cards, err := c.cards.BatchGetCards(ctx, ids)
			if err != nil {
				c.observe("card_read_failed", started)
				return domainfeed.TimelinePage{}, err
			}
			page := domainfeed.TimelinePage{Items: cached.Items, Cards: cards}
			if pageCardsMatch(page) {
				c.observe("hit", started)
				return page, nil
			}
			c.observe("stale", started)
		}
	} else {
		c.observe("first_page", time.Now())
	}

	started := time.Now()
	page, err := s.repo.ListTimelinePage(ctx, cursor, limit+1)
	if err != nil {
		c.observe("mysql_failed", started)
		return domainfeed.TimelinePage{}, err
	}
	c.observe("mysql_read", started)
	if !refill {
		return page, nil
	}
	cached := CachedPage{Items: page.Items}
	if err := cached.Validate(query); err != nil || !pageCardsMatch(page) {
		return domainfeed.TimelinePage{}, domainfeed.ErrInvalidReadResult
	}
	started = time.Now()
	if err := c.setPage(ctx, query, cached); err != nil {
		c.observe("write_failed", started)
	} else {
		c.observe("write_ok", started)
	}
	return page, nil
}

func pageCardsMatch(page domainfeed.TimelinePage) bool {
	for _, item := range page.Items {
		card, ok := page.Cards[item.VideoID]
		if !ok || card.VideoID != item.VideoID || card.AuthorID != item.AuthorID || !card.PublishedAt.Equal(item.PublishedAt) {
			return false
		}
	}
	return true
}

func (c *timelineCache) getPage(ctx context.Context, query PageCacheQuery) (CachedPage, bool, error) {
	select {
	case c.cacheSlots <- struct{}{}:
		defer func() { <-c.cacheSlots }()
		return c.cache.GetPage(ctx, query)
	default:
		return CachedPage{}, false, errPageCacheBusy
	}
}

func (c *timelineCache) setPage(ctx context.Context, query PageCacheQuery, page CachedPage) error {
	select {
	case c.cacheSlots <- struct{}{}:
		defer func() { <-c.cacheSlots }()
		return c.cache.SetPage(ctx, query, page)
	default:
		return errPageCacheBusy
	}
}

func (c *timelineCache) observe(result string, started time.Time) {
	if c.observer != nil {
		c.observer(CacheObservation{Result: result, Duration: time.Since(started)})
	}
}
