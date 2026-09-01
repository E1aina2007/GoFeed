package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"gofeed/internal/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type faultContextKey struct{}

// faultTarget 描述一次注入故障的目标表与错误值
type faultTarget struct {
	table string
	err   error
}

// faultInjection 按表名向真实 MySQL 语句注入暂态错误的测试夹具
type faultInjection struct {
	mu     sync.Mutex
	target *faultTarget
}

// 测试目标：进入请求前按当前装配的故障目标改写请求上下文
// 预期效果：命中目标表的语句被故障回调短路
func (f *faultInjection) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		f.mu.Lock()
		target := f.target
		f.mu.Unlock()
		if target != nil {
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), faultContextKey{}, target))
		}
		c.Next()
	}
}

// 测试目标：装配指定表的注入故障
// 预期效果：后续请求中该表的语句返回注入错误
func (f *faultInjection) arm(table string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.target = &faultTarget{table: table, err: err}
}

// 测试目标：解除故障注入
// 预期效果：后续请求恢复真实路径
func (f *faultInjection) disarm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.target = nil
}

// 测试目标：注册按表名短路语句执行的故障注入回调
// 预期效果：六类语句处理器均被覆盖，内置回调因语句携带错误而跳过实际 SQL
// 互动聚合经 Scan 走 Row 处理器，outbox 插入走 Create 处理器，读写都要覆盖
func registerFaultInjection(gdb *gorm.DB) error {
	inject := func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context == nil {
			return
		}
		target, ok := tx.Statement.Context.Value(faultContextKey{}).(*faultTarget)
		if !ok || target == nil || tx.Statement.Table != target.table {
			return
		}
		tx.AddError(target.err)
	}
	for _, registration := range []struct {
		register func() error
	}{
		{func() error {
			return gdb.Callback().Query().Before("gorm:query").Register("gofeed:test_fault_query", inject)
		}},
		{func() error {
			return gdb.Callback().Create().Before("gorm:create").Register("gofeed:test_fault_create", inject)
		}},
		{func() error {
			return gdb.Callback().Update().Before("gorm:update").Register("gofeed:test_fault_update", inject)
		}},
		{func() error {
			return gdb.Callback().Delete().Before("gorm:delete").Register("gofeed:test_fault_delete", inject)
		}},
		{func() error { return gdb.Callback().Raw().Before("gorm:raw").Register("gofeed:test_fault_raw", inject) }},
		{func() error { return gdb.Callback().Row().Before("gorm:row").Register("gofeed:test_fault_row", inject) }},
	} {
		if err := registration.register(); err != nil {
			return err
		}
	}
	return nil
}

// 测试目标：装配启用故障注入回调的完整路由服务
// 预期效果：异常矩阵用例可在真实 MySQL 上按表注入暂态错误
func newResilienceTestServer(t *testing.T) (*httptest.Server, *http.Client, *faultInjection, *gorm.DB) {
	t.Helper()
	gdb := testutil.DB(t)
	if err := registerFaultInjection(gdb); err != nil {
		t.Fatalf("注册故障注入回调失败: %v", err)
	}
	faults := &faultInjection{}
	engine := New(gdb, false, Options{UploadDir: t.TempDir(), Middlewares: []gin.HandlerFunc{faults.middleware()}})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client(), faults, gdb
}

// 测试目标：发送 GET 请求并返回原始响应体
// 预期效果：幂等断言可以逐字节比较两次响应
func getRawBody(t *testing.T, client *http.Client, rawURL string) []byte {
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
	return body
}

// 测试目标：验证同一发布时间下公开列表按标识倒序稳定翻页且不重不漏
// 预期效果：同刻视频先返回较大标识，旧游标翻页补齐另一条，重复请求顺序一致
func TestPublicListSameTimestampOrdering(t *testing.T) {
	srv, client, _, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "same_ts_author", "same-ts-password-123")
	sess := login(t, client, base, "same_ts_author", "same-ts-password-123")
	first := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "同刻较早")
	second := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "同刻较晚")

	// 两条视频强制共享同一精确发布时间，排序只剩标识倒序决定
	sameTime := time.Date(2026, 8, 1, 8, 0, 0, 0, time.Local)
	if err := gdb.Exec("UPDATE videos SET published_at = ? WHERE id IN (?, ?)", sameTime, first.ID, second.ID).Error; err != nil {
		t.Fatalf("对齐发布时间失败: %v", err)
	}

	var firstPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=1", "", nil, http.StatusOK, &firstPage)
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != second.ID {
		t.Fatalf("同刻视频应先返回较大标识 got=%+v want=%d", firstPage.Items, second.ID)
	}

	var secondPage struct {
		Items []videoItem `json:"items"`
	}
	nextURL := base + "/api/video?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)
	doJSON(t, client, http.MethodGet, nextURL, "", nil, http.StatusOK, &secondPage)
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID != first.ID {
		t.Fatalf("同刻翻页应补齐较小标识且不重复 got=%+v want=%d", secondPage.Items, first.ID)
	}

	replay := getRawBody(t, client, base+"/api/video?limit=1")
	if !bytes.Contains(replay, []byte(fmt.Sprintf(`"id":%d`, second.ID))) {
		t.Fatalf("重复请求应保持稳定排序 got=%s", replay)
	}
}

// 测试目标：验证翻页期间新增与软删除视频不产生重复或跳漏
// 预期效果：旧游标翻页只返回游标之后的既有记录，新视频和被删记录不混入
func TestFeedPagingDuringMutations(t *testing.T) {
	srv, client, _, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "mutation_author", "mutation-password-123")
	sess := login(t, client, base, "mutation_author", "mutation-password-123")
	oldest := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "翻页最旧")
	middle := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "翻页中间")
	newest := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "翻页最新")

	var firstPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=1", "", nil, http.StatusOK, &firstPage)
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != newest.ID {
		t.Fatalf("首屏应返回最新视频 got=%+v", firstPage.Items)
	}

	// 翻页间隙新增更新的视频，旧游标之后不应出现该记录
	during := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "翻页期间新增")
	var secondPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	nextURL := base + "/api/video?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)
	doJSON(t, client, http.MethodGet, nextURL, "", nil, http.StatusOK, &secondPage)
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID != middle.ID || secondPage.Items[0].ID == during.ID {
		t.Fatalf("旧游标翻页应返回中间记录且不含新增视频 got=%+v", secondPage.Items)
	}

	// 继续翻页前软删除中间记录，其游标之后不应再出现被删记录
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, middle.ID), sess.AccessToken, nil, http.StatusNoContent, nil)
	var thirdPage struct {
		Items []videoItem `json:"items"`
	}
	thirdURL := base + "/api/video?limit=3&cursor=" + url.QueryEscape(secondPage.NextCursor)
	doJSON(t, client, http.MethodGet, thirdURL, "", nil, http.StatusOK, &thirdPage)
	if len(thirdPage.Items) != 1 || thirdPage.Items[0].ID != oldest.ID {
		t.Fatalf("被删记录之后的翻页应只剩最旧记录 got=%+v", thirdPage.Items)
	}
}

// 测试目标：验证作者注销后公开列表与详情返回占位作者且不报错
// 预期效果：软删除作者经完整 HTTP 路径映射为已注销用户占位资料
func TestDeletedAuthorPlaceholderInPublicResponses(t *testing.T) {
	srv, client, _, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "gone_author", "gone-author-password-123")
	sess := login(t, client, base, "gone_author", "gone-author-password-123")
	video := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "注销作者的视频")

	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", sess.AccessToken, nil, http.StatusNoContent, nil)

	var list struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].Author.ID != sess.UserID || list.Items[0].Author.Username != "已注销用户" {
		t.Fatalf("列表应返回注销作者占位资料 got=%+v", list.Items)
	}

	detailBody := getRawBody(t, client, fmt.Sprintf("%s/api/video/%d", base, video.ID))
	if !bytes.Contains(detailBody, []byte("已注销用户")) {
		t.Fatalf("详情应返回注销作者占位资料 got=%s", detailBody)
	}
}

// 测试目标：验证互动统计查询失败时公开读路径整体返回服务不可用
// 预期效果：列表与详情返回 503 固定文案，不出现零计数的半组装响应
func TestEngagementFailureReturnsServiceUnavailable(t *testing.T) {
	srv, client, faults, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "stats_fault_author", "stats-fault-password-123")
	sess := login(t, client, base, "stats_fault_author", "stats-fault-password-123")
	video := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "统计故障视频")

	faults.arm("video_likes", errors.New("injected engagement outage"))
	defer faults.disarm()

	var listBody map[string]any
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusServiceUnavailable, &listBody)
	if _, hasItems := listBody["items"]; hasItems {
		t.Fatalf("统计失败不应返回半组装列表 got=%v", listBody)
	}
	message, _ := listBody["error"].(string)
	if !strings.Contains(message, "engagement stats temporarily unavailable") {
		t.Fatalf("统计失败应返回固定文案 got=%v", listBody)
	}

	var detailBody map[string]any
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, video.ID), "", nil, http.StatusServiceUnavailable, &detailBody)
	if _, hasVideo := detailBody["video"]; hasVideo {
		t.Fatalf("统计失败不应返回半组装详情 got=%v", detailBody)
	}
}

// 测试目标：验证数据库暂态失败时错误路径干净且无半组装响应
// 预期效果：视频或作者读取被注入失败时统一返回 500 固定文案
func TestInjectedDatabaseFailureYieldsCleanError(t *testing.T) {
	srv, client, faults, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "db_fault_author", "db-fault-password-123")
	sess := login(t, client, base, "db_fault_author", "db-fault-password-123")
	video := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "暂态故障视频")

	faults.arm("videos", errors.New("injected database outage"))
	var listBody map[string]any
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusInternalServerError, &listBody)
	if _, hasItems := listBody["items"]; hasItems {
		t.Fatalf("视频读取失败不应返回半组装列表 got=%v", listBody)
	}
	var detailBody map[string]any
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, video.ID), "", nil, http.StatusInternalServerError, &detailBody)
	if _, hasVideo := detailBody["video"]; hasVideo {
		t.Fatalf("视频读取失败不应返回半组装详情 got=%v", detailBody)
	}

	faults.arm("users", errors.New("injected author outage"))
	var authorFaultBody map[string]any
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusInternalServerError, &authorFaultBody)
	if _, hasItems := authorFaultBody["items"]; hasItems {
		t.Fatalf("作者读取失败不应返回半组装列表 got=%v", authorFaultBody)
	}
	faults.disarm()

	recovered := getRawBody(t, client, base+"/api/video")
	if !bytes.Contains(recovered, []byte(`"items"`)) {
		t.Fatalf("解除注入后列表应恢复正常 got=%s", recovered)
	}
}

// 测试目标：验证公开读接口对重复请求保持幂等
// 预期效果：同一列表与详情的连续两次响应逐字节一致
func TestRepeatedGetRequestsAreIdempotent(t *testing.T) {
	srv, client, _, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "idempotent_author", "idempotent-password-123")
	sess := login(t, client, base, "idempotent_author", "idempotent-password-123")
	video := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "幂等视频")

	listBody := getRawBody(t, client, base+"/api/video")
	listReplay := getRawBody(t, client, base+"/api/video")
	if !bytes.Equal(listBody, listReplay) {
		t.Fatalf("列表重复请求响应不一致 first=%s second=%s", listBody, listReplay)
	}

	detailURL := fmt.Sprintf("%s/api/video/%d", base, video.ID)
	detailBody := getRawBody(t, client, detailURL)
	detailReplay := getRawBody(t, client, detailURL)
	if !bytes.Equal(detailBody, detailReplay) {
		t.Fatalf("详情重复请求响应不一致 first=%s second=%s", detailBody, detailReplay)
	}
}
