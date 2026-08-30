package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gofeed/internal/db"
	"gofeed/internal/testutil"

	"github.com/gin-gonic/gin"
)

// queryCapture 按请求顺序记录请求内数据库查询次数，供预算断言读取
type queryCapture struct {
	mu     sync.Mutex
	counts []int64
}

// 测试目标：在请求结束后读取请求上下文中的查询计数
// 预期效果：计数与请求一一对应，读侧可安全并发
func (q *queryCapture) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		q.mu.Lock()
		defer q.mu.Unlock()
		q.counts = append(q.counts, db.QueryCount(c.Request.Context()))
	}
}

// 测试目标：清空已记录的计数序列
// 预期效果：预算断言只覆盖明确测量的目标请求
func (q *queryCapture) reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.counts = nil
}

// 测试目标：返回当前记录的计数副本
// 预期效果：断言使用稳定快照，不受后续请求影响
func (q *queryCapture) snapshot() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]int64(nil), q.counts...)
}

// 测试目标：装配启用查询计数回调并注入计数探针的完整路由服务
// 预期效果：预算断言可以读取每个请求在真实 MySQL 上的语句数量
func newCountingTestServer(t *testing.T) (*httptest.Server, *http.Client, *queryCapture) {
	t.Helper()
	gdb := testutil.DB(t)
	if err := db.RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("注册查询计数回调失败: %v", err)
	}
	capture := &queryCapture{}
	engine := New(gdb, false, Options{UploadDir: t.TempDir(), Middlewares: []gin.HandlerFunc{capture.middleware()}})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client(), capture
}

// 测试目标：断言单个请求的查询次数落在预算范围内
// 预期效果：次数不超过预算且至少发生一次语句，计数序列只新增一项
func assertQueryBudget(t *testing.T, capture *queryCapture, before int, budget int64) int64 {
	t.Helper()
	counts := capture.snapshot()
	if len(counts) != before+1 {
		t.Fatalf("应只新增一次请求记录 got=%d want=%d", len(counts), before+1)
	}
	got := counts[len(counts)-1]
	if got < 1 || got > budget {
		t.Fatalf("查询预算超限 got=%d want 1..%d", got, budget)
	}
	return got
}

// 测试目标：验证公开 Feed 首页的数据库查询收敛在预算内
// 预期效果：列表请求最多执行视频、作者、点赞、评论各一次共四条语句
func TestPublicListQueryBudget(t *testing.T) {
	srv, client, capture := newCountingTestServer(t)
	base := srv.URL
	register(t, client, base, "budget-author", "budget-author-password")
	session := login(t, client, base, "budget-author", "budget-author-password")
	publishCompleteVideo(t, client, base, session.AccessToken, "预算列表视频")

	capture.reset()
	before := len(capture.snapshot())
	var list struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusOK, &list)
	if len(list.Items) == 0 {
		t.Fatal("预算用例应至少返回一条已发布视频")
	}
	assertQueryBudget(t, capture, before, 4)
}

// 测试目标：验证公开视频详情的数据库查询收敛在预算内
// 预期效果：详情请求最多执行视频、作者与两类聚合共四条语句
func TestPublicDetailQueryBudget(t *testing.T) {
	srv, client, capture := newCountingTestServer(t)
	base := srv.URL
	register(t, client, base, "budget-detail-author", "budget-detail-password")
	session := login(t, client, base, "budget-detail-author", "budget-detail-password")
	video := publishCompleteVideo(t, client, base, session.AccessToken, "预算详情视频")

	capture.reset()
	before := len(capture.snapshot())
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, video.ID), "", nil, http.StatusOK, &videoItem{})
	assertQueryBudget(t, capture, before, 4)
}
