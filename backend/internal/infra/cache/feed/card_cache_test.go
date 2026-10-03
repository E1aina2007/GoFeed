package infracachefeed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"
)

type scriptFunc func(context.Context, string, []string, ...any) (any, error)

func (f scriptFunc) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return f(ctx, script, keys, args...)
}
func adapterCard(id uint) domainfeed.FeedCard {
	return domainfeed.FeedCard{VideoID: id, PublishedAt: time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC), PlayURL: "/v", PlayFileName: "v", PlayOriginalName: "v.mp4", CoverURL: "/c", CoverFileName: "c", CoverOriginalName: "c.jpg"}
}
func encodedCard(t *testing.T, id uint) string {
	t.Helper()
	card := adapterCard(id)
	b, err := json.Marshal(cardPayload{Version: 1, Card: &cachedCard{FeedCard: card, AuthorID: &card.AuthorID}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 测试目标：验证缓存载荷的严格结构和公开字段边界
// 预期效果：错误版本、额外字段、尾随对象、错误标识和缺失媒体字段均视为无效，零作者保持兼容
func TestCardPayloadContract(t *testing.T) {
	good := encodedCard(t, 1)
	for name, body := range map[string]string{
		"version":        strings.Replace(good, `"version":1`, `"version":2`, 1),
		"unknown":        strings.Replace(good, `"version":1`, `"version":1,"extra":true`, 1),
		"nested_unknown": strings.Replace(good, `"VideoID":1`, `"VideoID":1,"extra":true`, 1),
		"trailing":       good + `{}`, "wrong_id": encodedCard(t, 2), "null": `{"version":1,"card":null}`,
		"missing_media":  strings.Replace(good, `"CoverOriginalName":"c.jpg"`, `"CoverOriginalName":""`, 1),
		"missing_time":   strings.Replace(good, `"PublishedAt":"2026-08-01T01:00:00Z"`, `"PublishedAt":null`, 1),
		"missing_author": strings.Replace(good, `,"AuthorID":0`, ``, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCard(body, 1, 16384); !errors.Is(err, applicationfeed.ErrInvalidCachedCard) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if card, err := decodeCard(good, 1, 16384); err != nil || card.AuthorID != 0 {
		t.Fatalf("card=%v err=%v", card, err)
	}
	if _, err := decodeCard(good, 1, len(good)-1); err == nil {
		t.Fatal("载荷上限未生效")
	}
}

// 测试目标：验证混合命中批次和超大写入的有界操作
// 预期效果：单次批量调用保留好值，坏值计数，超大卡片跳过且不阻止其他卡片回填
func TestCardAdapterMixedBatchAndOversized(t *testing.T) {
	calls := 0
	cache, err := NewCardCache(scriptFunc(func(_ context.Context, _ string, keys []string, args ...any) (any, error) {
		calls++
		if len(keys) != 4 || keys[0] != "gofeed:feed:card:v1:2" || args[0] != 16384 {
			t.Fatalf("keys=%v args=%v", keys, args)
		}
		return []any{encodedCard(t, 2), nil, int64(1), "broken"}, nil
	}), CardCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := cache.GetCards(t.Context(), []uint{0, 2, 1, 2, 3, 4})
	if err != nil || len(result.Cards) != 1 || result.InvalidCount != 2 || calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
	cache, err = NewCardCache(scriptFunc(func(_ context.Context, _ string, keys []string, args ...any) (any, error) {
		if len(keys) != 1 || keys[0] != "gofeed:feed:card:v1:1" || args[0] != int64(30000) {
			t.Fatalf("keys=%v ttl=%v", keys, args[0])
		}
		return int64(1), nil
	}), CardCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	large := adapterCard(2)
	large.Description = strings.Repeat("x", 16384)
	written, err := cache.SetCards(t.Context(), []domainfeed.FeedCard{large, adapterCard(1), adapterCard(1)})
	if err != nil || written.Stored != 1 || written.SkippedOversized != 1 {
		t.Fatalf("written=%+v err=%v", written, err)
	}
}

// 测试目标：验证批量上限、空调用、超时和错误响应
// 预期效果：无效批次不访问 Redis，取消保留错误身份，错误批量响应不能被当成命中
func TestCardAdapterBoundsAndFailures(t *testing.T) {
	calls := 0
	cache, _ := NewCardCache(scriptFunc(func(context.Context, string, []string, ...any) (any, error) { calls++; return nil, nil }), CardCacheOptions{})
	ids := make([]uint, 52)
	for i := range ids {
		ids[i] = uint(i + 1)
	}
	if _, err := cache.GetCards(t.Context(), ids); !errors.Is(err, domainfeed.ErrInvalidCardBatch) {
		t.Fatal(err)
	}
	if _, err := cache.GetCards(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := cache.DeleteCards(t.Context(), nil); err != nil || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	for _, reply := range []any{nil, "bad", []any{}, []any{nil, nil}} {
		cache, _ := NewCardCache(scriptFunc(func(context.Context, string, []string, ...any) (any, error) { return reply, nil }), CardCacheOptions{})
		if _, err := cache.GetCards(t.Context(), []uint{1}); !errors.Is(err, applicationfeed.ErrCardCacheUnavailable) {
			t.Fatalf("reply=%v err=%v", reply, err)
		}
	}
	cache, _ = NewCardCache(scriptFunc(func(ctx context.Context, _ string, _ []string, _ ...any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}), CardCacheOptions{OperationTimeout: time.Millisecond})
	if _, err := cache.GetCards(t.Context(), []uint{1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cache.GetCards(ctx, []uint{1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	for _, options := range []CardCacheOptions{{TTL: -1}, {TTL: 6 * time.Minute}, {OperationTimeout: -1}, {OperationTimeout: 2 * time.Second}, {MaxPayloadBytes: -1}, {MaxPayloadBytes: 65537}} {
		if _, err := NewCardCache(scriptFunc(func(context.Context, string, []string, ...any) (any, error) { return nil, nil }), options); err == nil {
			t.Fatalf("options=%+v", options)
		}
	}
}

// 测试目标：在真实 Redis 上批量访问卡片并持有精确清理键
// 预期效果：只操作随机前缀内的已知键，不扫描共享实例
func (c *namespacedPageCache) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	full := make([]string, len(keys))
	c.mu.Lock()
	for i, key := range keys {
		full[i] = c.prefix + key
		c.keys = append(c.keys, full[i])
	}
	c.mu.Unlock()
	return c.client.Eval(ctx, script, full, args...)
}

// 测试目标：验证真实 Redis 卡片往返、超大值保护、损坏和默认 TTL 自然过期
// 预期效果：真实命中及删除可观测，超大值不返回给驱动，30 秒默认 TTL 到期后缺失并精确清理
func TestRealRedisCardCacheLifecycle(t *testing.T) {
	_, wrapped := realRedisCache(t, PageCacheOptions{})
	cache, err := NewCardCache(wrapped, CardCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	write, err := cache.SetCards(t.Context(), []domainfeed.FeedCard{adapterCard(1), adapterCard(2)})
	if err != nil || write.Stored != 2 {
		t.Fatalf("write=%v err=%v", write, err)
	}
	read, err := cache.GetCards(t.Context(), []uint{1, 2, 3})
	if err != nil || len(read.Cards) != 2 {
		t.Fatalf("read=%v err=%v", read, err)
	}
	ttl, err := wrapped.client.Eval(t.Context(), "return redis.call('PTTL',KEYS[1])", []string{wrapped.prefix + cardKeys([]uint{1})[0]})
	if err != nil || ttl.(int64) < 29000 || ttl.(int64) > 30000 {
		t.Fatalf("TTL=%v err=%v", ttl, err)
	}
	if err := wrapped.Set(t.Context(), cardKeys([]uint{2})[0], strings.Repeat("x", 16385), time.Minute); err != nil {
		t.Fatal(err)
	}
	read, err = cache.GetCards(t.Context(), []uint{1, 2})
	if err != nil || len(read.Cards) != 1 || read.InvalidCount != 1 {
		t.Fatalf("read=%v err=%v", read, err)
	}
	if err := cache.DeleteCards(t.Context(), []uint{2, 2}); err != nil {
		t.Fatal(err)
	}
	read, err = cache.GetCards(t.Context(), []uint{2})
	if err != nil || len(read.Cards) != 0 || read.InvalidCount != 0 {
		t.Fatalf("read=%v err=%v", read, err)
	}
	select {
	case <-time.After(31 * time.Second):
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	read, err = cache.GetCards(t.Context(), []uint{1})
	if err != nil || len(read.Cards) != 0 {
		t.Fatalf("TTL 未自然过期 read=%v err=%v", read, err)
	}
	t.Log("真实 Redis 两条卡片命中，超大值被标记，精确删除与默认 30s 自然过期通过")
}
