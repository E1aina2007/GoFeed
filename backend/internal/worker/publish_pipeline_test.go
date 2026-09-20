package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gofeed/internal/mq"
	"gofeed/internal/router"
	"gofeed/internal/sweeper"
	"gofeed/internal/testutil"
	"gofeed/internal/video"

	"gorm.io/gorm"
)

const (
	// pipelinePollInterval 是发布闭环用例轮询服务端状态的间隔
	pipelinePollInterval = 50 * time.Millisecond
	// pipelinePollTimeout 是等待 processing 进入终态的上限
	// 覆盖真实 broker 投递、独立 worker 进程处理与状态落库的完整往返，
	// 需容纳与其它集成测试并行时被拉长的机器负载
	pipelinePollTimeout = 90 * time.Second
	// pipelineStartupTimeout 是等待独立 worker 进程订阅临时队列的上限
	pipelineStartupTimeout = 60 * time.Second
	// pipelineConsumerStartedMarker 是子进程订阅成功后写入输出的就绪标记
	pipelineConsumerStartedMarker = "[consumer] 已启动"
)

// 测试目标：提供端到端媒体上传所需的最小文件头
// 预期效果：视频和封面上传可通过服务端类型校验
var (
	pipelineMP4Bytes = []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	pipelinePNGBytes = []byte{0x89, 'P', 'N', 'G'}
)

// 测试目标：收集独立 worker 进程的标准输出并支持并发读取
// 预期效果：父进程可在子进程运行期间安全轮询订阅就绪标记
type pipelineProcessOutput struct {
	mu     sync.Mutex
	buffer strings.Builder
}

func (o *pipelineProcessOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.Write(p)
}

func (o *pipelineProcessOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

// 测试目标：初始化发布闭环用例的完整 HTTP 服务与共享媒体根目录
// 预期效果：独立进程 worker 与 API 使用同一隔离数据库和存储根
func newPipelineServer(t *testing.T) (*httptest.Server, *http.Client, *gorm.DB, string) {
	t.Helper()
	gdb := testutil.DB(t)
	storageRoot := t.TempDir()
	engine := router.New(gdb, false, router.Options{UploadDir: storageRoot})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client(), gdb, storageRoot
}

// 测试目标：启动运行完整 relay 与 consumer 的独立 worker 进程
// 预期效果：进程只消费随机测试拓扑，结束后被父进程取消并等待退出
func startPipelineWorkerProcess(t *testing.T, gdb *gorm.DB, storageRoot string, spec mq.ConsumerSpec) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd, _ := newWorkerProcessCommand(ctx, workerProcessPipelineMode,
		workerProcessDatabaseName(t, gdb), storageRoot, "", 0, spec, workerProcessConfigPath(t))
	// 测试目标：用可并发读取的输出替换命令默认缓冲
	// 预期效果：父进程可在子进程运行期间安全轮询订阅就绪标记
	output := &pipelineProcessOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("启动 worker 闭环子进程失败: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		if cmd.ProcessState == nil {
			if err := cmd.Wait(); err != nil && ctx.Err() == nil {
				t.Errorf("worker 闭环子进程异常退出: %v output=%s", err, output.String())
			}
		}
	})
	waitForPipelineConsumer(t, output, spec.Queue)
}

// 测试目标：等待独立 worker 进程完成队列订阅
// 预期效果：以子进程输出中的订阅日志为准，避免用固定等待窗口猜测启动耗时
func waitForPipelineConsumer(t *testing.T, output *pipelineProcessOutput, queue string) {
	t.Helper()
	deadline := time.Now().Add(pipelineStartupTimeout)
	want := pipelineConsumerStartedMarker + " queue=" + queue
	for {
		if strings.Contains(output.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 worker 订阅临时队列超时 queue=%s want=%q output=%s",
				queue, want, output.String())
		}
		time.Sleep(pipelinePollInterval)
	}
}

// 测试目标：通过真实 API 注册并登录发布用例作者
// 预期效果：返回可复用访问令牌的会话
func pipelineSession(t *testing.T, client *http.Client, base, username string) string {
	t.Helper()
	password := "pipeline-password-123"
	doPipelineJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": username,
		"password": password,
	}, http.StatusCreated, nil)
	var out struct {
		AccessToken string `json:"access_token"`
	}
	doPipelineJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": username,
		"password": password,
	}, http.StatusOK, &out)
	if out.AccessToken == "" {
		t.Fatal("登录响应缺少访问令牌")
	}
	return out.AccessToken
}

// 测试目标：发送结构化请求并校验状态码
// 预期效果：按需解码成功响应，失败时给出响应体
func doPipelineJSON(t *testing.T, client *http.Client, method, url, token string, body any, wantStatus int, out any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求失败: %v", err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		buf := &bytes.Buffer{}
		_, _ = buf.ReadFrom(resp.Body)
		t.Fatalf("%s %s status got=%d want=%d body=%s", method, url, resp.StatusCode, wantStatus, buf.String())
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s 解析响应失败: %v", method, url, err)
		}
	}
}

// 测试目标：描述发布用例需要的草稿与上传结果
// 预期效果：后续断言直接使用服务端返回的标识与媒体地址
type pipelineDraft struct {
	draftID   uint
	playURL   string
	coverURL  string
	videoID   uint
	playPath  string
	coverPath string
}

// 测试目标：经真实 API 创建草稿并上传视频与封面
// 预期效果：返回共享存储根下的媒体绝对路径，供清扫与缺陷用例复用
func createPipelineDraft(t *testing.T, client *http.Client, base, token, storageRoot, stem string) pipelineDraft {
	t.Helper()
	var created struct {
		Draft struct {
			ID uint `json:"id"`
		} `json:"draft"`
	}
	doPipelineJSON(t, client, http.MethodPost, base+"/api/video/auth/drafts", token,
		map[string]string{"title": stem, "description": ""}, http.StatusCreated, &created)
	if created.Draft.ID == 0 {
		t.Fatal("创建草稿响应缺少标识")
	}
	play := uploadPipelineMedia(t, client, base, token,
		fmt.Sprintf("/api/video/auth/drafts/%d/play", created.Draft.ID), stem+".mp4", pipelineMP4Bytes)
	cover := uploadPipelineMedia(t, client, base, token,
		fmt.Sprintf("/api/video/auth/drafts/%d/cover", created.Draft.ID), stem+".png", pipelinePNGBytes)
	return pipelineDraft{
		draftID:   created.Draft.ID,
		playURL:   play,
		coverURL:  cover,
		playPath:  pipelineStoredPath(t, storageRoot, play),
		coverPath: pipelineStoredPath(t, storageRoot, cover),
	}
}

// 测试目标：上传单个媒体文件并返回服务端公开地址
// 预期效果：测试进程与独立 worker 共享同一存储根下的文件
func uploadPipelineMedia(t *testing.T, client *http.Client, base, token, path, filename string, content []byte) string {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("创建表单文件失败: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("写入表单失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+path, &buf)
	if err != nil {
		t.Fatalf("构造上传请求失败: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("上传请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body := &bytes.Buffer{}
		_, _ = body.ReadFrom(resp.Body)
		t.Fatalf("上传 %s status got=%d body=%s", path, resp.StatusCode, body.String())
	}
	var out struct {
		PlayURL  string `json:"play_url"`
		CoverURL string `json:"cover_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析上传响应失败: %v", err)
	}
	if out.PlayURL != "" {
		return out.PlayURL
	}
	if out.CoverURL != "" {
		return out.CoverURL
	}
	t.Fatalf("上传响应缺少媒体地址 path=%s", path)
	return ""
}

// 测试目标：把公开媒体地址映射为共享存储根下的绝对路径
// 预期效果：路径不存在时立即失败，避免后续断言在错误路径上通过
func pipelineStoredPath(t *testing.T, storageRoot, publicURL string) string {
	t.Helper()
	const prefix = "/static/"
	if len(publicURL) <= len(prefix) {
		t.Fatalf("媒体地址不符合公开路径契约 got=%q", publicURL)
	}
	path := filepath.Join(storageRoot, filepath.FromSlash(publicURL[len(prefix):]))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("共享存储根下缺少媒体文件 path=%s err=%v", path, err)
	}
	return path
}

// 测试目标：发布草稿并返回服务端受理结果
// 预期效果：成功受理返回 202 与处理中草稿
func publishPipelineDraft(t *testing.T, client *http.Client, base, token string, draftID uint) (uint, string) {
	t.Helper()
	var out struct {
		Draft struct {
			ID     uint   `json:"id"`
			Status string `json:"status"`
		} `json:"draft"`
	}
	doPipelineJSON(t, client, http.MethodPost,
		fmt.Sprintf("%s/api/video/auth/drafts/%d/publish", base, draftID), token, nil, http.StatusAccepted, &out)
	return out.Draft.ID, out.Draft.Status
}

// 测试目标：轮询作者状态接口直到 processing 进入终态
// 预期效果：返回顶层状态字段，超时保留最后一次状态用于诊断
func waitPipelineTerminal(t *testing.T, client *http.Client, base, token string, videoID uint) video.VideoProcessingStatus {
	t.Helper()
	deadline := time.Now().Add(pipelinePollTimeout)
	last := fetchPipelineStatus(t, client, base, token, videoID)
	for {
		if last.Status == video.VideoStatusPublished || last.Status == video.VideoStatusRejected {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待视频 %d 进入终态超时 last=%+v", videoID, last)
		}
		time.Sleep(pipelinePollInterval)
		last = fetchPipelineStatus(t, client, base, token, videoID)
	}
}

// 测试目标：读取作者视角的处理状态
// 预期效果：返回服务端顶层状态字段
func fetchPipelineStatus(t *testing.T, client *http.Client, base, token string, videoID uint) video.VideoProcessingStatus {
	t.Helper()
	var status video.VideoProcessingStatus
	doPipelineJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/video/auth/%d/status", base, videoID), token, nil, http.StatusOK, &status)
	return status
}

// 测试目标：读取指定视频的 outbox 事件
// 预期效果：事件数量异常直接暴露发布契约漂移
func pipelineOutboxEvent(t *testing.T, gdb *gorm.DB, videoID uint) video.OutboxEvent {
	t.Helper()
	var events []video.OutboxEvent
	if err := gdb.Where("video_id = ?", videoID).Find(&events).Error; err != nil {
		t.Fatalf("读取 outbox 事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("发布流程应只有一个 outbox 事件 got=%+v", events)
	}
	return events[0]
}

// 测试目标：等待 outbox 事件收口到 dispatched
// 预期效果：视频终态由消费端先落库、relay 后标记派发，因此必须显式等待派发终态；
// 超时输出事件租约字段，便于区分 relay 未运行与租约未到期
func waitPipelineOutboxDispatched(t *testing.T, gdb *gorm.DB, videoID uint) video.OutboxEvent {
	t.Helper()
	deadline := time.Now().Add(pipelinePollTimeout)
	for {
		event := pipelineOutboxEvent(t, gdb, videoID)
		if event.Status == video.OutboxEventStatusDispatched && event.DispatchedAt != nil {
			return event
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 outbox 事件派发终态超时 got=%+v", event)
		}
		time.Sleep(pipelinePollInterval)
	}
}

// 测试目标：验证媒体齐全的发布由独立 worker 进程推进到 published
// 预期效果：202 受理后状态转为 published，公开详情、列表与我的视频同时可见
func TestPublishPipelineReachesPublishedWithIsolatedWorkerProcess(t *testing.T) {
	srv, client, gdb, storageRoot := newPipelineServer(t)
	conn := newIntegrationConnection(t)
	spec := declareWorkerProcessTopology(t, conn)
	startPipelineWorkerProcess(t, gdb, storageRoot, spec)
	base := srv.URL
	token := pipelineSession(t, client, base, "pipeline_author")

	draft := createPipelineDraft(t, client, base, token, storageRoot, "pipeline")
	videoID, status := publishPipelineDraft(t, client, base, token, draft.draftID)
	if videoID == 0 || status != video.VideoStatusProcessing {
		t.Fatalf("发布响应应为处理中草稿 got id=%d status=%s", videoID, status)
	}
	// 测试目标：确认受理阶段尚未产生终态
	// 预期效果：状态为 processing，公开详情仍返回 404
	accepted := fetchPipelineStatus(t, client, base, token, videoID)
	if accepted.Status != video.VideoStatusProcessing || accepted.PublishedAt == nil || accepted.RejectedAt != nil {
		t.Fatalf("受理后状态应为 processing got=%+v", accepted)
	}
	doPipelineJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, videoID), "", nil, http.StatusNotFound, nil)

	// 测试目标：等待独立 worker 进程完成处理
	// 预期效果：状态转为 published 且不携带拒绝字段
	terminal := waitPipelineTerminal(t, client, base, token, videoID)
	if terminal.Status != video.VideoStatusPublished {
		t.Fatalf("媒体齐全的发布应转为 published got=%+v", terminal)
	}
	if terminal.RejectedAt != nil || terminal.RejectedReason != "" {
		t.Fatalf("published 不应携带拒绝字段 got=%+v", terminal)
	}

	var row video.Video
	if err := gdb.First(&row, videoID).Error; err != nil {
		t.Fatalf("读取视频行失败: %v", err)
	}
	if row.Status != video.VideoStatusPublished {
		t.Fatalf("数据库状态应为 published got=%+v", row)
	}
	// 测试目标：等待 outbox 收口到派发终态
	// 预期效果：视频终态由消费端先落库、relay 后标记派发，断言以派发终态为准
	waitPipelineOutboxDispatched(t, gdb, videoID)
	for _, path := range []string{draft.playPath, draft.coverPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("published 视频媒体不应被删除 path=%s err=%v", path, err)
		}
	}

	// 测试目标：验证静态媒体服务与公开读取使用同一存储根
	// 预期效果：上传响应给出的播放地址可直接经 /static 下载
	resp, err := client.Get(base + draft.playURL)
	if err != nil {
		t.Fatalf("请求静态媒体失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("published 媒体应可访问 got=%d url=%s", resp.StatusCode, draft.playURL)
	}

	var detail struct {
		Video struct {
			ID uint `json:"id"`
		} `json:"video"`
	}
	doPipelineJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, videoID), "", nil, http.StatusOK, &detail)
	if detail.Video.ID != videoID {
		t.Fatalf("published 视频详情应返回该视频 got=%+v", detail.Video)
	}
	var mine struct {
		Items []struct {
			ID uint `json:"id"`
		} `json:"items"`
	}
	doPipelineJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", token, nil, http.StatusOK, &mine)
	if len(mine.Items) != 1 || mine.Items[0].ID != videoID {
		t.Fatalf("published 视频应进入我的视频 got=%+v", mine.Items)
	}
}

// 测试目标：验证媒体缺失的发布由独立 worker 进程拒绝，并在保留期届满后不可逆清扫
// 预期效果：状态转为 rejected 且带原因，清扫后记录与媒体同时消失并对外不可读
func TestPublishPipelineRejectionAndDraftPurgeWithIsolatedWorkerProcess(t *testing.T) {
	srv, client, gdb, storageRoot := newPipelineServer(t)
	conn := newIntegrationConnection(t)
	spec := declareWorkerProcessTopology(t, conn)
	startPipelineWorkerProcess(t, gdb, storageRoot, spec)
	base := srv.URL
	token := pipelineSession(t, client, base, "rejected_author")

	draft := createPipelineDraft(t, client, base, token, storageRoot, "rejected")
	// 测试目标：制造共享卷下的媒体缺失
	// 预期效果：消费端只看到确定性媒体缺陷，不把基础设施故障误判为拒绝
	if err := os.Remove(draft.coverPath); err != nil {
		t.Fatalf("移除封面文件失败: %v", err)
	}
	videoID, _ := publishPipelineDraft(t, client, base, token, draft.draftID)

	status := waitPipelineTerminal(t, client, base, token, videoID)
	if status.Status != video.VideoStatusRejected {
		t.Fatalf("封面缺失的发布应转为 rejected got=%+v", status)
	}
	if status.RejectedAt == nil || status.RejectedReason == "" {
		t.Fatalf("rejected 必须携带拒绝时刻与原因 got=%+v", status)
	}

	var rejected video.Video
	if err := gdb.First(&rejected, videoID).Error; err != nil {
		t.Fatalf("读取拒绝视频失败: %v", err)
	}
	if rejected.Status != video.VideoStatusRejected || rejected.RejectedReason != status.RejectedReason {
		t.Fatalf("数据库拒绝字段应与状态接口一致 row=%+v status=%+v", rejected, status)
	}
	// 测试目标：等待 outbox 收口到派发终态
	// 预期效果：拒绝终态由消费端先落库、relay 后标记派发，断言以派发终态为准
	waitPipelineOutboxDispatched(t, gdb, videoID)

	// 测试目标：确认拒绝视频不进入任何公开读取路径
	// 预期效果：详情 404，公开列表与我的视频都不返回该记录
	doPipelineJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, videoID), "", nil, http.StatusNotFound, nil)
	var list struct {
		Items []struct {
			ID uint `json:"id"`
		} `json:"items"`
	}
	doPipelineJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Fatalf("rejected 视频不应进入公开列表 got=%+v", list.Items)
	}
	doPipelineJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", token, nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Fatalf("rejected 视频不应进入我的视频 got=%+v", list.Items)
	}

	// 测试目标：验证保留期控制与到期清扫
	// 预期效果：保留期内不清扫，回拨到保留期外后记录与媒体一并消失
	assertPipelineDraftPurge(t, gdb, storageRoot, videoID, draft.playPath, draft.coverPath)
	doPipelineJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, videoID), "", nil, http.StatusNotFound, nil)
	doPipelineJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/auth/%d/status", base, videoID), token, nil, http.StatusNotFound, nil)
}

// 测试目标：验证拒绝视频的保留期控制与到期清扫
// 预期效果：保留期内不清扫，回拨后由真实清扫任务删除记录与媒体
// 失败时输出候选与拒绝行，便于区分契约回归与外部干扰
func assertPipelineDraftPurge(t *testing.T, gdb *gorm.DB, storageRoot string, videoID uint, playPath, coverPath string) {
	t.Helper()
	const (
		retentionHours = 24
		lease          = time.Minute
	)
	repo := video.NewRepository(gdb)
	purger := sweeper.NewDraftPurgeJob(repo, video.NewLocalStorage(storageRoot), retentionHours*time.Hour, lease)

	// 测试目标：确认保留期内拒绝视频不被清扫
	// 预期效果：本轮删除数为零；非零时输出当时的候选与拒绝行
	purged, err := purger.Run(context.Background())
	if err != nil {
		t.Fatalf("保留期内清扫失败: %v", err)
	}
	if purged != 0 {
		t.Fatalf("保留期内的拒绝视频不应被清扫 got=%d %s", purged, describePurgeCandidates(t, gdb))
	}

	// 测试目标：把保留期起点回拨到保留期之外
	// 预期效果：用例不依赖应用时钟与数据库时钟完全一致
	backdated := fmt.Sprintf("NOW(3) - INTERVAL %d HOUR", retentionHours*2)
	if err := gdb.Exec("UPDATE videos SET created_at = "+backdated+", rejected_at = "+backdated+" WHERE id = ?", videoID).Error; err != nil {
		t.Fatalf("回拨拒绝时间失败: %v", err)
	}
	purged, err = purger.Run(context.Background())
	if err != nil {
		t.Fatalf("清扫拒绝视频失败: %v %s", err, describePurgeCandidates(t, gdb))
	}
	if purged != 1 {
		t.Fatalf("本轮应清扫一条拒绝视频 got=%d want=1 video_id=%d %s",
			purged, videoID, describePurgeCandidates(t, gdb))
	}

	var remaining int64
	if err := gdb.Unscoped().Model(&video.Video{}).Where("id = ?", videoID).Count(&remaining).Error; err != nil {
		t.Fatalf("统计清扫结果失败: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("清扫后不应残留视频记录 got=%d", remaining)
	}
	for _, path := range []string{playPath, coverPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("清扫后媒体文件应被删除 path=%s err=%v", path, err)
		}
	}
}

// 测试目标：汇总数据库中仍存在的视频行用于清扫失败诊断
// 预期效果：输出 id、状态与两个时间基准，便于判断多出的候选来自何处
func describePurgeCandidates(t *testing.T, gdb *gorm.DB) string {
	t.Helper()
	var rows []struct {
		ID         uint
		Status     string
		CreatedAt  string
		RejectedAt *string
		PurgeToken *string
	}
	if err := gdb.Raw(`SELECT id, status, CAST(created_at AS CHAR) AS created_at,
		CAST(rejected_at AS CHAR) AS rejected_at, purge_token
		FROM videos ORDER BY id`).Scan(&rows).Error; err != nil {
		return fmt.Sprintf("(诊断查询失败: %v)", err)
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		rejectedAt := "-"
		if row.RejectedAt != nil {
			rejectedAt = *row.RejectedAt
		}
		token := "-"
		if row.PurgeToken != nil {
			token = *row.PurgeToken
		}
		parts = append(parts, fmt.Sprintf("{id=%d status=%s created_at=%s rejected_at=%s purge_token=%s}",
			row.ID, row.Status, row.CreatedAt, rejectedAt, token))
	}
	return "rows=" + strings.Join(parts, " ")
}
