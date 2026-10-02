package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/config"
	"gofeed/internal/db"
	infracachefeed "gofeed/internal/infra/cache/feed"
	"gofeed/internal/middleware/cache"
	"gofeed/internal/testutil"
	videoModel "gofeed/internal/video"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// feedBaseTime 固定时间线用例的发布时间基准，避免断言依赖发布耗时
var feedBaseTime = time.Date(2026, 8, 1, 8, 0, 0, 0, time.Local)

// feedTimelineResponse 描述 /api/feed 的公开响应结构
type feedTimelineResponse struct {
	Items      []feedTimelineItem `json:"items"`
	NextCursor string             `json:"next_cursor"`
}

// feedTimelineItem 描述 /api/feed 时间线条目的展示契约
type feedTimelineItem struct {
	ID                uint      `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	PlayURL           string    `json:"play_url"`
	PlayFileName      string    `json:"play_file_name"`
	PlayOriginalName  string    `json:"play_original_name"`
	CoverURL          string    `json:"cover_url"`
	CoverFileName     string    `json:"cover_file_name"`
	CoverOriginalName string    `json:"cover_original_name"`
	PublishedAt       time.Time `json:"published_at"`
	LikesCount        int64     `json:"likes_count"`
	CommentsCount     int64     `json:"comments_count"`
	Author            struct {
		ID        uint   `json:"id"`
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
	} `json:"author"`
}

// feedTimelineCursor 描述新时间线游标的编码字段
type feedTimelineCursor struct {
	Version     int       `json:"version"`
	Scene       string    `json:"scene"`
	SortVersion int       `json:"sort_version"`
	PublishedAt time.Time `json:"published_at"`
	VideoID     uint      `json:"video_id"`
}

// feedCachedPage 描述页缓存载荷的结构，用于断言回填内容
type feedCachedPage struct {
	Version     int                  `json:"version"`
	SortVersion int                  `json:"sort_version"`
	Items       []feedCachedPageItem `json:"items"`
}

// feedCachedPageItem 描述轻量页条目的持久化字段
type feedCachedPageItem struct {
	VideoID     uint      `json:"video_id"`
	AuthorID    *uint     `json:"author_id"`
	PublishedAt time.Time `json:"published_at"`
}

// feedCacheRecorder 在真实 Redis 运行时外叠加随机命名空间并记录页缓存键访问
type feedCacheRecorder struct {
	runtime *cache.Runtime
	prefix  string

	mu        sync.Mutex
	readKeys  []string
	writeKeys []string
}

// 测试目标：读取本机真实 Redis 连接配置
// 预期效果：未配置 Redis 时集成用例整体跳过且不打印凭据
func realRedisConfig(t *testing.T) config.RedisConfig {
	t.Helper()
	host := os.Getenv("REDIS_HOST")
	port := 0
	if raw := os.Getenv("REDIS_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("REDIS_PORT 不是合法端口: %v", err)
		}
		port = parsed
	}
	if host == "" || port == 0 {
		t.Skip("需要真实 Redis：设置 REDIS_HOST 与 REDIS_PORT 后重跑")
	}
	database := 0
	if raw := os.Getenv("REDIS_DB"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("REDIS_DB 不是合法编号: %v", err)
		}
		database = parsed
	}
	return config.RedisConfig{Host: host, Port: port, DB: database, Password: os.Getenv("REDIS_PASSWORD")}
}

// 测试目标：为用例生成与其他数据隔离的随机 Redis 命名空间
// 预期效果：页缓存固定键前缀之前叠加唯一前缀，清理时不需要通配删除
func feedTestNamespace(t *testing.T) string {
	t.Helper()
	var buffer [8]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		t.Fatalf("生成测试命名空间失败: %v", err)
	}
	return fmt.Sprintf("gofeed:test:%x:", buffer[:])
}

// 测试目标：确认真实 Redis 可连接
// 预期效果：已配置但不可连接时用例立即失败，避免缓存断言在故障路径上静默通过
func assertRealRedis(t *testing.T, runtime *cache.Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := runtime.EnsureConnected(ctx); err != nil {
		t.Fatalf("真实 Redis 不可连接，集成用例要求真实依赖参与: %v", err)
	}
	if err := runtime.Ping(ctx); err != nil {
		t.Fatalf("真实 Redis Ping 失败: %v", err)
	}
}

// feedTestEnv 聚合同一临时库上的真实 MySQL 与真实 Redis 页缓存装配
type feedTestEnv struct {
	gdb      *gorm.DB
	runtime  *cache.Runtime
	recorder *feedCacheRecorder
	capture  *queryCapture
	faults   *faultInjection
}

// 测试目标：装配注入查询计数、故障注入与真实 Redis 命名空间的集成环境
// 预期效果：用例可在真实依赖上断言缓存键、查询预算与暂态故障
func newFeedTestEnv(t *testing.T) *feedTestEnv {
	t.Helper()
	gdb := testutil.DB(t)
	if err := db.RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("注册查询计数回调失败: %v", err)
	}
	if err := registerFaultInjection(gdb); err != nil {
		t.Fatalf("注册故障注入回调失败: %v", err)
	}
	runtime := cache.NewRuntime(realRedisConfig(t))
	t.Cleanup(func() { _ = runtime.Close() })
	assertRealRedis(t, runtime)
	recorder := &feedCacheRecorder{runtime: runtime, prefix: feedTestNamespace(t)}
	t.Cleanup(func() { recorder.cleanup(t) })
	return &feedTestEnv{
		gdb:      gdb,
		runtime:  runtime,
		recorder: recorder,
		capture:  &queryCapture{},
		faults:   &faultInjection{},
	}
}

// 测试目标：用真实 Redis 与随机命名空间构造生产同构的页缓存
// 预期效果：缓存读写走真实 Redis，键空间与其他用例隔离
func (e *feedTestEnv) livePageCache(t *testing.T) applicationfeed.PageCache {
	t.Helper()
	pageCache, err := infracachefeed.NewPageCache(e.recorder, infracachefeed.PageCacheOptions{})
	if err != nil {
		t.Fatalf("构造页缓存失败: %v", err)
	}
	return pageCache
}

// 测试目标：装配共享同一测试库的路由服务
// 预期效果：用例可对比启用与关闭页缓存时的可见行为
func (e *feedTestEnv) newServer(t *testing.T, pageCache applicationfeed.PageCache) (*httptest.Server, *http.Client) {
	t.Helper()
	engine := New(e.gdb, false, Options{
		UploadDir:     t.TempDir(),
		FeedPageCache: pageCache,
		Middlewares:   []gin.HandlerFunc{e.capture.middleware(), e.faults.middleware()},
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client()
}

// 测试目标：记录一次页缓存键访问
// 预期效果：断言读取稳定快照，不受后续请求影响
func (r *feedCacheRecorder) record(target *[]string, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*target = append(*target, key)
}

// 测试目标：读取页缓存并转发到真实 Redis
// 预期效果：命中与未命中语义与生产装配一致
func (r *feedCacheRecorder) Get(ctx context.Context, key string) (string, error) {
	r.record(&r.readKeys, key)
	return r.runtime.Get(ctx, r.prefix+key)
}

// 测试目标：写入页缓存并转发到真实 Redis
// 预期效果：写入的键带随机命名空间且被记录用于清理
func (r *feedCacheRecorder) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	r.record(&r.writeKeys, key)
	return r.runtime.Set(ctx, r.prefix+key, value, expiration)
}

// 测试目标：返回已记录的页缓存读取键副本
// 预期效果：断言不受并发访问影响
func (r *feedCacheRecorder) reads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.readKeys...)
}

// 测试目标：返回已记录的页缓存写入键副本
// 预期效果：断言可定位回填的键名
func (r *feedCacheRecorder) writes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.writeKeys...)
}

// 测试目标：检查页缓存键在真实 Redis 中是否存在
// 预期效果：返回结果不依赖客户端错误语义
func (r *feedCacheRecorder) keyExists(t *testing.T, key string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	result, err := r.runtime.Eval(ctx, "return redis.call('EXISTS', KEYS[1])", []string{r.prefix + key})
	if err != nil {
		t.Fatalf("检查页缓存键存在性失败: %v", err)
	}
	count, ok := result.(int64)
	if !ok {
		t.Fatalf("EXISTS 返回类型异常 got=%T", result)
	}
	return count == 1
}

// 测试目标：读取页缓存键的原始载荷
// 预期效果：供断言回填内容与坏值覆盖面
func (r *feedCacheRecorder) readValue(t *testing.T, key string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	value, err := r.runtime.Get(ctx, r.prefix+key)
	if err != nil {
		t.Fatalf("读取页缓存键失败: %v", err)
	}
	return value
}

// 测试目标：直接改写页缓存键的原始载荷
// 预期效果：用例可以构造非法载荷而不经过页缓存编码
func (r *feedCacheRecorder) writeValue(t *testing.T, key, value string) {
	t.Helper()
	r.record(&r.writeKeys, key)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := r.runtime.Set(ctx, r.prefix+key, value, time.Minute); err != nil {
		t.Fatalf("改写页缓存键失败: %v", err)
	}
}

// 测试目标：检查本用例记录的缓存键是否仍存在
// 预期效果：只访问精确键，不遍历共享 Redis 的键空间
func (r *feedCacheRecorder) recordedKeyCount(t *testing.T) int64 {
	t.Helper()
	// 清理阶段 t.Context 已被取消，必须使用独立上下文
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int64
	for _, key := range r.recordedKeys() {
		result, err := r.runtime.Eval(ctx, "return redis.call('EXISTS', KEYS[1])", []string{key})
		if err != nil {
			t.Fatalf("检查已记录的页缓存键失败: %v", err)
		}
		value, ok := result.(int64)
		if !ok {
			t.Fatalf("EXISTS 返回类型异常 got=%T", result)
		}
		count += value
	}
	return count
}

func (r *feedCacheRecorder) recordedKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]struct{}{}
	keys := make([]string, 0, len(r.readKeys)+len(r.writeKeys))
	for _, key := range append(append([]string(nil), r.readKeys...), r.writeKeys...) {
		full := r.prefix + key
		if _, ok := seen[full]; ok {
			continue
		}
		seen[full] = struct{}{}
		keys = append(keys, full)
	}
	return keys
}

// 测试目标：精确删除本用例访问过的页缓存键
// 预期效果：已记录的测试键不残留且不遍历共享实例
func (r *feedCacheRecorder) cleanup(t *testing.T) {
	t.Helper()
	keys := r.recordedKeys()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if len(keys) > 0 {
		if _, err := r.runtime.Del(ctx, keys...); err != nil {
			t.Fatalf("清理页缓存测试键失败: %v", err)
		}
	}
	if remaining := r.recordedKeyCount(t); remaining != 0 {
		t.Fatalf("清理后 Redis 仍残留 %d 个测试键", remaining)
	}
}

// 测试目标：验证缓存检查与清理只访问已记录的精确键
// 预期效果：重复访问不重复计数，直接注入的键被清理，其他键不受影响
func TestFeedCacheRecorderChecksOnlyRecordedKeys(t *testing.T) {
	runtime := cache.NewRuntime(realRedisConfig(t))
	t.Cleanup(func() { _ = runtime.Close() })
	assertRealRedis(t, runtime)
	recorder := &feedCacheRecorder{runtime: runtime, prefix: feedTestNamespace(t)}
	otherKey := recorder.prefix + "unrecorded"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := runtime.Del(ctx, otherKey); err != nil {
			t.Errorf("清理对照键失败: %v", err)
		}
	})
	t.Cleanup(func() { recorder.cleanup(t) })
	if err := runtime.Set(t.Context(), otherKey, "keep", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Set(t.Context(), "page", "value", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Get(t.Context(), "page"); err != nil {
		t.Fatal(err)
	}
	recorder.writeValue(t, "injected", "invalid payload")
	if count := recorder.recordedKeyCount(t); count != 2 {
		t.Fatalf("应只统计两个已记录的键 got=%d", count)
	}
	recorder.cleanup(t)
	if value, err := runtime.Get(t.Context(), otherKey); err != nil || value != "keep" {
		t.Fatalf("清理不应影响未记录的对照键 value=%q err=%v", value, err)
	}
}

// 测试目标：构造指向无监听端口的 Redis 运行时
// 预期效果：缓存读写必然失败且不触碰共享实例
func newUnreachableRedisRuntime(t *testing.T) *cache.Runtime {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配未监听端口失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("关闭占位监听失败: %v", err)
	}
	runtime := cache.NewRuntime(
		config.RedisConfig{Host: "127.0.0.1", Port: port},
		cache.WithReconnectCooldown(50*time.Millisecond),
	)
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

// 测试目标：读取响应状态码与原始响应体
// 预期效果：可在不预设状态码的前提下比较不同装配的响应
func rawStatusBody(t *testing.T, client *http.Client, rawURL string) (int, []byte) {
	t.Helper()
	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return resp.StatusCode, body
}

// 测试目标：测量单个请求在真实 MySQL 上执行的语句数量
// 预期效果：返回该请求的语句计数且不受此前请求影响
func measuredQueryCount(t *testing.T, capture *queryCapture, request func()) int64 {
	t.Helper()
	capture.reset()
	request()
	counts := capture.snapshot()
	if len(counts) != 1 {
		t.Fatalf("应只记录一次请求 got=%d", len(counts))
	}
	return counts[0]
}

// 测试目标：解码新时间线游标用于断言分页位置
// 预期效果：暴露版本、场景、排序版本与位置字段
func decodeFeedCursor(t *testing.T, cursor string) feedTimelineCursor {
	t.Helper()
	if cursor == "" {
		t.Fatal("游标不应为空")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("游标不是合法 base64: %v", err)
	}
	var decoded feedTimelineCursor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("游标不是合法 JSON: %v", err)
	}
	return decoded
}

// 测试目标：手工构造与新时间线游标同构的编码值
// 预期效果：用例可以请求任意读取位置而不依赖上一页响应
func encodeFeedTestCursor(t *testing.T, publishedAt time.Time, videoID uint) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"version":      1,
		"scene":        "timeline",
		"sort_version": 1,
		"published_at": publishedAt,
		"video_id":     videoID,
	})
	if err != nil {
		t.Fatalf("构造游标失败: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

// 测试目标：解码页缓存载荷用于断言回填内容
// 预期效果：暴露版本与轻量页条目，条目缺少作者标识时解码结果为 nil
func decodeCachedFeedPage(t *testing.T, payload string) feedCachedPage {
	t.Helper()
	var decoded feedCachedPage
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("页缓存载荷不是合法 JSON: %v", err)
	}
	return decoded
}

// 测试目标：发布指定数量的完整公开视频并按固定间隔对齐发布时间
// 预期效果：返回按发布时间从新到旧排列的条目，顺序不依赖发布耗时
func publishFeedVideos(t *testing.T, env *feedTestEnv, base, token string, client *http.Client, count int) []draftItem {
	t.Helper()
	items := make([]draftItem, 0, count)
	for index := 0; index < count; index++ {
		item := publishCompleteVideo(t, env.gdb, client, base, token, fmt.Sprintf("时间线视频 %d", index+1))
		publishedAt := feedBaseTime.Add(-time.Duration(index) * 10 * time.Second)
		result := env.gdb.Exec("UPDATE videos SET published_at = ? WHERE id = ?", publishedAt, item.ID)
		if result.Error != nil || result.RowsAffected != 1 {
			t.Fatalf("对齐发布时间失败 rows=%d err=%v", result.RowsAffected, result.Error)
		}
		items = append(items, item)
	}
	return items
}

// 测试目标：按固定发布时间改写单条视频的公开位置
// 预期效果：写入成功且影响行数为一行
func setFeedVideoPublishedAt(t *testing.T, env *feedTestEnv, videoID uint, publishedAt time.Time) {
	t.Helper()
	result := env.gdb.Exec("UPDATE videos SET published_at = ? WHERE id = ?", publishedAt, videoID)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("改写发布时间失败 rows=%d err=%v", result.RowsAffected, result.Error)
	}
}

// 测试目标：按状态改写单条视频的公开可见性
// 预期效果：写入成功且影响行数为一行
func setFeedVideoStatus(t *testing.T, env *feedTestEnv, videoID uint, status string) {
	t.Helper()
	result := env.gdb.Exec("UPDATE videos SET status = ? WHERE id = ?", status, videoID)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("改写视频状态失败 rows=%d err=%v", result.RowsAffected, result.Error)
	}
}

// 测试目标：构造已回填页缓存的第二页读取位置
// 预期效果：返回视频标识、第二页请求地址与该页响应供失效场景复用
func setupCachedFeedPage(t *testing.T, env *feedTestEnv, base, token string, client *http.Client) ([]draftItem, string, feedTimelineResponse) {
	t.Helper()
	items := publishFeedVideos(t, env, base, token, client, 3)
	var firstPage feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=1", "", nil, http.StatusOK, &firstPage)
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != items[0].ID || firstPage.NextCursor == "" {
		t.Fatalf("首屏应返回最新视频与游标 got=%+v", firstPage)
	}
	secondURL := base + "/api/feed?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)
	var secondPage feedTimelineResponse
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &secondPage)
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID != items[1].ID || secondPage.NextCursor == "" {
		t.Fatalf("第二页应未命中并返回中间视频 got=%+v", secondPage)
	}
	if writes := env.recorder.writes(); len(writes) != 1 {
		t.Fatalf("第二页未命中后应回填一次页缓存 got=%d", len(writes))
	}
	return items, secondURL, secondPage
}

// 测试目标：验证未注入页缓存时时间线的公开契约与排序
// 预期效果：默认时间线返回 200 与完整条目，按发布时间倒序且同刻按标识倒序
func TestFeedTimelineContractWithoutPageCache(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, nil)
	base := server.URL
	const username = "feed_contract_author"
	register(t, client, base, username, "feed-contract-password-123")
	session := login(t, client, base, username, "feed-contract-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, client, 3)

	// 让前两条共享同一发布时间，顺序只能由标识倒序决定
	sameTime := feedBaseTime.Add(time.Minute)
	for _, item := range items[:2] {
		setFeedVideoPublishedAt(t, env, item.ID, sameTime)
	}

	var page feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed", "", nil, http.StatusOK, &page)
	if len(page.Items) != 3 {
		t.Fatalf("默认时间线应返回全部公开条目 got=%d want=3", len(page.Items))
	}
	wantOrder := []uint{items[1].ID, items[0].ID, items[2].ID}
	for index, want := range wantOrder {
		if page.Items[index].ID != want {
			t.Fatalf("时间线顺序应为发布时间倒序加标识倒序 got=%+v want=%v", page.Items, wantOrder)
		}
	}
	if page.NextCursor != "" {
		t.Fatalf("没有更多数据时不应返回游标 got=%s", page.NextCursor)
	}

	first := page.Items[0]
	switch {
	case first.Title != "时间线视频 2":
		t.Fatalf("条目标题错误 got=%q", first.Title)
	case first.Description != "":
		t.Fatalf("条目描述错误 got=%q", first.Description)
	case !strings.HasPrefix(first.PlayURL, "/static/"):
		t.Fatalf("播放地址应为静态资源 got=%q", first.PlayURL)
	case !strings.HasPrefix(first.CoverURL, "/static/"):
		t.Fatalf("封面地址应为静态资源 got=%q", first.CoverURL)
	case first.PlayFileName == "" || first.CoverFileName == "":
		t.Fatalf("媒体文件名不应为空 got=%+v", first)
	case first.PlayOriginalName != "feed.mp4" || first.CoverOriginalName != "feed.png":
		t.Fatalf("媒体原始名应与上传一致 got=%+v", first)
	case first.Author.ID != session.UserID || first.Author.Username != username:
		t.Fatalf("作者展示字段错误 got=%+v", first.Author)
	case first.Author.AvatarURL != "":
		t.Fatalf("未设置头像时不应返回地址 got=%q", first.Author.AvatarURL)
	case first.LikesCount != 0 || first.CommentsCount != 0:
		t.Fatalf("互动计数应初始为零 got=%d/%d", first.LikesCount, first.CommentsCount)
	case !first.PublishedAt.Equal(sameTime):
		t.Fatalf("发布时间错误 got=%s want=%s", first.PublishedAt, sameTime)
	}

	body := getRawBody(t, client, base+"/api/feed")
	if !bytes.Contains(body, []byte(`"items":[`)) {
		t.Fatalf("响应应包含条目数组 got=%s", body)
	}
	sceneBody := getRawBody(t, client, base+"/api/feed?scene=timeline")
	if !bytes.Equal(body, sceneBody) {
		t.Fatalf("显式时间线场景应与默认响应一致 default=%s scene=%s", body, sceneBody)
	}

	var limited feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=2", "", nil, http.StatusOK, &limited)
	if len(limited.Items) != 2 || limited.NextCursor == "" {
		t.Fatalf("限量请求应返回两条并给出游标 got=%+v", limited)
	}
}

// 测试目标：验证时间线对非法查询参数与未启用场景的拒绝契约
// 预期效果：非法参数返回 400，未启用场景返回 501 并禁止缓存且都不触碰页缓存
func TestFeedRejectsInvalidQueryParameters(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_query_author", "feed-query-password-123")
	session := login(t, client, base, "feed_query_author", "feed-query-password-123")
	publishFeedVideos(t, env, base, session.AccessToken, client, 2)

	for _, invalid := range []struct {
		name    string
		path    string
		status  int
		message string
	}{
		{"未知参数", "/api/feed?author_id=1", http.StatusBadRequest, "invalid feed query"},
		{"重复参数", "/api/feed?limit=1&limit=2", http.StatusBadRequest, "invalid feed query"},
		{"页大小越界", "/api/feed?limit=51", http.StatusBadRequest, "invalid limit"},
		{"页大小为零", "/api/feed?limit=0", http.StatusBadRequest, "invalid limit"},
		{"未知场景", "/api/feed?scene=unknown", http.StatusBadRequest, "invalid feed scene"},
		{"未启用场景", "/api/feed?scene=hot", http.StatusNotImplemented, "feed scene is not enabled"},
	} {
		status, body := rawStatusBody(t, client, base+invalid.path)
		if status != invalid.status {
			t.Fatalf("%s 状态码错误 got=%d want=%d body=%s", invalid.name, status, invalid.status, body)
		}
		if !bytes.Contains(body, []byte(invalid.message)) {
			t.Fatalf("%s 应返回固定文案 got=%s want=%s", invalid.name, body, invalid.message)
		}
		if bytes.Contains(body, []byte(`"items"`)) {
			t.Fatalf("%s 不应返回条目 got=%s", invalid.name, body)
		}
	}

	disabled, err := client.Get(base + "/api/feed?scene=hot")
	if err != nil {
		t.Fatalf("请求未启用场景失败: %v", err)
	}
	defer disabled.Body.Close()
	if got := disabled.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("未启用场景应返回 Cache-Control: no-store got=%q", got)
	}

	var boundary feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=50", "", nil, http.StatusOK, &boundary)
	if len(boundary.Items) != 2 {
		t.Fatalf("页大小边界内应正常返回 got=%d", len(boundary.Items))
	}
	if reads, writes := env.recorder.reads(), env.recorder.writes(); len(reads) != 0 || len(writes) != 0 {
		t.Fatalf("非法参数不应触碰页缓存 reads=%v writes=%v", reads, writes)
	}
}

// 测试目标：验证关闭页缓存时时间线不触碰缓存且查询预算与开启时一致
// 预期效果：Redis 命名空间内不出现键，同一请求形状的语句数与启用缓存时相同
func TestFeedWithoutPageCacheSkipsCacheAndKeepsQueryBudget(t *testing.T) {
	env := newFeedTestEnv(t)
	uncached, uncachedClient := env.newServer(t, nil)
	cached, cachedClient := env.newServer(t, env.livePageCache(t))
	base := uncached.URL
	register(t, uncachedClient, base, "feed_budget_author", "feed-budget-password-123")
	session := login(t, uncachedClient, base, "feed_budget_author", "feed-budget-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, uncachedClient, 3)

	var firstPage feedTimelineResponse
	doJSON(t, uncachedClient, http.MethodGet, base+"/api/feed?limit=1", "", nil, http.StatusOK, &firstPage)
	if firstPage.NextCursor == "" || firstPage.Items[0].ID != items[0].ID {
		t.Fatalf("首屏应返回最新视频与游标 got=%+v", firstPage)
	}
	cursorPath := "/api/feed?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)

	uncachedFirst := measuredQueryCount(t, env.capture, func() {
		doJSON(t, uncachedClient, http.MethodGet, base+"/api/feed?limit=1", "", nil, http.StatusOK, &feedTimelineResponse{})
	})
	uncachedSecond := measuredQueryCount(t, env.capture, func() {
		doJSON(t, uncachedClient, http.MethodGet, base+cursorPath, "", nil, http.StatusOK, &feedTimelineResponse{})
	})
	if uncachedSecond != uncachedFirst {
		t.Fatalf("关闭缓存时首屏与翻页预算应一致 got=%d/%d", uncachedFirst, uncachedSecond)
	}
	if uncachedSecond != 4 {
		t.Fatalf("关闭缓存时翻页预算应为视频、作者、点赞、评论各一次 got=%d want=4", uncachedSecond)
	}
	if reads, writes := env.recorder.reads(), env.recorder.writes(); len(reads) != 0 || len(writes) != 0 {
		t.Fatalf("关闭缓存时不应产生缓存访问 reads=%v writes=%v", reads, writes)
	}
	if count := env.recorder.recordedKeyCount(t); count != 0 {
		t.Fatalf("关闭缓存时不应在 Redis 中留下键 got=%d", count)
	}

	cachedFirst := measuredQueryCount(t, env.capture, func() {
		doJSON(t, cachedClient, http.MethodGet, cached.URL+"/api/feed?limit=1", "", nil, http.StatusOK, &feedTimelineResponse{})
	})
	cachedSecond := measuredQueryCount(t, env.capture, func() {
		doJSON(t, cachedClient, http.MethodGet, cached.URL+cursorPath, "", nil, http.StatusOK, &feedTimelineResponse{})
	})
	if cachedFirst != uncachedFirst || cachedSecond != uncachedSecond {
		t.Fatalf(
			"启用缓存不应改变查询预算 cached=%d/%d uncached=%d/%d",
			cachedFirst, cachedSecond, uncachedFirst, uncachedSecond,
		)
	}
}

// 测试目标：验证首屏时间线绕过页缓存且不回填
// 预期效果：请求成功但 Redis 命名空间内不出现任何页缓存键
func TestFeedFirstPageBypassesPageCache(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_first_page_author", "feed-first-page-password-123")
	session := login(t, client, base, "feed_first_page_author", "feed-first-page-password-123")
	publishFeedVideos(t, env, base, session.AccessToken, client, 2)

	var limited feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=1", "", nil, http.StatusOK, &limited)
	if limited.NextCursor == "" {
		t.Fatal("首屏存在下一页时应返回游标")
	}
	var page feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed", "", nil, http.StatusOK, &page)
	if len(page.Items) != 2 {
		t.Fatalf("首屏应返回全部公开条目 got=%d", len(page.Items))
	}
	if reads := env.recorder.reads(); len(reads) != 0 {
		t.Fatalf("首屏不应读取页缓存 got=%v", reads)
	}
	if writes := env.recorder.writes(); len(writes) != 0 {
		t.Fatalf("首屏不应回填页缓存 got=%v", writes)
	}
	if count := env.recorder.recordedKeyCount(t); count != 0 {
		t.Fatalf("首屏不应在 Redis 中留下键 got=%d", count)
	}
}

// 测试目标：验证第二页未命中时用原游标回源并回填完整探测页
// 预期效果：Redis 出现按该游标命名的键且解码后的条目数为 limit+1
func TestFeedSecondPageMissRefillsProbePage(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_refill_author", "feed-refill-password-123")
	session := login(t, client, base, "feed_refill_author", "feed-refill-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, client, 5)

	var firstPage feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=2", "", nil, http.StatusOK, &firstPage)
	if len(firstPage.Items) != 2 || firstPage.Items[0].ID != items[0].ID || firstPage.NextCursor == "" {
		t.Fatalf("首屏应返回最新两条与游标 got=%+v", firstPage)
	}
	secondURL := base + "/api/feed?limit=2&cursor=" + url.QueryEscape(firstPage.NextCursor)
	var secondPage feedTimelineResponse
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &secondPage)
	if len(secondPage.Items) != 2 || secondPage.Items[0].ID != items[2].ID {
		t.Fatalf("第二页应返回接下来的两条 got=%+v", secondPage.Items)
	}

	reads := env.recorder.reads()
	if len(reads) != 1 {
		t.Fatalf("第二页应先读取一次页缓存 got=%v", reads)
	}
	writes := env.recorder.writes()
	if len(writes) != 1 {
		t.Fatalf("第二页未命中后应写入一次页缓存 got=%v", writes)
	}
	key := writes[0]
	if !strings.HasPrefix(key, "gofeed:feed:page:v1:timeline:s1:l2:") {
		t.Fatalf("页缓存键应包含场景、排序版本与页大小 got=%s", key)
	}
	position := decodeFeedCursor(t, firstPage.NextCursor)
	wantSuffix := position.PublishedAt.UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatUint(uint64(items[1].ID), 10)
	if !strings.HasSuffix(key, wantSuffix) {
		t.Fatalf("页缓存键应定位到该游标位置 got=%s wantSuffix=%s", key, wantSuffix)
	}
	if !env.recorder.keyExists(t, key) {
		t.Fatal("回填后页缓存键应存在于 Redis")
	}

	payload := decodeCachedFeedPage(t, env.recorder.readValue(t, key))
	if payload.Version != 1 || payload.SortVersion != 1 {
		t.Fatalf("载荷版本字段错误 got=%+v", payload)
	}
	if len(payload.Items) != 3 {
		t.Fatalf("回填应为 limit+1 条轻量页 got=%d want=3", len(payload.Items))
	}
	for index, entry := range payload.Items {
		want := items[index+2]
		if entry.VideoID != want.ID {
			t.Fatalf("轻量页条目顺序错误 got=%+v", payload.Items)
		}
		if entry.AuthorID == nil || *entry.AuthorID != session.UserID {
			t.Fatalf("轻量页应包含作者标识 got=%+v", entry)
		}
	}
	if !payload.Items[2].PublishedAt.Equal(feedBaseTime.Add(-40 * time.Second)) {
		t.Fatalf("轻量页发布时间错误 got=%s", payload.Items[2].PublishedAt)
	}
}

// 测试目标：验证页缓存命中时仍读取当前公开卡片与真实数据库
// 预期效果：命中响应的条目与关闭缓存时逐字节一致且游标指向同一位置，命中请求仍在 MySQL 上执行语句
func TestFeedPageCacheHitUsesFreshCardsAndQueriesMySQL(t *testing.T) {
	env := newFeedTestEnv(t)
	oracle, oracleClient := env.newServer(t, nil)
	cached, cachedClient := env.newServer(t, env.livePageCache(t))
	base := oracle.URL
	register(t, oracleClient, base, "feed_hit_author", "feed-hit-password-123")
	session := login(t, oracleClient, base, "feed_hit_author", "feed-hit-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, oracleClient, 3)

	var firstPage feedTimelineResponse
	doJSON(t, oracleClient, http.MethodGet, base+"/api/feed?limit=1", "", nil, http.StatusOK, &firstPage)
	if firstPage.NextCursor == "" || firstPage.Items[0].ID != items[0].ID {
		t.Fatalf("首屏应返回最新视频与游标 got=%+v", firstPage)
	}
	cursorPath := "/api/feed?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)

	oracleStatus, oracleBody := rawStatusBody(t, oracleClient, base+cursorPath)
	if oracleStatus != http.StatusOK {
		t.Fatalf("关闭缓存时翻页应成功 got=%d body=%s", oracleStatus, oracleBody)
	}
	oracleBudget := measuredQueryCount(t, env.capture, func() {
		doJSON(t, oracleClient, http.MethodGet, base+cursorPath, "", nil, http.StatusOK, &feedTimelineResponse{})
	})

	missBudget := measuredQueryCount(t, env.capture, func() {
		doJSON(t, cachedClient, http.MethodGet, cached.URL+cursorPath, "", nil, http.StatusOK, &feedTimelineResponse{})
	})
	var hitStatus int
	var hitBody []byte
	hitBudget := measuredQueryCount(t, env.capture, func() {
		hitStatus, hitBody = rawStatusBody(t, cachedClient, cached.URL+cursorPath)
	})
	if hitStatus != http.StatusOK {
		t.Fatalf("命中页缓存时翻页应成功 got=%d body=%s", hitStatus, hitBody)
	}
	reads, writes := env.recorder.reads(), env.recorder.writes()
	if len(reads) != 2 || len(writes) != 1 {
		t.Fatalf("两次请求应各读一次且只回填一次 reads=%v writes=%v", reads, writes)
	}
	var hitPage, oraclePage struct {
		Items      json.RawMessage `json:"items"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(hitBody, &hitPage); err != nil {
		t.Fatalf("命中响应不是合法 JSON: %v", err)
	}
	if err := json.Unmarshal(oracleBody, &oraclePage); err != nil {
		t.Fatalf("未命中响应不是合法 JSON: %v", err)
	}
	if !bytes.Equal(hitPage.Items, oraclePage.Items) {
		t.Fatalf("命中响应条目应与关闭缓存时一致 hit=%s oracle=%s", hitPage.Items, oraclePage.Items)
	}
	hitCursor, oracleCursor := decodeFeedCursor(t, hitPage.NextCursor), decodeFeedCursor(t, oraclePage.NextCursor)
	if hitCursor.Version != oracleCursor.Version || hitCursor.Scene != oracleCursor.Scene ||
		hitCursor.SortVersion != oracleCursor.SortVersion || hitCursor.VideoID != oracleCursor.VideoID ||
		!hitCursor.PublishedAt.Equal(oracleCursor.PublishedAt) {
		t.Fatalf("命中与未命中的游标应指向同一位置 hit=%+v oracle=%+v", hitCursor, oracleCursor)
	}
	if !bytes.Equal(hitBody, oracleBody) {
		t.Fatalf("命中与未命中的响应应逐字节一致 hit=%s oracle=%s", hitBody, oracleBody)
	}
	// 两个游标必须可以互换继续翻页，位置一致不能只体现在解码结果上
	nextHitStatus, nextHitBody := rawStatusBody(t, cachedClient, cached.URL+"/api/feed?limit=1&cursor="+url.QueryEscape(hitPage.NextCursor))
	nextOracleStatus, nextOracleBody := rawStatusBody(t, cachedClient, cached.URL+"/api/feed?limit=1&cursor="+url.QueryEscape(oraclePage.NextCursor))
	if nextHitStatus != http.StatusOK || nextOracleStatus != http.StatusOK || !bytes.Equal(nextHitBody, nextOracleBody) {
		t.Fatalf("命中与未命中的游标应可互换翻页 got=%d/%d body=%s/%s", nextHitStatus, nextOracleStatus, nextHitBody, nextOracleBody)
	}
	if hitBudget < 1 {
		t.Fatalf("命中路径仍必须查询 MySQL 组装当前卡片 got=%d", hitBudget)
	}
	if hitBudget != missBudget || missBudget != oracleBudget {
		t.Fatalf("命中与未命中的查询预算应与关闭缓存一致 hit=%d miss=%d oracle=%d", hitBudget, missBudget, oracleBudget)
	}
}

// 测试目标：验证命中页缓存后条目被删除时整页回源且不沿用旧分页
// 预期效果：被删条目消失且不再返回基于旧条目的下一页游标
func TestFeedPageCacheRefillsAfterVideoDeleted(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_delete_author", "feed-delete-password-123")
	session := login(t, client, base, "feed_delete_author", "feed-delete-password-123")
	items, secondURL, secondPage := setupCachedFeedPage(t, env, base, session.AccessToken, client)
	if decodeFeedCursor(t, secondPage.NextCursor).VideoID != items[1].ID {
		t.Fatalf("旧游标应定位到中间条目 got=%s", secondPage.NextCursor)
	}

	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, items[1].ID), session.AccessToken, nil, http.StatusNoContent, nil)

	var after feedTimelineResponse
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &after)
	if len(after.Items) != 1 || after.Items[0].ID != items[2].ID {
		t.Fatalf("整页回源后应只返回未删除的最旧条目 got=%+v", after.Items)
	}
	if after.NextCursor != "" {
		t.Fatalf("旧分页游标不应在整页回源后被沿用 got=%s", after.NextCursor)
	}
}

// 测试目标：验证命中页缓存后条目改为非公开状态时整页回源
// 预期效果：非公开条目消失且不再返回基于旧条目的下一页游标
func TestFeedPageCacheRefillsAfterVideoUnpublished(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_unpublish_author", "feed-unpublish-password-123")
	session := login(t, client, base, "feed_unpublish_author", "feed-unpublish-password-123")
	items, secondURL, _ := setupCachedFeedPage(t, env, base, session.AccessToken, client)

	setFeedVideoStatus(t, env, items[1].ID, videoModel.VideoStatusProcessing)

	var after feedTimelineResponse
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &after)
	if len(after.Items) != 1 || after.Items[0].ID != items[2].ID {
		t.Fatalf("整页回源后应只返回仍公开的最旧条目 got=%+v", after.Items)
	}
	if after.NextCursor != "" {
		t.Fatalf("旧分页游标不应在整页回源后被沿用 got=%s", after.NextCursor)
	}
}

// 测试目标：验证命中页缓存后条目作者变更时整页回源
// 预期效果：条目作者展示字段来自当前数据而不是缓存中的旧作者
func TestFeedPageCacheRefillsAfterAuthorChanged(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_author_owner", "feed-author-owner-password-123")
	owner := login(t, client, base, "feed_author_owner", "feed-author-owner-password-123")
	items, secondURL, secondPage := setupCachedFeedPage(t, env, base, owner.AccessToken, client)

	register(t, client, base, "feed_author_new", "feed-author-new-password-123")
	adopter := login(t, client, base, "feed_author_new", "feed-author-new-password-123")
	result := env.gdb.Exec("UPDATE videos SET author_id = ? WHERE id = ?", adopter.UserID, items[1].ID)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("改写作者失败 rows=%d err=%v", result.RowsAffected, result.Error)
	}

	var after feedTimelineResponse
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &after)
	if len(after.Items) != 1 || after.Items[0].ID != items[1].ID {
		t.Fatalf("整页回源后应仍返回该条目 got=%+v", after.Items)
	}
	if after.Items[0].Author.ID != adopter.UserID || after.Items[0].Author.Username != "feed_author_new" {
		t.Fatalf("作者展示字段应来自当前数据 got=%+v", after.Items[0].Author)
	}
	if decodeFeedCursor(t, after.NextCursor).VideoID != items[1].ID {
		t.Fatalf("回源后游标应仍定位到该条目 got=%s", after.NextCursor)
	}
	if after.NextCursor != secondPage.NextCursor {
		t.Fatalf("位置未变化时游标应保持稳定 got=%s want=%s", after.NextCursor, secondPage.NextCursor)
	}
}

// 测试目标：验证命中页缓存后条目发布时间变更时整页回源并重建游标
// 预期效果：条目发布时间与下一页游标都反映当前数据而不是缓存中的旧位置
func TestFeedPageCacheRefillsAfterPublishedAtChanged(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_time_author", "feed-time-password-123")
	session := login(t, client, base, "feed_time_author", "feed-time-password-123")
	items, secondURL, secondPage := setupCachedFeedPage(t, env, base, session.AccessToken, client)

	// 仍然位于首屏游标之后，因此回源后仍应出现在第二页
	movedTime := feedBaseTime.Add(-5 * time.Second)
	setFeedVideoPublishedAt(t, env, items[1].ID, movedTime)

	var after feedTimelineResponse
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &after)
	if len(after.Items) != 1 || after.Items[0].ID != items[1].ID {
		t.Fatalf("整页回源后应仍返回该条目 got=%+v", after.Items)
	}
	if !after.Items[0].PublishedAt.Equal(movedTime) {
		t.Fatalf("条目发布时间应来自当前数据 got=%s want=%s", after.Items[0].PublishedAt, movedTime)
	}
	if after.NextCursor == secondPage.NextCursor {
		t.Fatalf("发布时间变化后不应沿用旧分页游标 got=%s", after.NextCursor)
	}
	decoded := decodeFeedCursor(t, after.NextCursor)
	if decoded.VideoID != items[1].ID || !decoded.PublishedAt.Equal(movedTime) {
		t.Fatalf("下一页游标应基于当前发布时间重建 got=%+v want=%s", decoded, movedTime)
	}
}

// 测试目标：验证页缓存中的非法载荷被回源结果覆盖且不影响响应
// 预期效果：非法 JSON、缺作者标识与尾随 JSON 三种坏值都返回正确条目并被正确载荷替换
func TestFeedPageCacheRecoversFromCorruptPayload(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_corrupt_author", "feed-corrupt-password-123")
	session := login(t, client, base, "feed_corrupt_author", "feed-corrupt-password-123")
	items, secondURL, secondPage := setupCachedFeedPage(t, env, base, session.AccessToken, client)

	key := env.recorder.writes()[0]
	good := env.recorder.readValue(t, key)
	goodPage := decodeCachedFeedPage(t, good)
	if len(goodPage.Items) != 2 {
		t.Fatalf("回填应为 limit+1 条轻量页 got=%d want=2", len(goodPage.Items))
	}
	first := goodPage.Items[0]
	missingAuthor := fmt.Sprintf(
		`{"version":1,"sort_version":1,"items":[{"video_id":%d,"published_at":%q}]}`,
		first.VideoID, first.PublishedAt.UTC().Format(time.RFC3339Nano),
	)

	for _, corrupt := range []struct {
		name    string
		payload string
	}{
		{"非法 JSON", "{not-json"},
		{"缺作者标识", missingAuthor},
		{"尾随 JSON", good + "{}"},
	} {
		env.recorder.writeValue(t, key, corrupt.payload)
		var page feedTimelineResponse
		doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &page)
		if len(page.Items) != 1 || page.Items[0].ID != items[1].ID {
			t.Fatalf("%s 应回源返回正确条目 got=%+v", corrupt.name, page.Items)
		}
		if page.NextCursor != secondPage.NextCursor {
			t.Fatalf("%s 回源后分页位置应保持不变 got=%s", corrupt.name, page.NextCursor)
		}
		if restored := env.recorder.readValue(t, key); restored != good {
			t.Fatalf("%s 坏值应被正确载荷覆盖 got=%s", corrupt.name, restored)
		}
	}
}

// 测试目标：验证没有更多数据时返回空条目数组且不返回游标
// 预期效果：越过末尾的游标返回空数组，恰好取完的一页也不返回游标
func TestFeedEmptyPageAndExhaustedPage(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_empty_author", "feed-empty-password-123")
	session := login(t, client, base, "feed_empty_author", "feed-empty-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, client, 3)

	var exhausted feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=3", "", nil, http.StatusOK, &exhausted)
	if len(exhausted.Items) != 3 || exhausted.Items[2].ID != items[2].ID {
		t.Fatalf("恰好取完的一页应返回全部条目 got=%+v", exhausted.Items)
	}
	if exhausted.NextCursor != "" {
		t.Fatalf("没有探测记录时不应返回游标 got=%s", exhausted.NextCursor)
	}

	// 手工构造早于全部条目的游标，第二次读取必然为空页
	older := encodeFeedTestCursor(t, feedBaseTime.Add(-time.Hour), 1)
	emptyURL := base + "/api/feed?limit=2&cursor=" + url.QueryEscape(older)
	emptyStatus, emptyBody := rawStatusBody(t, client, emptyURL)
	if emptyStatus != http.StatusOK {
		t.Fatalf("空页请求应成功 got=%d body=%s", emptyStatus, emptyBody)
	}
	if !bytes.Contains(emptyBody, []byte(`"items":[]`)) {
		t.Fatalf("空页应返回空条目数组 got=%s", emptyBody)
	}
	if bytes.Contains(emptyBody, []byte("next_cursor")) {
		t.Fatalf("空页不应返回游标 got=%s", emptyBody)
	}
	replayStatus, replayBody := rawStatusBody(t, client, emptyURL)
	if replayStatus != http.StatusOK || !bytes.Equal(replayBody, emptyBody) {
		t.Fatalf("空页重复请求应保持一致 got=%d body=%s", replayStatus, replayBody)
	}
}

// 测试目标：验证 Redis 不可用时时间线仍从 MySQL 正常返回
// 预期效果：首屏与翻页都返回 200 且与关闭缓存时逐字节一致，不出现 503
func TestFeedServesFromMySQLWhenRedisUnreachable(t *testing.T) {
	env := newFeedTestEnv(t)
	oracle, oracleClient := env.newServer(t, nil)
	deadRuntime := newUnreachableRedisRuntime(t)
	deadContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := deadRuntime.EnsureConnected(deadContext); err == nil {
		t.Fatal("未监听端口不应连接成功")
	}
	pageCache, err := infracachefeed.NewPageCache(deadRuntime, infracachefeed.PageCacheOptions{})
	if err != nil {
		t.Fatalf("构造故障页缓存失败: %v", err)
	}
	broken, brokenClient := env.newServer(t, pageCache)
	base := oracle.URL
	register(t, oracleClient, base, "feed_dead_redis_author", "feed-dead-redis-password-123")
	session := login(t, oracleClient, base, "feed_dead_redis_author", "feed-dead-redis-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, oracleClient, 3)

	var firstPage feedTimelineResponse
	doJSON(t, oracleClient, http.MethodGet, base+"/api/feed?limit=1", "", nil, http.StatusOK, &firstPage)
	if firstPage.NextCursor == "" || firstPage.Items[0].ID != items[0].ID {
		t.Fatalf("首屏应返回最新视频与游标 got=%+v", firstPage)
	}
	cursorPath := "/api/feed?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)

	for _, path := range []string{"/api/feed?limit=1", cursorPath} {
		oracleStatus, oracleBody := rawStatusBody(t, oracleClient, base+path)
		brokenStatus, brokenBody := rawStatusBody(t, brokenClient, broken.URL+path)
		if oracleStatus != http.StatusOK || brokenStatus != http.StatusOK {
			t.Fatalf("Redis 不可用时路径 %s 应返回 200 got=%d/%d", path, oracleStatus, brokenStatus)
		}
		if !bytes.Equal(oracleBody, brokenBody) {
			t.Fatalf("Redis 不可用时响应应与 MySQL 结果一致 path=%s broken=%s oracle=%s", path, brokenBody, oracleBody)
		}
	}

	replayStatus, replayBody := rawStatusBody(t, brokenClient, broken.URL+cursorPath)
	if replayStatus != http.StatusOK {
		t.Fatalf("Redis 持续不可用时翻页仍应成功 got=%d body=%s", replayStatus, replayBody)
	}
	oracleStatus, oracleBody := rawStatusBody(t, oracleClient, base+cursorPath)
	if oracleStatus != http.StatusOK || !bytes.Equal(replayBody, oracleBody) {
		t.Fatalf("故障重复出现时响应不应漂移 got=%s", replayBody)
	}
}

// 测试目标：验证命中路径上的卡片读取故障不会被伪装成空页
// 预期效果：注入 videos 表故障后请求返回 503 且不出现条目字段，解除注入后恢复
func TestFeedCardReadFailureIsNotAnEmptyPage(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_card_fault_author", "feed-card-fault-password-123")
	session := login(t, client, base, "feed_card_fault_author", "feed-card-fault-password-123")
	_, secondURL, _ := setupCachedFeedPage(t, env, base, session.AccessToken, client)

	key := env.recorder.writes()[0]
	good := env.recorder.readValue(t, key)
	beforeStatus, beforeBody := rawStatusBody(t, client, secondURL)
	if beforeStatus != http.StatusOK {
		t.Fatalf("注入前命中请求应成功 got=%d body=%s", beforeStatus, beforeBody)
	}

	env.faults.arm("videos", errors.New("injected feed card outage"))
	var faultBody []byte
	faultBudget := measuredQueryCount(t, env.capture, func() {
		var status int
		status, faultBody = rawStatusBody(t, client, secondURL)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("卡片读取故障应返回 503 got=%d body=%s", status, faultBody)
		}
	})
	if faultBudget < 1 {
		t.Fatalf("命中路径必须真正查询 MySQL got=%d", faultBudget)
	}
	if bytes.Contains(faultBody, []byte(`"items"`)) {
		t.Fatalf("故障不应返回半组装条目 got=%s", faultBody)
	}
	if !bytes.Contains(faultBody, []byte("feed temporarily unavailable")) {
		t.Fatalf("故障应返回固定公开文案 got=%s", faultBody)
	}
	if cached := env.recorder.readValue(t, key); cached != good {
		t.Fatalf("故障不应改写已回填的页缓存 got=%s", cached)
	}

	env.faults.disarm()
	recoveredStatus, recoveredBody := rawStatusBody(t, client, secondURL)
	if recoveredStatus != http.StatusOK || !bytes.Equal(recoveredBody, beforeBody) {
		t.Fatalf("解除注入后应恢复原响应 got=%d body=%s", recoveredStatus, recoveredBody)
	}
}

// 测试目标：验证 Redis 不可用不影响仍只依赖 MySQL 的就绪检查
// 预期效果：/ready 与 /health 返回 200，同一进程上的时间线也仍可读
func TestFeedReadinessStaysAvailableWhenRedisUnreachable(t *testing.T) {
	env := newFeedTestEnv(t)
	publisher, publisherClient := env.newServer(t, nil)
	base := publisher.URL
	register(t, publisherClient, base, "feed_ready_author", "feed-ready-password-123")
	session := login(t, publisherClient, base, "feed_ready_author", "feed-ready-password-123")
	publishFeedVideos(t, env, base, session.AccessToken, publisherClient, 1)

	deadRuntime := newUnreachableRedisRuntime(t)
	pageCache, err := infracachefeed.NewPageCache(deadRuntime, infracachefeed.PageCacheOptions{})
	if err != nil {
		t.Fatalf("构造故障页缓存失败: %v", err)
	}
	engine := New(env.gdb, false, Options{
		UploadDir:      t.TempDir(),
		FeedPageCache:  pageCache,
		RateLimitCache: deadRuntime,
	})
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	client := server.Client()

	deadContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := deadRuntime.EnsureConnected(deadContext); err == nil {
		t.Fatal("未监听端口不应连接成功")
	}

	readyStatus, readyBody := rawStatusBody(t, client, server.URL+"/ready")
	if readyStatus != http.StatusOK {
		t.Fatalf("Redis 不可用不应影响就绪检查 got=%d body=%s", readyStatus, readyBody)
	}
	var ready struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(readyBody, &ready); err != nil || ready.Status != "ready" {
		t.Fatalf("就绪检查应报告可用 got=%s err=%v", readyBody, err)
	}
	healthStatus, healthBody := rawStatusBody(t, client, server.URL+"/health")
	if healthStatus != http.StatusOK {
		t.Fatalf("存活检查应返回 200 got=%d body=%s", healthStatus, healthBody)
	}
	feedStatus, feedBody := rawStatusBody(t, client, server.URL+"/api/feed")
	if feedStatus != http.StatusOK || !bytes.Contains(feedBody, []byte(`"items":[`)) {
		t.Fatalf("Redis 不可用不影响时间线读取 got=%d body=%s", feedStatus, feedBody)
	}
}

// 测试目标：验证旧视频接口游标不能用于新时间线接口
// 预期效果：旧游标请求时间线返回 400，而旧接口自身仍可用同一游标继续翻页
func TestFeedRejectsLegacyVideoCursor(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, nil)
	base := server.URL
	register(t, client, base, "feed_isolation_author", "feed-isolation-password-123")
	session := login(t, client, base, "feed_isolation_author", "feed-isolation-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, client, 2)

	var legacyPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=1", "", nil, http.StatusOK, &legacyPage)
	if len(legacyPage.Items) != 1 || legacyPage.Items[0].ID != items[0].ID || legacyPage.NextCursor == "" {
		t.Fatalf("旧接口首屏应返回最新视频与游标 got=%+v", legacyPage)
	}

	var invalidBody map[string]any
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=1&cursor="+url.QueryEscape(legacyPage.NextCursor), "", nil, http.StatusBadRequest, &invalidBody)
	if _, hasItems := invalidBody["items"]; hasItems {
		t.Fatalf("非法游标不应返回条目 got=%v", invalidBody)
	}

	var legacyNext struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=1&cursor="+url.QueryEscape(legacyPage.NextCursor), "", nil, http.StatusOK, &legacyNext)
	if len(legacyNext.Items) != 1 || legacyNext.Items[0].ID != items[1].ID {
		t.Fatalf("旧接口自身应继续接受该游标 got=%+v", legacyNext.Items)
	}
}
