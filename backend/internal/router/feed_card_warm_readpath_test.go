package router

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"

	applicationfeed "gofeed/internal/application/feed"
	infrafeed "gofeed/internal/infra/persistence/feed"
	"gofeed/internal/video"
)

// warmReadSentinelTitle 只存在于预热缓存载荷中，MySQL 中不存在该标题
const warmReadSentinelTitle = "预热卡片哨兵标题"

// 测试目标：构造卡片缓存使用的精确键名
// 预期效果：与生产 cardKeys 的拼接规则一致，可直接用于 EXISTS 断言
func warmReadCardKey(videoID uint) string {
	return fmt.Sprintf("gofeed:feed:card:v1:%d", videoID)
}

// 测试目标：用生产同构的 CardWarmer 预热指定视频的卡片缓存
// 预期效果：每个视频都返回 warmed 并给出实际写入的精确键
func warmReadWarmCards(t *testing.T, warmer *applicationfeed.CardWarmer, videoIDs []uint) []string {
	t.Helper()
	keys := make([]string, 0, len(videoIDs))
	for _, videoID := range videoIDs {
		result, err := warmer.WarmCard(t.Context(), videoID)
		if err != nil {
			t.Fatalf("预热视频 %d 失败: %v", videoID, err)
		}
		if result != applicationfeed.CardWarmed {
			t.Fatalf("预热结果应为 warmed got=%s video=%d", result, videoID)
		}
		keys = append(keys, warmReadCardKey(videoID))
	}
	return keys
}

// 测试目标：只改写预热卡片载荷中的展示标题
// 预期效果：可见性校验字段（VideoID、AuthorID、PublishedAt）保持预热内容不变
func warmReadRewriteTitle(t *testing.T, recorder *feedScriptRecorder, key, title string) {
	t.Helper()
	payload := recorder.base.readValue(t, key)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("预热载荷不是合法 JSON: %v", err)
	}
	card, ok := decoded["card"].(map[string]any)
	if !ok {
		t.Fatalf("预热载荷缺少 card 对象: %s", payload)
	}
	for _, field := range []string{"VideoID", "AuthorID", "PublishedAt"} {
		if _, ok := card[field]; !ok {
			t.Fatalf("预热载荷缺少 %s: %s", field, payload)
		}
	}
	if _, ok := card["Title"]; !ok {
		t.Fatalf("预热载荷缺少 Title: %s", payload)
	}
	card["Title"] = title
	mutated, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("重新编码预热载荷失败: %v", err)
	}
	recorder.base.writeValue(t, key, string(mutated))
}

// 测试目标：验证预热写入的卡片被 HTTP 读路径真实使用
// 预期效果：页缓存命中时响应标题取自预热键，且读路径没有回填卡片
func TestCardWarmupRealCacheFeedsHTTPReadPath(t *testing.T) {
	env := newFeedTestEnv(t)
	cards, recorder := env.liveCardCache(t)
	events := &timelineCacheEvents{event: "feed_card_cache", counts: make(map[string]int)}
	original := log.Writer()
	log.SetOutput(io.MultiWriter(original, events))
	t.Cleanup(func() { log.SetOutput(original) })

	// 先用只开页缓存的服务建立第二页游标位置，此时卡片缓存必须保持零访问
	pageOnly, client := env.newServer(t, env.livePageCache(t))
	register(t, client, pageOnly.URL, "card_warm_read_author", "card-warm-read-password-123")
	session := login(t, client, pageOnly.URL, "card_warm_read_author", "card-warm-read-password-123")
	items, secondURL, _ := setupCachedFeedPage(t, env, pageOnly.URL, session.AccessToken, client)
	path := strings.TrimPrefix(secondURL, pageOnly.URL)
	if recorder.reads != 0 || recorder.writes != 0 {
		t.Fatalf("建立页缓存时不应访问卡片缓存 reads=%d writes=%d", recorder.reads, recorder.writes)
	}

	// 真实预热：由 CardWarmer 写入卡片缓存，并断言精确键存在
	warmer, err := applicationfeed.NewCardWarmer(infrafeed.NewCardReader(video.NewRepository(env.gdb)), cards)
	if err != nil {
		t.Fatalf("构造卡片预热器失败: %v", err)
	}
	keys := warmReadWarmCards(t, warmer, []uint{items[0].ID, items[1].ID, items[2].ID})
	if recorder.reads != 0 || recorder.writes != len(keys) {
		t.Fatalf("预热写入次数异常 reads=%d writes=%d want_writes=%d", recorder.reads, recorder.writes, len(keys))
	}
	for _, key := range keys {
		if !recorder.base.keyExists(t, key) {
			t.Fatalf("预热精确键不存在 key=%s", key)
		}
	}

	// 把第二页视频的预热标题改成哨兵值，MySQL 中仍是原标题
	target := items[1].ID
	targetKey := warmReadCardKey(target)
	if items[1].Title == warmReadSentinelTitle {
		t.Fatalf("哨兵标题不得与 MySQL 标题相同: %s", items[1].Title)
	}
	warmReadRewriteTitle(t, recorder, targetKey, warmReadSentinelTitle)

	// 页缓存与卡片缓存同时开启，第二页命中页缓存
	both, _ := env.newServer(t, env.livePageCache(t), cards)
	readsBefore, writesBefore := recorder.reads, recorder.writes
	var page feedTimelineResponse
	sqlCount := measuredQueryCount(t, env.capture, func() {
		doJSON(t, client, http.MethodGet, both.URL+path, "", nil, http.StatusOK, &page)
	})
	if len(page.Items) != 1 || page.Items[0].ID != target {
		t.Fatalf("第二页条目错误 got=%+v want=%d", page.Items, target)
	}
	if page.Items[0].Title != warmReadSentinelTitle {
		t.Fatalf("HTTP 读路径未使用预热卡片 got=%q want=%q", page.Items[0].Title, warmReadSentinelTitle)
	}
	if recorder.reads != readsBefore+1 {
		t.Fatalf("页命中应只读取一次卡片 got=%d want=%d", recorder.reads, readsBefore+1)
	}
	if recorder.writes != writesBefore {
		t.Fatalf("读路径不得回填卡片 got=%d want=%d", recorder.writes, writesBefore)
	}
	if events.snapshot()["hit"] != 1 {
		t.Fatalf("卡片命中事件错误 got=%v", events.snapshot())
	}
	if sqlCount != 4 {
		t.Fatalf("页命中 SQL 预算错误 got=%d want=4", sqlCount)
	}

	// 重复读取必须继续使用同一预热来源，证明不是一次性回填
	readsBefore, writesBefore = recorder.reads, recorder.writes
	var repeated feedTimelineResponse
	doJSON(t, client, http.MethodGet, both.URL+path, "", nil, http.StatusOK, &repeated)
	if len(repeated.Items) != 1 || repeated.Items[0].Title != warmReadSentinelTitle {
		t.Fatalf("重复读取未继续使用预热卡片 got=%+v", repeated.Items)
	}
	if recorder.reads != readsBefore+1 || recorder.writes != writesBefore {
		t.Fatalf("重复读取缓存计数异常 reads=%d writes=%d", recorder.reads, recorder.writes)
	}
	if events.snapshot()["hit"] != 2 {
		t.Fatalf("重复读取命中事件错误 got=%v", events.snapshot())
	}
	t.Logf("预热键=%v 第二页命中 SQL=%d 卡片事件=%v 响应标题=%q", keys, sqlCount, events.snapshot(), repeated.Items[0].Title)
}

// 测试目标：验证预热缓存不能让已不可见的视频重新出现在响应中
// 预期效果：视频改为非公开后页命中剔除该视频，预热键仍存在而不是被删除
func TestCardWarmupRealCacheKeepsInvisibleVideoHidden(t *testing.T) {
	env := newFeedTestEnv(t)
	cards, recorder := env.liveCardCache(t)

	pageOnly, client := env.newServer(t, env.livePageCache(t))
	register(t, client, pageOnly.URL, "card_warm_hidden_author", "card-warm-hidden-password-123")
	session := login(t, client, pageOnly.URL, "card_warm_hidden_author", "card-warm-hidden-password-123")
	items, secondURL, _ := setupCachedFeedPage(t, env, pageOnly.URL, session.AccessToken, client)
	path := strings.TrimPrefix(secondURL, pageOnly.URL)

	warmer, err := applicationfeed.NewCardWarmer(infrafeed.NewCardReader(video.NewRepository(env.gdb)), cards)
	if err != nil {
		t.Fatalf("构造卡片预热器失败: %v", err)
	}
	keys := warmReadWarmCards(t, warmer, []uint{items[0].ID, items[1].ID, items[2].ID})
	for _, key := range keys {
		if !recorder.base.keyExists(t, key) {
			t.Fatalf("预热精确键不存在 key=%s", key)
		}
	}

	// 第二页视频改为非公开状态，预热的精确键必须保留
	hiddenKey := warmReadCardKey(items[1].ID)
	setFeedVideoStatus(t, env, items[1].ID, video.VideoStatusRejected)
	both, _ := env.newServer(t, env.livePageCache(t), cards)
	var page feedTimelineResponse
	doJSON(t, client, http.MethodGet, both.URL+path, "", nil, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].ID != items[2].ID {
		t.Fatalf("不可见视频复现于响应 got=%+v want=%d", page.Items, items[2].ID)
	}
	if !recorder.base.keyExists(t, hiddenKey) {
		t.Fatalf("预热键应由可见性检查拦截而不是被删除 key=%s", hiddenKey)
	}
	t.Logf("预热键=%v 不可见视频=%d 响应=%d 保留键=%s", keys, items[1].ID, page.Items[0].ID, hiddenKey)
}
