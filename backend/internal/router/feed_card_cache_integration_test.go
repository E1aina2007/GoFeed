package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	infracachefeed "gofeed/internal/infra/cache/feed"
	"gorm.io/gorm"
)

type feedScriptRecorder struct {
	base          *feedCacheRecorder
	mu            sync.Mutex
	reads, writes int
}

// 测试目标：观测真实 Redis 批量脚本并复用精确键清理
// 预期效果：真实卡片访问有独立读写计数，不污染已有页缓存观测
func (r *feedScriptRecorder) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	full := make([]string, len(keys))
	for i, key := range keys {
		full[i] = r.base.prefix + key
		r.base.record(&r.base.writeKeys, key)
	}
	r.mu.Lock()
	if strings.Contains(script, "redis.call('GET'") {
		r.reads++
	}
	if strings.Contains(script, "redis.call('SET'") {
		r.writes++
	}
	r.mu.Unlock()
	return r.base.runtime.Eval(ctx, script, full, args...)
}

// 测试目标：装配与生产同构的卡片适配器和独立随机键空间
// 预期效果：使用真实 Redis 且退出时检查全部精确键已删除
func (e *feedTestEnv) liveCardCache(t *testing.T) (applicationfeed.CardCache, *feedScriptRecorder) {
	t.Helper()
	base := &feedCacheRecorder{runtime: e.runtime, prefix: feedTestNamespace(t)}
	t.Cleanup(func() { base.cleanup(t) })
	recorder := &feedScriptRecorder{base: base}
	adapter, err := infracachefeed.NewCardCache(recorder, infracachefeed.CardCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return adapter, recorder
}

// 测试目标：验证四种缓存组合、完整响应兼容和实际查询投影
// 预期效果：仅两个开关都开启时读取卡片，首屏绕过，冷读五条 SQL、命中四条且视频仅查询三列
func TestFeedCardCacheRealReadPath(t *testing.T) {
	env := newFeedTestEnv(t)
	cardCache, recorder := env.liveCardCache(t)
	events := &timelineCacheEvents{event: "feed_card_cache", counts: make(map[string]int)}
	original := log.Writer()
	log.SetOutput(io.MultiWriter(original, events))
	t.Cleanup(func() { log.SetOutput(original) })
	plain, client := env.newServer(t, nil)
	register(t, client, plain.URL, "card_reader_author", "card-reader-password-123")
	session := login(t, client, plain.URL, "card_reader_author", "card-reader-password-123")
	items := publishFeedVideos(t, env, plain.URL, session.AccessToken, client, 3)
	var first feedTimelineResponse
	doJSON(t, client, http.MethodGet, plain.URL+"/api/feed?scene=timeline&limit=1", "", nil, http.StatusOK, &first)
	path := "/api/feed?scene=timeline&limit=1&cursor=" + url.QueryEscape(first.NextCursor)
	_, oracle := rawStatusBody(t, client, plain.URL+path)
	cardOnly, _ := env.newServer(t, nil, cardCache)
	_, body := rawStatusBody(t, client, cardOnly.URL+path)
	if !bytes.Equal(body, oracle) || recorder.reads != 0 || recorder.writes != 0 {
		t.Fatal("只开卡片不得访问缓存")
	}
	pageOnly, _ := env.newServer(t, env.livePageCache(t))
	for i := 0; i < 2; i++ {
		_, body = rawStatusBody(t, client, pageOnly.URL+path)
		if !bytes.Equal(body, oracle) {
			t.Fatal("页缓存响应变化")
		}
	}
	if recorder.reads != 0 || recorder.writes != 0 {
		t.Fatal("页缓存不能隐式启用卡片")
	}
	both, _ := env.newServer(t, env.livePageCache(t), cardCache)
	_, body = rawStatusBody(t, client, both.URL+"/api/feed?scene=timeline&limit=1")
	if recorder.reads != 0 || recorder.writes != 0 {
		t.Fatal("首屏访问卡片缓存")
	}
	for _, want := range []int64{5, 4} {
		count := measuredQueryCount(t, env.capture, func() {
			status, response := rawStatusBody(t, client, both.URL+path)
			if status != 200 || !bytes.Equal(response, oracle) {
				t.Fatalf("status=%d response=%s", status, response)
			}
		})
		if count != want {
			t.Fatalf("SQL=%d want=%d", count, want)
		}
	}
	if recorder.reads != 2 || recorder.writes != 1 || events.snapshot()["hit"] != 1 {
		t.Fatalf("reads=%d writes=%d events=%v", recorder.reads, recorder.writes, events.snapshot())
	}
	var sqlMu sync.Mutex
	var projections []string
	callback := "test:feed_card_projection"
	if err := env.gdb.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "videos" {
			return
		}
		sqlMu.Lock()
		defer sqlMu.Unlock()
		projections = append(projections, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.gdb.Callback().Query().Remove(callback) })
	_, body = rawStatusBody(t, client, both.URL+path)
	sqlMu.Lock()
	captured := append([]string(nil), projections...)
	sqlMu.Unlock()
	if len(captured) != 1 || !strings.HasPrefix(captured[0], "SELECT `id`,`author_id`,`published_at` FROM `videos`") {
		t.Fatalf("轻量投影未生效: %v", captured)
	}
	// 命中卡片也不能绕过 MySQL 可见性检查，公开数据读取失败须返回错误
	env.faults.arm("videos", errors.New("injected public guard outage"))
	status, _ := rawStatusBody(t, client, both.URL+path)
	env.faults.disarm()
	if status != http.StatusServiceUnavailable {
		t.Fatalf("公开检查失败 status=%d", status)
	}
	// 仅破坏一条卡片，剩余探测记录应继续命中
	key := fmt.Sprintf("gofeed:feed:card:v1:%d", items[1].ID)
	recorder.base.writeValue(t, key, "broken")
	_, body = rawStatusBody(t, client, both.URL+path)
	if !bytes.Equal(body, oracle) || events.snapshot()["invalid_payload"] != 1 {
		t.Fatalf("坏值回源不兼容 body=%s events=%v", body, events.snapshot())
	}
	// 旧请求在 MySQL 删除之后又回填，真实公开检查必须拦住
	old := recorder.base.readValue(t, key)
	if err := env.gdb.Exec("UPDATE videos SET deleted_at = ? WHERE id = ?", time.Now(), items[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	recorder.base.writeValue(t, key, old)
	var after feedTimelineResponse
	doJSON(t, client, http.MethodGet, both.URL+path, "", nil, http.StatusOK, &after)
	if len(after.Items) != 1 || after.Items[0].ID != items[2].ID {
		t.Fatalf("已删除卡片复活: %+v", after)
	}
	t.Logf("真实卡片缓存事件=%v; SQL 冷读=5 命中=4; 视频命中投影=%s", events.snapshot(), captured[0])
}

// 测试目标：卡片已命中时验证作者更新、注销占位及实时互动统计
// 预期效果：缓存不包含作者资料或统计，响应立即反映 MySQL 变化并保留原游标
func TestFeedCardCacheRealAuthorAndEngagement(t *testing.T) {
	env := newFeedTestEnv(t)
	cards, recorder := env.liveCardCache(t)
	server, client := env.newServer(t, env.livePageCache(t), cards)
	base := server.URL
	register(t, client, base, "card_live_author", "card-live-password-123")
	author := login(t, client, base, "card_live_author", "card-live-password-123")
	items, secondURL, initial := setupCachedFeedPage(t, env, base, author.AccessToken, client)
	var warm feedTimelineResponse
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &warm)
	if recorder.writes != 1 {
		t.Fatalf("未回填卡片 writes=%d", recorder.writes)
	}
	register(t, client, base, "card_live_viewer", "card-viewer-password-123")
	viewer := login(t, client, base, "card_live_viewer", "card-viewer-password-123")
	doJSON(t, client, http.MethodPut, fmt.Sprintf("%s/api/video/auth/%d/like", base, items[1].ID), viewer.AccessToken, nil, http.StatusOK, nil)
	doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/video/auth/%d/comments", base, items[1].ID), viewer.AccessToken, map[string]string{"content": "实时评论"}, http.StatusCreated, nil)
	if err := env.gdb.Exec("UPDATE users SET avatar_url = ? WHERE id = ?", "/static/changed-avatar.png", author.UserID).Error; err != nil {
		t.Fatal(err)
	}
	var updated feedTimelineResponse
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &updated)
	if len(updated.Items) != 1 || updated.Items[0].Author.AvatarURL != "/static/changed-avatar.png" || updated.Items[0].LikesCount != 1 || updated.Items[0].CommentsCount != 1 || updated.NextCursor != initial.NextCursor {
		t.Fatalf("更新未实时生效: %+v", updated)
	}
	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", author.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &updated)
	if len(updated.Items) != 1 || updated.Items[0].Author.Username != "已注销用户" || updated.Items[0].LikesCount != 1 || recorder.writes != 1 {
		t.Fatalf("注销占位不兼容: %+v writes=%d", updated, recorder.writes)
	}
}

// 测试目标：真实页命中时注入独立 Redis 故障
// 预期效果：卡片适配器不可用仍批量回源 MySQL，JSON 完全一致且不影响共享服务
func TestFeedCardCacheRealRedisFailure(t *testing.T) {
	env := newFeedTestEnv(t)
	plain, client := env.newServer(t, env.livePageCache(t))
	register(t, client, plain.URL, "card_failure_author", "card-failure-password-123")
	session := login(t, client, plain.URL, "card_failure_author", "card-failure-password-123")
	_, path, _ := setupCachedFeedPage(t, env, plain.URL, session.AccessToken, client)
	path = strings.TrimPrefix(path, plain.URL)
	_, oracle := rawStatusBody(t, client, plain.URL+path)
	bad, err := infracachefeed.NewCardCache(newUnreachableRedisRuntime(t), infracachefeed.CardCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	both, _ := env.newServer(t, env.livePageCache(t), bad)
	for i := 0; i < 2; i++ {
		status, body := rawStatusBody(t, client, both.URL+path)
		if status != 200 || !bytes.Equal(body, oracle) {
			t.Fatalf("status=%d body=%s", status, body)
		}
	}
}
