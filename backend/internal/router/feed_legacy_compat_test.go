package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	videoModel "gofeed/internal/video"
)

// 测试目标：判断两个时间线游标是否指向同一读取位置
// 预期效果：只比较版本、场景、排序版本与位置，忽略时间字段的时区渲染
func sameFeedCursorPosition(a, b feedTimelineCursor) bool {
	return a.Version == b.Version &&
		a.Scene == b.Scene &&
		a.SortVersion == b.SortVersion &&
		a.VideoID == b.VideoID &&
		a.PublishedAt.Equal(b.PublishedAt)
}

// 测试目标：比较两个装配下同一时间线请求的语义一致性
// 预期效果：条目逐字节一致且游标指向同一位置，允许游标时间以不同时区渲染
func assertSameTimelineResponse(t *testing.T, label string, wantStatus int, wantBody []byte, gotStatus int, gotBody []byte) {
	t.Helper()
	if gotStatus != wantStatus {
		t.Fatalf("%s 状态码不一致 got=%d want=%d body=%s", label, gotStatus, wantStatus, gotBody)
	}
	var want, got struct {
		Items      json.RawMessage `json:"items"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(wantBody, &want); err != nil {
		t.Fatalf("%s 基准响应不是合法 JSON: %v", label, err)
	}
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("%s 对比响应不是合法 JSON: %v", label, err)
	}
	if !bytes.Equal(want.Items, got.Items) {
		t.Fatalf("%s 条目应逐字节一致 got=%s want=%s", label, got.Items, want.Items)
	}
	if (want.NextCursor == "") != (got.NextCursor == "") {
		t.Fatalf("%s 游标有无应一致 got=%q want=%q", label, got.NextCursor, want.NextCursor)
	}
	if want.NextCursor != "" && !sameFeedCursorPosition(decodeFeedCursor(t, got.NextCursor), decodeFeedCursor(t, want.NextCursor)) {
		t.Fatalf("%s 游标应指向同一位置 got=%q want=%q", label, got.NextCursor, want.NextCursor)
	}
}

// 测试目标：按页遍历旧公开列表接口收集全部条目标识
// 预期效果：返回跨页拼接的顺序，暴露分页边界差异
func collectLegacyPageIDs(t *testing.T, client *http.Client, base, path string) []uint {
	t.Helper()
	ids := make([]uint, 0, 8)
	next := ""
	for page := 0; page < 10; page++ {
		requestURL := base + path
		if next != "" {
			requestURL += "&cursor=" + url.QueryEscape(next)
		}
		var response struct {
			Items      []videoItem `json:"items"`
			NextCursor string      `json:"next_cursor"`
		}
		doJSON(t, client, http.MethodGet, requestURL, "", nil, http.StatusOK, &response)
		if len(response.Items) == 0 {
			t.Fatalf("旧接口遍历出现空页 page=%d", page)
		}
		for _, item := range response.Items {
			ids = append(ids, item.ID)
		}
		if response.NextCursor == "" {
			return ids
		}
		next = response.NextCursor
	}
	t.Fatal("旧接口分页遍历超过预期页数")
	return nil
}

// 测试目标：按页遍历时间线接口收集全部条目标识
// 预期效果：返回跨页拼接的顺序，暴露分页边界差异
func collectFeedPageIDs(t *testing.T, client *http.Client, base, path string) []uint {
	t.Helper()
	ids := make([]uint, 0, 8)
	next := ""
	for page := 0; page < 10; page++ {
		requestURL := base + path
		if next != "" {
			requestURL += "&cursor=" + url.QueryEscape(next)
		}
		var response feedTimelineResponse
		doJSON(t, client, http.MethodGet, requestURL, "", nil, http.StatusOK, &response)
		if len(response.Items) == 0 {
			t.Fatalf("时间线遍历出现空页 page=%d", page)
		}
		for _, item := range response.Items {
			ids = append(ids, item.ID)
		}
		if response.NextCursor == "" {
			return ids
		}
		next = response.NextCursor
	}
	t.Fatal("时间线分页遍历超过预期页数")
	return nil
}

// 测试目标：验证页缓存开关不改变旧视频接口与新时间线的公开响应
// 预期效果：同一批数据在关闭与启用缓存时响应一致且查询预算相同，旧接口从不触碰页缓存
func TestFeedPageCacheSwitchKeepsLegacyContracts(t *testing.T) {
	env := newFeedTestEnv(t)
	uncached, uncachedClient := env.newServer(t, nil)
	cached, cachedClient := env.newServer(t, env.livePageCache(t))
	base := uncached.URL
	register(t, uncachedClient, base, "feed_switch_author", "feed-switch-password-123")
	session := login(t, uncachedClient, base, "feed_switch_author", "feed-switch-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, uncachedClient, 3)

	var legacyFirst struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, uncachedClient, http.MethodGet, base+"/api/video?limit=1", "", nil, http.StatusOK, &legacyFirst)
	if legacyFirst.NextCursor == "" || legacyFirst.Items[0].ID != items[0].ID {
		t.Fatalf("旧接口首屏应返回最新视频与游标 got=%+v", legacyFirst)
	}
	var feedFirst feedTimelineResponse
	doJSON(t, uncachedClient, http.MethodGet, base+"/api/feed?limit=1", "", nil, http.StatusOK, &feedFirst)
	if feedFirst.NextCursor == "" || feedFirst.Items[0].ID != items[0].ID {
		t.Fatalf("时间线首屏应返回最新视频与游标 got=%+v", feedFirst)
	}

	legacyPaths := []string{
		"/api/video",
		"/api/video?limit=1",
		"/api/video?limit=1&cursor=" + url.QueryEscape(legacyFirst.NextCursor),
		"/api/video?author_id=" + strconv.FormatUint(uint64(session.UserID), 10),
		"/api/video?limit=1&cursor=not-a-legacy-cursor",
		"/api/video?limit=0",
	}
	for _, path := range legacyPaths {
		uncachedStatus, uncachedBody := rawStatusBody(t, uncachedClient, base+path)
		cachedStatus, cachedBody := rawStatusBody(t, cachedClient, cached.URL+path)
		if cachedStatus != uncachedStatus || !bytes.Equal(cachedBody, uncachedBody) {
			t.Fatalf("旧接口响应应不受页缓存开关影响 path=%s got=%d/%s want=%d/%s", path, cachedStatus, cachedBody, uncachedStatus, uncachedBody)
		}
	}

	feedPaths := []string{
		"/api/feed",
		"/api/feed?limit=1",
		"/api/feed?limit=1&cursor=" + url.QueryEscape(feedFirst.NextCursor),
		"/api/feed?limit=0",
		"/api/feed?limit=1&cursor=not-a-feed-cursor",
	}
	for _, path := range feedPaths {
		uncachedStatus, uncachedBody := rawStatusBody(t, uncachedClient, base+path)
		cachedStatus, cachedBody := rawStatusBody(t, cachedClient, cached.URL+path)
		assertSameTimelineResponse(t, path, uncachedStatus, uncachedBody, cachedStatus, cachedBody)
		// 第二轮请求命中已回填的页缓存，响应也必须保持同一语义
		replayStatus, replayBody := rawStatusBody(t, cachedClient, cached.URL+path)
		assertSameTimelineResponse(t, path+" 命中缓存", uncachedStatus, uncachedBody, replayStatus, replayBody)
	}

	uncachedLegacyBudget := measuredQueryCount(t, env.capture, func() {
		doJSON(t, uncachedClient, http.MethodGet, base+"/api/video", "", nil, http.StatusOK, &struct {
			Items []videoItem `json:"items"`
		}{})
	})
	cachedLegacyBudget := measuredQueryCount(t, env.capture, func() {
		doJSON(t, cachedClient, http.MethodGet, cached.URL+"/api/video", "", nil, http.StatusOK, &struct {
			Items []videoItem `json:"items"`
		}{})
	})
	if uncachedLegacyBudget != cachedLegacyBudget || cachedLegacyBudget < 1 {
		t.Fatalf("页缓存开关不应改变旧接口查询预算 got=%d want=%d", cachedLegacyBudget, uncachedLegacyBudget)
	}
	cursorPath := "/api/feed?limit=1&cursor=" + url.QueryEscape(feedFirst.NextCursor)
	uncachedFeedBudget := measuredQueryCount(t, env.capture, func() {
		doJSON(t, uncachedClient, http.MethodGet, base+cursorPath, "", nil, http.StatusOK, &feedTimelineResponse{})
	})
	cachedFeedBudget := measuredQueryCount(t, env.capture, func() {
		doJSON(t, cachedClient, http.MethodGet, cached.URL+cursorPath, "", nil, http.StatusOK, &feedTimelineResponse{})
	})
	if uncachedFeedBudget != cachedFeedBudget || cachedFeedBudget < 1 {
		t.Fatalf("命中页缓存仍应查询 MySQL 且预算与关闭缓存一致 got=%d want=%d", cachedFeedBudget, uncachedFeedBudget)
	}

	readCount, writeCount := len(env.recorder.reads()), len(env.recorder.writes())
	for _, path := range []string{
		"/api/video",
		fmt.Sprintf("/api/video/%d", items[0].ID),
		fmt.Sprintf("/api/video/%d/comments", items[0].ID),
		"/api/video?limit=1&cursor=" + url.QueryEscape(legacyFirst.NextCursor),
	} {
		rawStatusBody(t, cachedClient, cached.URL+path)
	}
	if reads, writes := len(env.recorder.reads()), len(env.recorder.writes()); reads != readCount || writes != writeCount {
		t.Fatalf("旧视频接口不应触碰 Feed 页缓存 reads=%d->%d writes=%d->%d", readCount, reads, writeCount, writes)
	}
}

// 测试目标：验证新时间线与旧公开列表的内容、排序与展示字段一致
// 预期效果：两个接口返回相同的视频集合与顺序，媒体展示字段与分页边界逐项相同
func TestFeedTimelineMatchesLegacyPublicList(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, nil)
	base := server.URL
	register(t, client, base, "feed_parity_author", "feed-parity-password-123")
	session := login(t, client, base, "feed_parity_author", "feed-parity-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, client, 5)

	// 让两条视频共享同一发布时间，排序只能由标识倒序决定
	sharedTime := feedBaseTime.Add(-time.Minute)
	setFeedVideoPublishedAt(t, env, items[2].ID, sharedTime)
	setFeedVideoPublishedAt(t, env, items[4].ID, sharedTime)
	wantOrder := []uint{items[0].ID, items[1].ID, items[3].ID, items[4].ID, items[2].ID}
	wantPublished := map[uint]time.Time{
		items[0].ID: feedBaseTime,
		items[1].ID: feedBaseTime.Add(-10 * time.Second),
		items[3].ID: feedBaseTime.Add(-30 * time.Second),
		items[2].ID: sharedTime,
		items[4].ID: sharedTime,
	}

	var legacy struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=20", "", nil, http.StatusOK, &legacy)
	var timeline feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=20", "", nil, http.StatusOK, &timeline)
	if len(legacy.Items) != 5 || len(timeline.Items) != 5 {
		t.Fatalf("两个接口都应返回全部公开条目 got=%d/%d", len(legacy.Items), len(timeline.Items))
	}
	for index, want := range wantOrder {
		if legacy.Items[index].ID != want || timeline.Items[index].ID != want {
			t.Fatalf("两个接口的顺序应一致 index=%d got=%d/%d want=%d", index, legacy.Items[index].ID, timeline.Items[index].ID, want)
		}
	}

	for index := range wantOrder {
		old, current := legacy.Items[index], timeline.Items[index]
		switch {
		case old.Title != current.Title:
			t.Fatalf("标题应一致 got=%q want=%q", current.Title, old.Title)
		case old.PlayURL != current.PlayURL:
			t.Fatalf("播放地址应一致 got=%q want=%q", current.PlayURL, old.PlayURL)
		case old.PlayFileName != current.PlayFileName:
			t.Fatalf("播放文件名应一致 got=%q want=%q", current.PlayFileName, old.PlayFileName)
		case old.PlayOriginalName != current.PlayOriginalName:
			t.Fatalf("播放原始名应一致 got=%q want=%q", current.PlayOriginalName, old.PlayOriginalName)
		case old.CoverURL != current.CoverURL:
			t.Fatalf("封面地址应一致 got=%q want=%q", current.CoverURL, old.CoverURL)
		case old.CoverFileName != current.CoverFileName:
			t.Fatalf("封面文件名应一致 got=%q want=%q", current.CoverFileName, old.CoverFileName)
		case old.CoverOriginalName != current.CoverOriginalName:
			t.Fatalf("封面原始名应一致 got=%q want=%q", current.CoverOriginalName, old.CoverOriginalName)
		case old.LikesCount != current.LikesCount || old.CommentsCount != current.CommentsCount:
			t.Fatalf("互动计数应一致 got=%d/%d want=%d/%d", current.LikesCount, current.CommentsCount, old.LikesCount, old.CommentsCount)
		case old.Author.ID != current.Author.ID || old.Author.Username != current.Author.Username:
			t.Fatalf("作者展示应一致 got=%+v want=%+v", current.Author, old.Author)
		case !current.PublishedAt.Equal(wantPublished[current.ID]):
			t.Fatalf("发布时间应为预期值 got=%s want=%s", current.PublishedAt, wantPublished[current.ID])
		}
	}

	legacyPageIDs := collectLegacyPageIDs(t, client, base, "/api/video?limit=2")
	feedPageIDs := collectFeedPageIDs(t, client, base, "/api/feed?limit=2")
	if len(legacyPageIDs) != len(wantOrder) || len(feedPageIDs) != len(wantOrder) {
		t.Fatalf("分页遍历应覆盖全部条目 got=%d/%d want=%d", len(legacyPageIDs), len(feedPageIDs), len(wantOrder))
	}
	for index, want := range wantOrder {
		if legacyPageIDs[index] != want || feedPageIDs[index] != want {
			t.Fatalf("分页边界应一致 index=%d got=%d/%d want=%d", index, legacyPageIDs[index], feedPageIDs[index], want)
		}
	}
}

// 测试目标：验证新时间线保持旧公开查询的可见性与注销作者占位语义
// 预期效果：软删、媒体不完整与非公开条目在两个接口都消失，注销作者仍以占位名展示
func TestFeedTimelineKeepsLegacyVisibilitySemantics(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, nil)
	base := server.URL
	register(t, client, base, "feed_visibility_author", "feed-visibility-password-123")
	session := login(t, client, base, "feed_visibility_author", "feed-visibility-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, client, 4)

	register(t, client, base, "feed_visibility_ghost", "feed-visibility-ghost-password-123")
	ghost := login(t, client, base, "feed_visibility_ghost", "feed-visibility-ghost-password-123")
	ghostItem := publishCompleteVideo(t, env.gdb, client, base, ghost.AccessToken, "注销作者视频")
	setFeedVideoPublishedAt(t, env, ghostItem.ID, feedBaseTime.Add(10*time.Second))

	deleteResult := env.gdb.Exec("UPDATE videos SET deleted_at = NOW(3) WHERE id = ?", items[1].ID)
	if deleteResult.Error != nil || deleteResult.RowsAffected != 1 {
		t.Fatalf("软删除失败 rows=%d err=%v", deleteResult.RowsAffected, deleteResult.Error)
	}
	incompleteResult := env.gdb.Exec("UPDATE videos SET cover_original_name = '' WHERE id = ?", items[2].ID)
	if incompleteResult.Error != nil || incompleteResult.RowsAffected != 1 {
		t.Fatalf("改写媒体字段失败 rows=%d err=%v", incompleteResult.RowsAffected, incompleteResult.Error)
	}
	setFeedVideoStatus(t, env, items[3].ID, videoModel.VideoStatusProcessing)
	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", ghost.AccessToken, nil, http.StatusNoContent, nil)

	var legacy struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=20", "", nil, http.StatusOK, &legacy)
	var timeline feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=20", "", nil, http.StatusOK, &timeline)
	wantOrder := []uint{ghostItem.ID, items[0].ID}
	if len(legacy.Items) != len(wantOrder) || len(timeline.Items) != len(wantOrder) {
		t.Fatalf("两个接口都只应保留公开条目 got=%d/%d want=%d", len(legacy.Items), len(timeline.Items), len(wantOrder))
	}
	for index, want := range wantOrder {
		if legacy.Items[index].ID != want || timeline.Items[index].ID != want {
			t.Fatalf("两个接口的可见集合应一致 index=%d got=%d/%d want=%d", index, legacy.Items[index].ID, timeline.Items[index].ID, want)
		}
	}
	if timeline.Items[0].Author.ID != ghost.UserID || timeline.Items[0].Author.Username != "已注销用户" {
		t.Fatalf("注销作者应展示占位身份 got=%+v", timeline.Items[0].Author)
	}
	if legacy.Items[0].Author.Username != timeline.Items[0].Author.Username {
		t.Fatalf("两个接口的占位作者应一致 got=%q want=%q", timeline.Items[0].Author.Username, legacy.Items[0].Author.Username)
	}
}

// 测试目标：验证页缓存命中时互动统计仍来自当前数据
// 预期效果：点赞与评论变化在命中缓存的翻页请求上立即可见且不改变分页位置
func TestFeedTimelineReflectsEngagementOnCacheHit(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, env.livePageCache(t))
	base := server.URL
	register(t, client, base, "feed_engagement_author", "feed-engagement-password-123")
	author := login(t, client, base, "feed_engagement_author", "feed-engagement-password-123")
	items, secondURL, secondPage := setupCachedFeedPage(t, env, base, author.AccessToken, client)
	if secondPage.Items[0].LikesCount != 0 || secondPage.Items[0].CommentsCount != 0 {
		t.Fatalf("初始互动计数应为零 got=%+v", secondPage.Items[0])
	}

	register(t, client, base, "feed_engagement_viewer", "feed-engagement-viewer-password-123")
	viewer := login(t, client, base, "feed_engagement_viewer", "feed-engagement-viewer-password-123")
	target := items[1].ID
	var likeState struct {
		Liked      bool  `json:"liked"`
		LikesCount int64 `json:"likes_count"`
	}
	doJSON(t, client, http.MethodPut, fmt.Sprintf("%s/api/video/auth/%d/like", base, target), viewer.AccessToken, nil, http.StatusOK, &likeState)
	if !likeState.Liked || likeState.LikesCount != 1 {
		t.Fatalf("点赞状态错误 got=%+v", likeState)
	}
	doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/video/auth/%d/comments", base, target), viewer.AccessToken, map[string]string{"content": "命中缓存的评论"}, http.StatusCreated, nil)

	var after feedTimelineResponse
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &after)
	if len(after.Items) != 1 || after.Items[0].ID != target {
		t.Fatalf("命中缓存后应仍返回同一条目 got=%+v", after.Items)
	}
	if after.Items[0].LikesCount != 1 || after.Items[0].CommentsCount != 1 {
		t.Fatalf("命中缓存应立即反映互动变化 got=%d/%d", after.Items[0].LikesCount, after.Items[0].CommentsCount)
	}
	if !sameFeedCursorPosition(decodeFeedCursor(t, after.NextCursor), decodeFeedCursor(t, secondPage.NextCursor)) {
		t.Fatalf("互动变化不应改变分页位置 got=%s want=%s", after.NextCursor, secondPage.NextCursor)
	}
	if writes := env.recorder.writes(); len(writes) != 1 {
		t.Fatalf("互动变化后的请求应命中页缓存且不回填 got=%d", len(writes))
	}

	var legacy struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=20", "", nil, http.StatusOK, &legacy)
	for _, item := range legacy.Items {
		if item.ID != target {
			continue
		}
		if item.LikesCount != 1 || item.CommentsCount != 1 {
			t.Fatalf("旧接口互动计数应与时间线一致 got=%d/%d", item.LikesCount, item.CommentsCount)
		}
		return
	}
	t.Fatalf("旧接口应包含该条目 got=%+v", legacy.Items)
}
