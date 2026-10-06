package video

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	authn "gofeed/internal/auth"
	jwtmw "gofeed/internal/middleware/jwt"
	"gofeed/internal/testutil"
	"gofeed/internal/user"
)

// 测试目标：固定仓储测试的基准时间
// 预期效果：统一使用本地时区，避免读写往返产生时区断言差异
var baseTime = time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)

func timePtr(value time.Time) *time.Time {
	return &value
}

// 测试目标：配置视频仓储集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：构造字段齐全的视频测试数据
// 预期效果：调用方可指定作者、标题、状态和发布时间
func newVideoFixture(authorID uint, title, status string, publishedAt time.Time) *Video {
	return &Video{
		AuthorID:          authorID,
		Title:             title,
		Description:       "集成测试描述",
		PlayURL:           "/static/videos/1/a.mp4",
		PlayFileName:      "a.mp4",
		PlayOriginalName:  "原始视频.mp4",
		CoverURL:          "/static/covers/1/a.webp",
		CoverFileName:     "a.webp",
		CoverOriginalName: "封面.webp",
		Status:            status,
		PublishedAt:       timePtr(publishedAt),
	}
}

// 测试目标：通过仓储写入一条视频测试数据
// 预期效果：返回已回填视频标识的实体
func seedVideo(t *testing.T, repo *Repository, authorID uint, title, status string, publishedAt time.Time) *Video {
	t.Helper()
	v := newVideoFixture(authorID, title, status, publishedAt)
	if err := repo.Create(context.Background(), v); err != nil {
		t.Fatalf("写入视频 %q 失败: %v", title, err)
	}
	return v
}

// 测试目标：设置视频软删除时间以构造宽限期边界
// 预期效果：测试可准确覆盖到期、边界和未到期三种记录
func setVideoDeletedAt(t *testing.T, db *gorm.DB, id uint, at time.Time) {
	t.Helper()
	if err := db.Exec("UPDATE videos SET deleted_at = ? WHERE id = ?", at, id).Error; err != nil {
		t.Fatalf("设置视频 deleted_at 失败: %v", err)
	}
}

// 测试目标：验证草稿清扫租约独占、过期接管和逐媒体删除检查点
// 预期效果：旧 token 不能提交进度或硬删除，两个非空媒体确认后才可物理删除
func TestRepositoryDraftPurgeLeaseAndCheckpoints(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	cutoff := time.Now().Add(-time.Hour)
	expired := newVideoFixture(1, "expired", VideoStatusDraft, baseTime)
	expired.PublishedAt = nil
	active := newVideoFixture(1, "active", VideoStatusDraft, baseTime)
	active.PublishedAt = nil
	published := newVideoFixture(1, "published", VideoStatusPublished, baseTime)
	for _, item := range []*Video{expired, active, published} {
		if err := repo.Create(ctx, item); err != nil {
			t.Fatalf("创建测试视频失败: %v", err)
		}
	}
	if err := db.Exec("UPDATE videos SET created_at = ? WHERE id = ?", cutoff.Add(-time.Minute), expired.ID).Error; err != nil {
		t.Fatalf("设置过期草稿创建时间失败: %v", err)
	}
	if err := db.Exec("UPDATE videos SET created_at = ? WHERE id = ?", time.Now().Add(time.Hour), active.ID).Error; err != nil {
		t.Fatalf("设置活跃草稿创建时间失败: %v", err)
	}

	ids, err := repo.GetExpiredDraftPurgeList(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("查询过期草稿失败: %v", err)
	}
	if len(ids) != 1 || ids[0] != expired.ID {
		t.Fatalf("过期草稿筛选错误 got=%+v", ids)
	}

	tokenA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	claim, ok, err := repo.UpdateDraftPurgeClaim(ctx, expired.ID, cutoff, tokenA, time.Hour)
	if err != nil || !ok || claim == nil || claim.Token != tokenA || claim.PlayURL == "" || claim.CoverURL == "" {
		t.Fatalf("认领过期草稿错误 claim=%+v ok=%t err=%v", claim, ok, err)
	}
	if _, err := repo.UpdateDraftPublication(ctx, expired.ID, 1); !errors.Is(err, ErrDraftNotWritable) {
		t.Fatalf("清扫中的草稿仍可发布 error=%v", err)
	}
	if _, ok, err := repo.UpdateDraftPurgeClaim(ctx, expired.ID, cutoff, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", time.Hour); err != nil || ok {
		t.Fatalf("未过期租约不应被接管 ok=%t err=%v", ok, err)
	}
	if err := db.Model(&Video{}).Where("id = ?", expired.ID).Update("purge_lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("设置过期租约失败: %v", err)
	}
	ids, err = repo.GetRecoverableDraftPurgeList(ctx, 10)
	if err != nil || len(ids) != 1 || ids[0] != expired.ID {
		t.Fatalf("过期租约应重新成为候选 ids=%v err=%v", ids, err)
	}

	tokenB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	claim, ok, err = repo.UpdateDraftPurgeClaim(ctx, expired.ID, cutoff, tokenB, time.Hour)
	if err != nil || !ok || claim == nil || claim.Token != tokenB {
		t.Fatalf("过期租约接管失败 claim=%+v ok=%t err=%v", claim, ok, err)
	}
	if renewed, err := repo.UpdateDraftPurgeLease(ctx, expired.ID, tokenA, time.Hour); err != nil || renewed {
		t.Fatalf("旧 token 不应续租 renewed=%t err=%v", renewed, err)
	}
	if marked, err := repo.UpdateDraftMediaPurge(ctx, expired.ID, tokenA, MediaVideo, time.Hour); err != nil || marked {
		t.Fatalf("旧 token 不应写入进度 marked=%t err=%v", marked, err)
	}
	if marked, err := repo.UpdateDraftMediaPurge(ctx, expired.ID, tokenB, MediaVideo, time.Hour); err != nil || !marked {
		t.Fatalf("视频删除检查点写入失败 marked=%t err=%v", marked, err)
	}
	if deleted, err := repo.RemovePurgedDraft(ctx, expired.ID, tokenB); err != nil || deleted {
		t.Fatalf("封面未确认前不应硬删除 deleted=%t err=%v", deleted, err)
	}
	if marked, err := repo.UpdateDraftMediaPurge(ctx, expired.ID, tokenB, MediaCover, time.Hour); err != nil || !marked {
		t.Fatalf("封面删除检查点写入失败 marked=%t err=%v", marked, err)
	}
	if deleted, err := repo.RemovePurgedDraft(ctx, expired.ID, tokenA); err != nil || deleted {
		t.Fatalf("旧 token 不应硬删除 deleted=%t err=%v", deleted, err)
	}
	deleted, err := repo.RemovePurgedDraft(ctx, expired.ID, tokenB)
	if err != nil || !deleted {
		t.Fatalf("硬删除已认领草稿失败 deleted=%t err=%v", deleted, err)
	}
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM videos WHERE id = ?", expired.ID).Scan(&count).Error; err != nil {
		t.Fatalf("统计已删除草稿失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("过期草稿应已硬删除 count=%d", count)
	}
}

// 测试目标：验证两个仓储实例并发认领同一过期草稿时租约互斥
// 预期效果：恰好一个 token 成功取得草稿，另一个实例不能获得清扫所有权
func TestRepositoryClaimDraftPurgeIsExclusive(t *testing.T) {
	db := testutil.DB(t)
	first := NewRepository(db)
	second := NewRepository(db.Session(&gorm.Session{NewDB: true}))
	ctx := context.Background()
	cutoff := time.Now().Add(-time.Hour)
	draft := newVideoFixture(1, "concurrent claim", VideoStatusDraft, baseTime)
	draft.PublishedAt = nil
	if err := first.Create(ctx, draft); err != nil {
		t.Fatalf("创建并发认领草稿失败: %v", err)
	}
	if err := db.Exec("UPDATE videos SET created_at = ? WHERE id = ?", cutoff.Add(-time.Minute), draft.ID).Error; err != nil {
		t.Fatalf("设置过期草稿创建时间失败: %v", err)
	}

	type result struct {
		token string
		ok    bool
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, item := range []struct {
		repository *Repository
		token      string
	}{
		{repository: first, token: "11111111111111111111111111111111"},
		{repository: second, token: "22222222222222222222222222222222"},
	} {
		item := item
		go func() {
			<-start
			_, ok, err := item.repository.UpdateDraftPurgeClaim(ctx, draft.ID, cutoff, item.token, time.Minute)
			results <- result{token: item.token, ok: ok, err: err}
		}()
	}
	close(start)

	var winner string
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("并发认领失败 token=%s err=%v", result.token, result.err)
		}
		if result.ok {
			if winner != "" {
				t.Fatalf("不应有两个租约 owner first=%s second=%s", winner, result.token)
			}
			winner = result.token
		}
	}
	if winner == "" {
		t.Fatal("并发认领应有一个 token 成功")
	}
	var claimed Video
	if err := db.First(&claimed, draft.ID).Error; err != nil {
		t.Fatalf("读取并发认领结果失败: %v", err)
	}
	if claimed.Status != VideoStatusPurging || claimed.PurgeToken == nil || *claimed.PurgeToken != winner {
		t.Fatalf("并发认领持久化结果错误 status=%s token=%v winner=%s", claimed.Status, claimed.PurgeToken, winner)
	}
}

// 测试目标：验证仓储只软删除作者的已发布视频
// 预期效果：草稿不会进入已发布视频清扫路径，已发布行仍保持软删除语义
func TestRepositorySoftDeletePublished(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	v := seedVideo(t, repo, 1, "删除", VideoStatusPublished, baseTime)

	if err := repo.DeletePublishedVideo(ctx, v.ID, v.AuthorID); err != nil {
		t.Fatalf("DeletePublishedVideo: %v", err)
	}
	if _, err := repo.GetByID(ctx, v.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("软删后 GetByID 应 not found, err=%v", err)
	}
	if _, err := repo.GetPublishedByID(ctx, v.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("软删后公开读应 not found, err=%v", err)
	}

	// 测试目标：检查软删除后的物理记录
	// 预期效果：物理行保留且删除时间已写入
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM videos WHERE id = ?", v.ID).Scan(&count).Error; err != nil {
		t.Fatalf("原生统计失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("物理行应保留, count=%d", count)
	}
	var deletedAt *time.Time
	if err := db.Raw("SELECT deleted_at FROM videos WHERE id = ?", v.ID).Scan(&deletedAt).Error; err != nil {
		t.Fatalf("原生读 deleted_at 失败: %v", err)
	}
	if deletedAt == nil {
		t.Fatal("deleted_at 应为非空")
	}

	items, err := repo.GetPublishedVideoList(ctx, 1, nil, 10)
	if err != nil {
		t.Fatalf("删除后列表查询失败: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("删除后列表应不含该视频, got=%+v", items)
	}

	if err := repo.DeletePublishedVideo(ctx, v.ID, v.AuthorID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("重复删除应 not found, err=%v", err)
	}

	draft := seedVideo(t, repo, 1, "草稿", VideoStatusDraft, baseTime)
	if err := repo.DeletePublishedVideo(ctx, draft.ID, draft.AuthorID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("草稿不应走已发布软删除 err=%v", err)
	}
	if _, err := repo.GetByID(ctx, draft.ID); err != nil {
		t.Fatalf("草稿不应被软删除 err=%v", err)
	}
}

// videoJSONRequest 发送可选 JSON 请求体与可选访问令牌的 HTTP 请求
func videoJSONRequest(t *testing.T, engine *gin.Engine, method, path, token string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if payload == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	request := httptest.NewRequest(method, path, reader)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// videoResponseBody 解析响应体为通用映射以便断言字段契约
func videoResponseBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v body=%s", err, recorder.Body.String())
	}
	return body
}

// videoErrorMessage 断言状态码并读取错误响应的公开文案
func videoErrorMessage(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) string {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	message, ok := videoResponseBody(t, recorder)["error"].(string)
	if !ok {
		t.Fatalf("错误响应缺少 error 字段 body=%s", recorder.Body.String())
	}
	return message
}

// videoListIDs 读取列表响应中的视频标识顺序
func videoListIDs(t *testing.T, recorder *httptest.ResponseRecorder) []uint {
	t.Helper()
	value, exists := videoResponseBody(t, recorder)["items"]
	if !exists {
		t.Fatalf("列表响应缺少 items body=%s", recorder.Body.String())
	}
	if value == nil {
		return []uint{}
	}
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("列表响应 items 格式错误 body=%s", recorder.Body.String())
	}
	ids := make([]uint, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("列表项格式错误 got=%v", entry)
		}
		value, ok := item["id"].(float64)
		if !ok {
			t.Fatalf("列表项缺少标识 got=%v", item)
		}
		ids = append(ids, uint(value))
	}
	return ids
}

// assertVideoIDs 断言列表响应中的视频标识与顺序
func assertVideoIDs(t *testing.T, got, want []uint) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("列表长度错误 got=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("列表顺序错误 got=%v want=%v", got, want)
		}
	}
}

// sqlEngagementReader 使用真实 SQL 聚合互动计数以替代 social 包避免测试包循环依赖
type sqlEngagementReader struct {
	db *gorm.DB
}

func (r sqlEngagementReader) GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]EngagementCounts, error) {
	counts := make(map[uint]EngagementCounts, len(videoIDs))
	if len(videoIDs) == 0 {
		return counts, nil
	}
	for _, id := range videoIDs {
		counts[id] = EngagementCounts{}
	}

	type aggregate struct {
		VideoID uint
		Total   int64
	}
	var likes []aggregate
	if err := r.db.WithContext(ctx).Table("video_likes").
		Select("video_id, COUNT(*) AS total").Where("video_id IN ?", videoIDs).
		Group("video_id").Scan(&likes).Error; err != nil {
		return nil, err
	}
	for _, item := range likes {
		entry := counts[item.VideoID]
		entry.LikesCount = item.Total
		counts[item.VideoID] = entry
	}

	var comments []aggregate
	if err := r.db.WithContext(ctx).Table("video_comments").
		Select("video_id, COUNT(*) AS total").
		Where("video_id IN ? AND deleted_at IS NULL", videoIDs).
		Group("video_id").Scan(&comments).Error; err != nil {
		return nil, err
	}
	for _, item := range comments {
		entry := counts[item.VideoID]
		entry.CommentsCount = item.Total
		counts[item.VideoID] = entry
	}
	return counts, nil
}

// newVideoHTTPEngine 装配视频模块的公开读取与认证写入端点
func newVideoHTTPEngine(t *testing.T, middlewares ...gin.HandlerFunc) (*gin.Engine, *gorm.DB, *Repository, *authn.SessionService) {
	t.Helper()
	gdb := testutil.DB(t)
	repo := NewRepository(gdb)
	sessions := authn.NewSessionService(authn.NewSessionRepository(gdb))
	service := NewService(repo, NewUserAuthorReader(user.NewRepository(gdb)), sqlEngagementReader{db: gdb})
	controller := NewController(service, NewLocalStorage(t.TempDir()))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middlewares...)
	engine.GET("/api/video", controller.GetVideoList)
	engine.GET("/api/video/:id", controller.GetVideo)

	authorized := engine.Group("/api/video/auth", jwtmw.Auth(sessions))
	authorized.POST("/drafts", controller.CreateDraft)
	authorized.GET("/drafts/:id", controller.GetDraft)
	authorized.POST("/drafts/:id/play", controller.UpdateDraftVideo)
	authorized.POST("/drafts/:id/cover", controller.UpdateDraftCover)
	authorized.POST("/drafts/:id/publish", controller.UpdateDraftPublication)
	authorized.DELETE("/drafts/:id", controller.DiscardDraft)
	authorized.GET("/mine", controller.GetMyVideoList)
	authorized.GET("/:id/status", controller.GetVideoStatus)
	authorized.DELETE("/:id", controller.DeleteVideo)
	return engine, gdb, repo, sessions
}

// newVideoAuthor 创建作者账号并签发访问令牌
func newVideoAuthor(t *testing.T, gdb *gorm.DB, username string) (*user.User, string) {
	t.Helper()
	account := &user.User{Username: username, Password: "test-password-hash"}
	if err := gdb.Create(account).Error; err != nil {
		t.Fatalf("创建用户 %q 失败: %v", username, err)
	}
	sessions := authn.NewSessionService(authn.NewSessionRepository(gdb))
	pair, err := sessions.Create(context.Background(), account.ID, account.Username)
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	return account, pair.AccessToken
}

// videoInteractions 写入真实点赞与评论行以验证互动计数
func videoInteractions(t *testing.T, gdb *gorm.DB, videoID uint, likerIDs []uint, commenters []uint) {
	t.Helper()
	now := time.Now()
	for _, likerID := range likerIDs {
		if err := gdb.Exec("INSERT INTO video_likes (video_id, user_id, created_at) VALUES (?, ?, ?)",
			videoID, likerID, now).Error; err != nil {
			t.Fatalf("写入点赞失败: %v", err)
		}
	}
	for index, authorID := range commenters {
		if err := gdb.Exec("INSERT INTO video_comments (video_id, author_id, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			videoID, authorID, fmt.Sprintf("评论 %d", index+1), now, now).Error; err != nil {
			t.Fatalf("写入评论失败: %v", err)
		}
	}
}

// 测试目标：验证公开列表与详情的可见性、排序与互动计数语义
// 预期效果：仅完整已发布作品可见，同发布时间按标识倒序，计数随真实互动增长
func TestVideoPublicVisibilityHTTPContract(t *testing.T) {
	engine, gdb, repo, _ := newVideoHTTPEngine(t)
	author, token := newVideoAuthor(t, gdb, "list-author")
	other, otherToken := newVideoAuthor(t, gdb, "list-other")

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)
	first := seedVideo(t, repo, author.ID, "第一条", VideoStatusPublished, base)
	second := seedVideo(t, repo, author.ID, "第二条", VideoStatusPublished, base)
	newest := seedVideo(t, repo, author.ID, "最新一条", VideoStatusPublished, base.Add(time.Hour))
	draft := seedVideo(t, repo, author.ID, "未发布草稿", VideoStatusDraft, base)
	foreign := seedVideo(t, repo, other.ID, "他人作品", VideoStatusPublished, base.Add(2*time.Hour))
	removed := seedVideo(t, repo, author.ID, "已删除作品", VideoStatusPublished, base.Add(3*time.Hour))
	setVideoDeletedAt(t, gdb, removed.ID, time.Now())
	incomplete := newVideoFixture(author.ID, "媒体不完整作品", VideoStatusPublished, base.Add(4*time.Hour))
	incomplete.CoverFileName = ""
	if err := repo.Create(context.Background(), incomplete); err != nil {
		t.Fatalf("写入不完整作品失败: %v", err)
	}

	public := videoJSONRequest(t, engine, http.MethodGet, "/api/video", "", nil)
	if public.Code != http.StatusOK {
		t.Fatalf("公开列表状态错误 got=%d body=%s", public.Code, public.Body.String())
	}
	assertVideoIDs(t, videoListIDs(t, public), []uint{foreign.ID, newest.ID, second.ID, first.ID})

	byAuthor := videoJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/video?author_id=%d", author.ID), "", nil)
	assertVideoIDs(t, videoListIDs(t, byAuthor), []uint{newest.ID, second.ID, first.ID})

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video?author_id=abc", "", nil),
		http.StatusBadRequest); message != ErrInvalidAuthorID.Error() {
		t.Fatalf("非法作者参数文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video?limit=99", "", nil),
		http.StatusBadRequest); message != ErrInvalidLimit.Error() {
		t.Fatalf("非法分页文案错误 got=%q", message)
	}

	detail := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", first.ID), "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("公开详情状态错误 got=%d body=%s", detail.Code, detail.Body.String())
	}
	item := videoResponseBody(t, detail)["video"].(map[string]any)
	detailAuthor := item["author"].(map[string]any)
	if detailAuthor["username"] != "list-author" || detailAuthor["id"].(float64) != float64(author.ID) {
		t.Fatalf("公开详情作者错误 got=%v", detailAuthor)
	}
	if item["likes_count"].(float64) != 0 || item["comments_count"].(float64) != 0 {
		t.Fatalf("初始互动计数错误 got=%v", item)
	}

	videoInteractions(t, gdb, first.ID, []uint{other.ID, author.ID}, []uint{other.ID})
	liked := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", first.ID), "", nil)
	likedItem := videoResponseBody(t, liked)["video"].(map[string]any)
	if likedItem["likes_count"].(float64) != 2 || likedItem["comments_count"].(float64) != 1 {
		t.Fatalf("互动计数错误 got=%v", likedItem)
	}

	for name, id := range map[string]uint{
		"草稿":    draft.ID,
		"已删除":   removed.ID,
		"媒体不完整": incomplete.ID,
	} {
		message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", id), "", nil),
			http.StatusNotFound)
		if message != "video not found" {
			t.Fatalf("%s作品详情文案错误 got=%q", name, message)
		}
	}

	mine := videoJSONRequest(t, engine, http.MethodGet, "/api/video/auth/mine", token, nil)
	if mine.Code != http.StatusOK {
		t.Fatalf("我的视频状态错误 got=%d body=%s", mine.Code, mine.Body.String())
	}
	assertVideoIDs(t, videoListIDs(t, mine), []uint{newest.ID, second.ID, first.ID})

	foreignMine := videoJSONRequest(t, engine, http.MethodGet, "/api/video/auth/mine", otherToken, nil)
	assertVideoIDs(t, videoListIDs(t, foreignMine), []uint{foreign.ID})
}

// 测试目标：验证视频删除的权限与可见性契约
// 预期效果：非作者删除 403、作者删除 204 后详情 404 且公开列表不再包含
func TestVideoDeleteHTTPContract(t *testing.T) {
	engine, gdb, repo, _ := newVideoHTTPEngine(t)
	author, token := newVideoAuthor(t, gdb, "delete-author")
	_, otherToken := newVideoAuthor(t, gdb, "delete-other")
	video := seedVideo(t, repo, author.ID, "待删除作品", VideoStatusPublished, time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local))
	path := fmt.Sprintf("/api/video/auth/%d", video.ID)

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, otherToken, nil),
		http.StatusForbidden); message != ErrNotAuthor.Error() {
		t.Fatalf("非作者删除文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, "", nil),
		http.StatusUnauthorized); message != "missing authorization header" {
		t.Fatalf("未登录删除文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, "not-a-token", nil),
		http.StatusUnauthorized); message != "invalid or expired token" {
		t.Fatalf("非法令牌删除文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, "/api/video/auth/abc", token, nil),
		http.StatusBadRequest); message != ErrInvalidVideoID.Error() {
		t.Fatalf("非法标识删除文案错误 got=%q", message)
	}

	deleted := videoJSONRequest(t, engine, http.MethodDelete, path, token, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("删除状态错误 got=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if deleted.Body.Len() != 0 {
		t.Fatalf("删除响应体应为空 got=%s", deleted.Body.String())
	}

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", video.ID), "", nil),
		http.StatusNotFound); message != "video not found" {
		t.Fatalf("删除后详情文案错误 got=%q", message)
	}
	assertVideoIDs(t, videoListIDs(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video", "", nil)), []uint{})
	assertVideoIDs(t, videoListIDs(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video/auth/mine", token, nil)), []uint{})

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, token, nil),
		http.StatusNotFound); message != "video not found" {
		t.Fatalf("重复删除文案错误 got=%q", message)
	}
}

// 测试目标：写入一条指定状态的事件
// 预期效果：租约用例可基于该事件构造 claim 场景
func seedOutboxEvent(t *testing.T, db *gorm.DB, videoID uint, eventID, status string) OutboxEvent {
	t.Helper()
	event := OutboxEvent{
		EventID:   eventID,
		VideoID:   videoID,
		EventType: VideoProcessEventType,
		Status:    status,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("创建 outbox 事件失败: %v", err)
	}
	return event
}

// 测试目标：写入一条 processing 视频
// 预期效果：满足 outbox 外键与视频快照读取需求
func seedProcessingVideoRow(t *testing.T, repo *Repository, authorID uint, title string) *Video {
	t.Helper()
	publishedAt := baseTime
	row := newVideoFixture(authorID, title, VideoStatusProcessing, publishedAt)
	row.PublishedAt = &publishedAt
	if err := repo.Create(context.Background(), row); err != nil {
		t.Fatalf("创建处理视频失败: %v", err)
	}
	return row
}

// 测试目标：验证过期 publishing 事件可被接管，且旧持有者的围栏令牌失效
// 预期效果：接管后 attempt 递增，旧令牌的标记与释放均不生效，新持有者可完成标记
func TestClaimPendingOutboxEventsTakesOverExpiredLease(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "接管视频")
	event := seedOutboxEvent(t, db, row.ID, "evt-lease-expired", OutboxEventStatusPublishing)

	// 测试目标：构造一个已过期的租约模拟持有者崩溃
	// 预期效果：该事件可被后续 claim 接管
	if err := db.Model(&OutboxEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
		"locked_until": gorm.Expr("TIMESTAMPADD(SECOND, -60, NOW(3))"),
		"attempt":      1,
	}).Error; err != nil {
		t.Fatalf("构造过期租约失败: %v", err)
	}

	// 测试目标：验证租约过期且尚未接管时旧持有者失效
	// 预期效果：标记与释放都不产生变更
	marked, err := repo.MarkOutboxDispatched(context.Background(), event.ID, 1)
	if err != nil {
		t.Fatalf("过期租约标记失败: %v", err)
	}
	if marked {
		t.Fatal("租约过期后原持有者不应能标记事件")
	}
	released, err := repo.ReleaseOutboxRetry(context.Background(), event.ID, 1, time.Second, errors.New("stale"))
	if err != nil {
		t.Fatalf("过期租约释放失败: %v", err)
	}
	if released {
		t.Fatal("租约过期后原持有者不应能释放事件")
	}

	// 测试目标：验证接管后新持有者拿到递增的围栏令牌
	// 预期效果：旧令牌无法覆盖状态，新令牌可完成标记
	taken, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil {
		t.Fatalf("接管失败: %v", err)
	}
	if len(taken) != 1 || taken[0].Event.Attempt != 2 {
		t.Fatalf("过期租约应被接管并递增 attempt got=%+v", taken)
	}
	staleMark, err := repo.MarkOutboxDispatched(context.Background(), event.ID, 1)
	if err != nil {
		t.Fatalf("旧令牌标记失败: %v", err)
	}
	if staleMark {
		t.Fatal("旧围栏令牌不应能标记已接管的事件")
	}
	marked, err = repo.MarkOutboxDispatched(context.Background(), taken[0].Event.ID, taken[0].Event.Attempt)
	if err != nil || !marked {
		t.Fatalf("接管者应能标记事件 marked=%v err=%v", marked, err)
	}
}

// 以下用例验收「视频处理完成时同事务写入 video.published 发布事件」这一能力
// 覆盖正常流转、CAS 零行、拒绝分支、并发竞争、插入失败回滚、提交失败回滚与开关关闭七类场景

// 测试目标：验证并发完成同一视频时只有一个调用赢得状态流转并写入发布事件
// 预期效果：八个并发调用恰好一个返回变更且都不报错 视频最终为 published 发布事件恰好一条
func TestPublishedEventSingleWinnerUnderConcurrentCompletion(t *testing.T) {
	db := testutil.DB(t)
	row := seedProcessingVideoRow(t, NewRepository(db, WithPublishedEvents(true)), 1, "并发完成视频")

	const workers = 8
	type completionResult struct {
		changed bool
		err     error
	}

	start := make(chan struct{})
	results := make(chan completionResult, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repo := NewRepository(db, WithPublishedEvents(true))
			<-start
			changed, err := repo.CompleteVideoProcessing(context.Background(), row.ID)
			results <- completionResult{changed: changed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	winners := 0
	for got := range results {
		if got.err != nil {
			t.Fatalf("并发完成处理不应返回错误 got=%v", got.err)
		}
		if got.changed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("应恰好一个调用赢得状态流转 got=%d", winners)
	}
	if got := readPublishedEventVideo(t, db, row.ID); got.Status != VideoStatusPublished {
		t.Fatalf("视频状态应为 published got=%s", got.Status)
	}
	if events := listPublishedEvents(t, db, row.ID); len(events) != 1 {
		t.Fatalf("发布事件应恰好一条 got=%d", len(events))
	}
}

// 测试目标：验证发布事件插入失败时状态流转随事务一起回滚
// 预期效果：返回注入的插入错误 视频仍为 processing published_at 不变 outbox 无任何事件
func TestPublishedEventRolledBackWhenOutboxInsertFails(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db, WithPublishedEvents(true))
	row := seedProcessingVideoRow(t, repo, 1, "事件插入失败视频")
	before := readPublishedEventVideo(t, db, row.ID)

	fault := registerPublishedEventCreateFault(t, db)
	injected := errors.New("injected outbox insert failure")
	fault.arm("video_outbox_events", injected)

	changed, err := repo.CompleteVideoProcessing(t.Context(), row.ID)
	fault.disarm()
	if !errors.Is(err, injected) {
		t.Fatalf("应返回注入的插入错误 changed=%v err=%v", changed, err)
	}
	if changed {
		t.Fatal("插入失败回滚后不应报告状态变更")
	}
	if !fault.hit() {
		t.Fatal("注入点未被触发 用例未覆盖真实插入失败路径")
	}

	after := readPublishedEventVideo(t, db, row.ID)
	if after.Status != VideoStatusProcessing {
		t.Fatalf("插入失败应回滚状态 got=%s", after.Status)
	}
	if after.PublishedAt == nil || before.PublishedAt == nil {
		t.Fatal("published_at 不应为空")
	}
	if !before.PublishedAt.Equal(*after.PublishedAt) {
		t.Fatalf("插入失败后 published_at 不应改变 before=%v after=%v", *before.PublishedAt, *after.PublishedAt)
	}
	if total := countAllOutboxEvents(t, db); total != 0 {
		t.Fatalf("插入失败回滚后不应留下事件 got=%d", total)
	}

	// 移除注入后同一视频仍可正常完成 说明失败并非前置状态被破坏
	fault.disarm()
	if changed, err := repo.CompleteVideoProcessing(t.Context(), row.ID); err != nil || !changed {
		t.Fatalf("解除注入后应能正常完成 changed=%v err=%v", changed, err)
	}
	if events := listPublishedEvents(t, db, row.ID); len(events) != 1 {
		t.Fatalf("解除注入后应写入一条发布事件 got=%d", len(events))
	}
}

// 测试目标：验证事务提交失败时状态流转与发布事件一起回滚
// 预期效果：返回注入的提交错误 视频仍为 processing published_at 不变 outbox 无任何事件
func TestPublishedEventRolledBackWhenCommitFails(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db, WithPublishedEvents(true))
	row := seedProcessingVideoRow(t, repo, 1, "提交失败视频")
	before := readPublishedEventVideo(t, db, row.ID)

	faultRepo, fault := newPublishedEventCommitFaultRepository(t, db)
	injected := errors.New("injected commit failure")
	fault.arm(injected)

	changed, err := faultRepo.CompleteVideoProcessing(t.Context(), row.ID)
	if !errors.Is(err, injected) {
		t.Fatalf("应返回注入的提交错误 changed=%v err=%v", changed, err)
	}
	if changed {
		t.Fatal("提交失败回滚后不应报告状态变更")
	}
	if begins, commits := fault.counters(); begins != 1 || commits != 1 {
		t.Fatalf("注错点应恰好开启并提交一次事务 begins=%d commits=%d", begins, commits)
	}
	fault.disarm()

	after := readPublishedEventVideo(t, db, row.ID)
	if after.Status != VideoStatusProcessing {
		t.Fatalf("提交失败应回滚状态 got=%s", after.Status)
	}
	if after.PublishedAt == nil || before.PublishedAt == nil {
		t.Fatal("published_at 不应为空")
	}
	if !before.PublishedAt.Equal(*after.PublishedAt) {
		t.Fatalf("提交失败后 published_at 不应改变 before=%v after=%v", *before.PublishedAt, *after.PublishedAt)
	}
	if total := countAllOutboxEvents(t, db); total != 0 {
		t.Fatalf("提交失败回滚后不应留下事件 got=%d", total)
	}

	// 同一连接池在解除注入后仍可正常提交 说明回滚确实落到底层事务
	faultRepo2, _ := newPublishedEventCommitFaultRepository(t, db)
	if changed, err := faultRepo2.CompleteVideoProcessing(t.Context(), row.ID); err != nil || !changed {
		t.Fatalf("解除注入后应能正常完成 changed=%v err=%v", changed, err)
	}
	if events := listPublishedEvents(t, db, row.ID); len(events) != 1 {
		t.Fatalf("解除注入后应写入一条发布事件 got=%d", len(events))
	}
}

// readPublishedEventVideo 读取视频当前落库状态
// 测试目标：为用例提供真实数据库中的状态快照
// 预期效果：返回该视频行的状态与发布时间
func readPublishedEventVideo(t *testing.T, db *gorm.DB, videoID uint) Video {
	t.Helper()
	var row Video
	if err := db.First(&row, "id = ?", videoID).Error; err != nil {
		t.Fatalf("读取视频 %d 失败: %v", videoID, err)
	}
	return row
}

// listPublishedEvents 列出指定视频的发布事件
// 测试目标：只统计 event_type 为 video.published 的事件
// 预期效果：按创建顺序返回该视频的发布事件
func listPublishedEvents(t *testing.T, db *gorm.DB, videoID uint) []OutboxEvent {
	t.Helper()
	var events []OutboxEvent
	if err := db.Where("video_id = ? AND event_type = ?", videoID, VideoPublishedEventType).
		Order("id ASC").Find(&events).Error; err != nil {
		t.Fatalf("读取视频 %d 的发布事件失败: %v", videoID, err)
	}
	return events
}

// countAllOutboxEvents 统计 outbox 全表事件数
// 测试目标：判断是否有多余事件残留
// 预期效果：返回 video_outbox_events 的当前行数
func countAllOutboxEvents(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var total int64
	if err := db.Model(&OutboxEvent{}).Count(&total).Error; err != nil {
		t.Fatalf("统计 outbox 事件失败: %v", err)
	}
	return total
}

// publishedEventCreateFault 是写入发布事件时的插入故障注入器
// 测试目标：让针对目标表的 Create 语句在回调阶段直接失败
// 预期效果：武装后命中目标表的插入返回注入错误 其余语句不受影响
type publishedEventCreateFault struct {
	mu    sync.Mutex
	table string
	err   error
	hits  int
}

// inject 是注册到 Create 回调链上的处理函数
// 测试目标：在 INSERT 执行前按表名短路语句
// 预期效果：命中目标表时把注入错误写入语句错误并累计命中次数
func (f *publishedEventCreateFault) inject(tx *gorm.DB) {
	f.mu.Lock()
	table, err := f.table, f.err
	if err != nil && tx.Statement != nil && tx.Statement.Table == table {
		f.hits++
	}
	f.mu.Unlock()
	if err == nil || tx.Statement == nil || tx.Statement.Table != table {
		return
	}
	tx.AddError(err)
}

// arm 武装故障
// 测试目标：让下一次命中目标表的插入失败
// 预期效果：目标表匹配且错误非空
func (f *publishedEventCreateFault) arm(table string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.table = table
	f.err = err
}

// disarm 解除故障
// 测试目标：恢复目标表的正常写入
// 预期效果：后续插入不再注入错误
func (f *publishedEventCreateFault) disarm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = nil
}

// hit 报告注入点是否被触发
// 测试目标：确认用例确实走到了目标语句
// 预期效果：命中过至少一次返回 true
func (f *publishedEventCreateFault) hit() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits > 0
}

const publishedEventCreateFaultCallback = "gofeed:published_event_create_fault"

// registerPublishedEventCreateFault 在 Create 流程上注册插入故障注入
// 测试目标：为用例提供可武装的插入失败能力
// 预期效果：回调在 gorm:create 之前执行 测试结束后自动移除
func registerPublishedEventCreateFault(t *testing.T, db *gorm.DB) *publishedEventCreateFault {
	t.Helper()
	fault := &publishedEventCreateFault{}
	if err := db.Callback().Create().Before("gorm:create").
		Register(publishedEventCreateFaultCallback, fault.inject); err != nil {
		t.Fatalf("注册插入故障回调失败: %v", err)
	}
	t.Cleanup(func() {
		fault.disarm()
		if err := db.Callback().Create().Remove(publishedEventCreateFaultCallback); err != nil {
			t.Errorf("移除插入故障回调失败: %v", err)
		}
	})
	return fault
}

// publishedEventCommitFault 是事务提交故障注入器
// 测试目标：在连接池层拦截事务开启并在提交时注入错误
// 预期效果：武装后真实事务被回滚且返回注入错误 未武装时提交照常
type publishedEventCommitFault struct {
	mu       sync.Mutex
	pool     *sql.DB
	injected error
	begins   int
	commits  int
}

// PrepareContext 委托底层连接池
// 测试目标：保持非事务语句可用
// 预期效果：直接使用底层预处理语句
func (f *publishedEventCommitFault) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return f.pool.PrepareContext(ctx, query)
}

// ExecContext 委托底层连接池
// 测试目标：保持非事务语句可用
// 预期效果：直接执行底层语句
func (f *publishedEventCommitFault) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return f.pool.ExecContext(ctx, query, args...)
}

// QueryContext 委托底层连接池
// 测试目标：保持非事务查询可用
// 预期效果：返回底层查询结果
func (f *publishedEventCommitFault) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return f.pool.QueryContext(ctx, query, args...)
}

// QueryRowContext 委托底层连接池
// 测试目标：保持非事务单行查询可用
// 预期效果：返回底层单行结果
func (f *publishedEventCommitFault) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return f.pool.QueryRowContext(ctx, query, args...)
}

// BeginTx 开启真实事务并包装提交
// 测试目标：让事务提交点可注入错误
// 预期效果：返回包装后的连接池并累计开启次数
func (f *publishedEventCommitFault) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	real, err := f.pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.begins++
	f.mu.Unlock()
	return &publishedEventCommitFaultTx{Tx: real, fault: f}, nil
}

// arm 武装提交故障
// 测试目标：让下一次事务提交失败
// 预期效果：提交时先回滚再返回注入错误
func (f *publishedEventCommitFault) arm(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.injected = err
}

// disarm 解除提交故障
// 测试目标：恢复事务正常提交
// 预期效果：后续提交直接落地
func (f *publishedEventCommitFault) disarm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.injected = nil
}

// counters 报告事务开启与提交次数
// 测试目标：确认注错点确实被走到
// 预期效果：返回开启次数与提交次数
func (f *publishedEventCommitFault) counters() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.begins, f.commits
}

// publishedEventCommitFaultTx 包装真实事务以便注入提交错误
// 测试目标：在事务提交点注入错误并回滚
// 预期效果：武装时回滚真实事务并返回注入错误 未武装时正常提交
type publishedEventCommitFaultTx struct {
	*sql.Tx
	fault *publishedEventCommitFault
}

// Commit 按武装状态提交或注入失败
// 测试目标：模拟提交阶段失败
// 预期效果：武装时先回滚真实事务再返回注入错误 否则提交真实事务
func (tx *publishedEventCommitFaultTx) Commit() error {
	tx.fault.mu.Lock()
	injected := tx.fault.injected
	tx.fault.commits++
	tx.fault.mu.Unlock()
	if injected != nil {
		_ = tx.Tx.Rollback()
		return injected
	}
	return tx.Tx.Commit()
}

// Rollback 回滚真实事务
// 测试目标：保留事务回滚能力
// 预期效果：返回底层回滚结果
func (tx *publishedEventCommitFaultTx) Rollback() error {
	return tx.Tx.Rollback()
}

// newPublishedEventCommitFaultRepository 构造提交点可注入错误的仓储
// 测试目标：让 CompleteVideoProcessing 走真实事务并在提交处失败
// 预期效果：返回使用真实连接池的仓储与对应故障注入器
func newPublishedEventCommitFaultRepository(t *testing.T, db *gorm.DB) (*Repository, *publishedEventCommitFault) {
	t.Helper()
	pool, err := db.DB()
	if err != nil {
		t.Fatalf("读取底层连接池失败: %v", err)
	}
	fault := &publishedEventCommitFault{pool: pool}
	session := db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	session.Statement.ConnPool = fault
	return NewRepository(session, WithPublishedEvents(true)), fault
}

func requireObjectName(t *testing.T, got, stem, ext string) {
	t.Helper()
	prefix := stem + "_"
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, ext) {
		t.Fatalf("对象文件名格式错误 got=%q want prefix=%q suffix=%q", got, prefix, ext)
	}
	objectID := strings.TrimSuffix(strings.TrimPrefix(got, prefix), ext)
	if len(objectID) != storageObjectIDBytes*2 {
		t.Fatalf("对象键长度错误 got=%q", objectID)
	}
	for _, r := range objectID {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("对象键应为小写十六进制 got=%q", objectID)
		}
	}
	if sanitizeFilename(got) != got {
		t.Fatalf("对象文件名不再符合清洗规则 got=%q", got)
	}
}

// 测试目标：验证本地存储阻止文件名中的路径穿越
// 预期效果：仅保留最后一段文件名且文件始终落在上传目录内
func TestLocalStorageSaveSanitizesPathTraversal(t *testing.T) {
	// 1 上传文件名携带目录前缀
	// 2 保存时必须只保留最后一段文件名，且落盘位置始终在上传目录内
	root := t.TempDir()
	s := NewLocalStorage(root)
	content := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

	saved, err := s.Save(context.Background(), 7, MediaVideo, "../../etc/passwd.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("保存失败 error=%v", err)
	}
	publicURL := saved.PublicURL
	if !strings.HasSuffix(publicURL, "/"+saved.FileName) {
		t.Fatalf("应只保留最后一段文件名 got=%q", publicURL)
	}
	requireObjectName(t, saved.FileName, "passwd", ".mp4")

	rel := strings.TrimPrefix(publicURL, "/static/")
	savedPath := filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(savedPath, root+string(os.PathSeparator)) {
		t.Fatalf("文件逃出上传目录 %q", savedPath)
	}
	if _, err := os.ReadFile(savedPath); err != nil {
		t.Fatalf("文件未落盘 %s error=%v", savedPath, err)
	}
}

// 测试目标：验证清扫任务不能借由异常 URL 删除上传目录外的文件
// 预期效果：非规范媒体路径被拒绝，根目录外文件保持不变
func TestLocalStorageRemoveRejectsUnsafePath(t *testing.T) {
	root := t.TempDir()
	s := NewLocalStorage(root)
	outside := filepath.Join(filepath.Dir(root), "outside.mp4")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatalf("创建根目录外文件失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, rawURL := range []string{
		"/static/videos/1/20260818/../../outside.mp4",
		"/static/videos/1/not-a-date/clip.mp4",
		"https://example.com/static/videos/1/20260818/clip.mp4",
		"/static/videos/1/20260818/clip.mp4?download=1",
	} {
		if err := s.Remove(context.Background(), rawURL); !errors.Is(err, ErrInvalidMediaPath) {
			t.Fatalf("不安全路径应被拒绝 url=%q err=%v", rawURL, err)
		}
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("根目录外文件不应被删除 data=%q err=%v", data, err)
	}
}
