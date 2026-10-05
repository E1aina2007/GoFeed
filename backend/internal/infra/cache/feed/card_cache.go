package infracachefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"
)

// 批量回复只包含有界字符串；超大值返回整数标记，避免传给驱动
const getCardsScript = `
local result = {}
for i, key in ipairs(KEYS) do
  if redis.call('STRLEN', key) > tonumber(ARGV[1]) then
    result[i] = 1
  else
    result[i] = redis.call('GET', key) or false
  end
end
return result`

const setCardsScript = `
for i, key in ipairs(KEYS) do
  redis.call('SET', key, ARGV[i + 1], 'PX', ARGV[1])
end
return #KEYS`

const deleteCardsScript = `return redis.call('DEL', unpack(KEYS))`

type ScriptCache interface {
	Eval(context.Context, string, []string, ...any) (any, error)
}

type CardCacheOptions = PageCacheOptions

type CardCache struct {
	client  ScriptCache
	options CardCacheOptions
}

type cardPayload struct {
	Version int         `json:"version"`
	Card    *cachedCard `json:"card"`
}

type cachedCard struct {
	domainfeed.FeedCard
	AuthorID *uint `json:"AuthorID"` // 区分字段缺失与作者 ID 为 0
}

var _ applicationfeed.CardCache = (*CardCache)(nil)

// NewCardCache 复用缓存操作选项，创建有界批量卡片适配器
func NewCardCache(client ScriptCache, options CardCacheOptions) (*CardCache, error) {
	if client == nil {
		return nil, applicationfeed.ErrCardCacheUnavailable
	}
	if options.TTL == 0 {
		options.TTL = defaultPageTTL
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = defaultPageTimeout
	}
	if options.MaxPayloadBytes == 0 {
		options.MaxPayloadBytes = defaultMaxPayloadSize
	}
	if options.TTL < time.Millisecond || options.TTL > maxPageTTL || options.OperationTimeout < 0 ||
		options.OperationTimeout > maxPageTimeout || options.MaxPayloadBytes < 0 || options.MaxPayloadBytes > maxPayloadSize {
		return nil, fmt.Errorf("invalid feed card cache options")
	}
	return &CardCache{client: client, options: options}, nil
}

func (c *CardCache) GetCards(ctx context.Context, ids []uint) (applicationfeed.CachedCards, error) {
	unique, err := applicationfeed.NormalizeCardIDs(ids)
	if err != nil {
		return applicationfeed.CachedCards{}, err
	}
	result := applicationfeed.CachedCards{Cards: make(map[uint]domainfeed.FeedCard, len(unique))}
	if len(unique) == 0 {
		return result, nil
	}
	raw, err := c.eval(ctx, getCardsScript, cardKeys(unique), c.options.MaxPayloadBytes)
	if err != nil {
		return result, err
	}
	values, ok := raw.([]any)
	if !ok || len(values) != len(unique) {
		return result, applicationfeed.ErrCardCacheUnavailable
	}
	for i, value := range values {
		if value == nil {
			continue
		}
		encoded, ok := value.(string)
		if !ok {
			result.InvalidCount++
			continue
		}
		card, err := decodeCard(encoded, unique[i], c.options.MaxPayloadBytes)
		if err != nil {
			result.InvalidCount++
			continue
		}
		result.Cards[card.VideoID] = card
	}
	return result, nil
}

func (c *CardCache) SetCards(ctx context.Context, cards []domainfeed.FeedCard) (applicationfeed.CardCacheWrite, error) {
	result := applicationfeed.CardCacheWrite{}
	ids := make([]uint, 0, min(len(cards), domainfeed.MaxCardBatchSize))
	values := make([]any, 1, cap(ids)+1)
	values[0] = c.options.TTL.Milliseconds()
	seen := make(map[uint]struct{}, cap(ids))
	for _, card := range cards {
		if err := applicationfeed.ValidateCachedCard(card); err != nil {
			return result, err
		}
		if _, exists := seen[card.VideoID]; exists {
			continue
		}
		if len(seen) == domainfeed.MaxCardBatchSize {
			return result, domainfeed.ErrInvalidCardBatch
		}
		seen[card.VideoID] = struct{}{}
		card.PublishedAt = card.PublishedAt.UTC()
		authorID := card.AuthorID
		encoded, err := json.Marshal(cardPayload{Version: 1, Card: &cachedCard{FeedCard: card, AuthorID: &authorID}})
		if err != nil {
			return result, fmt.Errorf("%w: %w", applicationfeed.ErrInvalidCachedCard, err)
		}
		if len(encoded) > c.options.MaxPayloadBytes {
			result.SkippedOversized++
			continue
		}
		ids = append(ids, card.VideoID)
		values = append(values, string(encoded))
	}
	if len(ids) == 0 {
		return result, nil
	}
	raw, err := c.eval(ctx, setCardsScript, cardKeys(ids), values...)
	if err != nil {
		return result, err
	}
	stored, ok := raw.(int64)
	if !ok || stored != int64(len(ids)) {
		return result, applicationfeed.ErrCardCacheUnavailable
	}
	result.Stored = int(stored)
	return result, nil
}

func (c *CardCache) DeleteCards(ctx context.Context, ids []uint) error {
	unique, err := applicationfeed.NormalizeCardIDs(ids)
	if err != nil || len(unique) == 0 {
		return err
	}
	_, err = c.eval(ctx, deleteCardsScript, cardKeys(unique))
	return err
}

func (c *CardCache) eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	opCtx, cancel := context.WithTimeout(ctx, c.options.OperationTimeout)
	defer cancel()
	result, err := c.client.Eval(opCtx, script, keys, args...)
	if opCtx.Err() != nil {
		return nil, fmt.Errorf("%w: %w", applicationfeed.ErrCardCacheUnavailable, opCtx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", applicationfeed.ErrCardCacheUnavailable, err)
	}
	return result, nil
}

func cardKeys(ids []uint) []string {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, "gofeed:feed:card:v1:"+strconv.FormatUint(uint64(id), 10))
	}
	return keys
}

func decodeCard(encoded string, id uint, maxBytes int) (domainfeed.FeedCard, error) {
	if len(encoded) > maxBytes {
		return domainfeed.FeedCard{}, applicationfeed.ErrInvalidCachedCard
	}
	var payload cardPayload
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return domainfeed.FeedCard{}, applicationfeed.ErrInvalidCachedCard
	}
	if err := decoder.Decode(new(any)); err != io.EOF || payload.Version != 1 || payload.Card == nil || payload.Card.AuthorID == nil || payload.Card.VideoID != id {
		return domainfeed.FeedCard{}, applicationfeed.ErrInvalidCachedCard
	}
	card := payload.Card.FeedCard
	card.AuthorID = *payload.Card.AuthorID
	if err := applicationfeed.ValidateCachedCard(card); err != nil {
		return domainfeed.FeedCard{}, err
	}
	return card, nil
}
