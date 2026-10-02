package infracachefeed

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	applicationfeed "gofeed/internal/application/feed"

	redisdriver "github.com/redis/go-redis/v9"
)

const (
	pagePayloadVersion    = 1
	defaultPageTTL        = 30 * time.Second
	defaultPageTimeout    = 100 * time.Millisecond
	defaultMaxPayloadSize = 16 * 1024
	maxPageTTL            = 5 * time.Minute
	maxPageTimeout        = time.Second
	maxPayloadSize        = 64 * 1024
)

type StringCache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, expiration time.Duration) error
}

type PageCacheOptions struct {
	TTL              time.Duration
	OperationTimeout time.Duration
	MaxPayloadBytes  int
}

type PageCache struct {
	client  StringCache
	options PageCacheOptions
}

var _ applicationfeed.PageCache = (*PageCache)(nil)

// NewPageCache 创建页缓存适配器并校验配置
func NewPageCache(client StringCache, options PageCacheOptions) (*PageCache, error) {
	if options.TTL == 0 {
		options.TTL = defaultPageTTL
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = defaultPageTimeout
	}
	if options.MaxPayloadBytes == 0 {
		options.MaxPayloadBytes = defaultMaxPayloadSize
	}
	if options.TTL < 0 || options.TTL > maxPageTTL ||
		options.OperationTimeout < 0 || options.OperationTimeout > maxPageTimeout ||
		options.MaxPayloadBytes < 0 || options.MaxPayloadBytes > maxPayloadSize {
		return nil, fmt.Errorf("invalid feed page cache options")
	}
	if client == nil {
		return nil, applicationfeed.ErrPageCacheUnavailable
	}
	return &PageCache{client: client, options: options}, nil
}

// GetPage 读取并校验缓存页，返回页数据及是否命中
func (c *PageCache) GetPage(ctx context.Context, query applicationfeed.PageCacheQuery) (applicationfeed.CachedPage, bool, error) {
	if err := query.Validate(); err != nil {
		return applicationfeed.CachedPage{}, false, err
	}
	if c == nil || c.client == nil {
		return applicationfeed.CachedPage{}, false, applicationfeed.ErrPageCacheUnavailable
	}
	opCtx, cancel := context.WithTimeout(ctx, c.options.OperationTimeout)
	defer cancel()
	value, err := c.client.Get(opCtx, pageKey(query))
	if opCtx.Err() != nil {
		return applicationfeed.CachedPage{}, false, fmt.Errorf("%w: %w", applicationfeed.ErrPageCacheUnavailable, opCtx.Err())
	}
	if errors.Is(err, redisdriver.Nil) {
		return applicationfeed.CachedPage{}, false, nil
	}
	if err != nil {
		return applicationfeed.CachedPage{}, false, fmt.Errorf("%w: %w", applicationfeed.ErrPageCacheUnavailable, err)
	}
	page, err := decodePage(value, query, c.options.MaxPayloadBytes)
	if err != nil {
		return applicationfeed.CachedPage{}, false, err
	}
	return page, true, nil
}

// SetPage 校验并写入轻量页缓存
func (c *PageCache) SetPage(ctx context.Context, query applicationfeed.PageCacheQuery, page applicationfeed.CachedPage) error {
	if err := page.Validate(query); err != nil {
		return err
	}
	if c == nil || c.client == nil {
		return applicationfeed.ErrPageCacheUnavailable
	}
	value, err := encodePage(page, c.options.MaxPayloadBytes)
	if err != nil {
		return err
	}
	opCtx, cancel := context.WithTimeout(ctx, c.options.OperationTimeout)
	defer cancel()
	err = c.client.Set(opCtx, pageKey(query), value, c.options.TTL)
	if opCtx.Err() != nil {
		return fmt.Errorf("%w: %w", applicationfeed.ErrPageCacheUnavailable, opCtx.Err())
	}
	if err != nil {
		return fmt.Errorf("%w: %w", applicationfeed.ErrPageCacheUnavailable, err)
	}
	return nil
}

// pageKey 根据查询场景、页大小和游标生成缓存键
func pageKey(query applicationfeed.PageCacheQuery) string {
	position := "start"
	if query.Cursor != nil {
		position = query.Cursor.PublishedAt.UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatUint(uint64(query.Cursor.VideoID), 10)
	}
	return "gofeed:feed:page:v" + strconv.Itoa(pagePayloadVersion) + ":" + string(query.Scene) +
		":s" + strconv.Itoa(applicationfeed.TimelinePageSortVersion) + ":l" + strconv.Itoa(query.Limit) + ":" + position
}
