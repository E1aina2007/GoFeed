package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type timelineCacheEvents struct {
	mu     sync.Mutex
	counts map[string]int
}

// 测试目标：采集生产 Feed 应用层的缓存事件
// 预期效果：用真实命中、回源和回填观测证明缓存参与而非比较相同响应
func (e *timelineCacheEvents) Write(data []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "event=feed_page_cache ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if result, ok := strings.CutPrefix(field, "result="); ok {
				e.counts[result]++
			}
		}
	}
	return len(data), nil
}

// 测试目标：返回缓存事件的并发安全快照
// 预期效果：浏览器完成后可断言生产服务接受了缓存页
func (e *timelineCacheEvents) snapshot() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make(map[string]int, len(e.counts))
	for key, value := range e.counts {
		result[key] = value
	}
	return result
}

// 测试目标：构造隔离浏览器进程的环境变量
// 预期效果：不向前端进程传递数据库、中间件密码和 JWT 密钥，测试地址覆盖外部同名变量
func timelineBrowserEnv(values map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "MYSQL_") || strings.HasPrefix(upper, "REDIS_") ||
			strings.HasPrefix(upper, "RABBITMQ_") || upper == "JWT_SECRET" {
			continue
		}
		if _, overridden := values[upper]; !overridden {
			env = append(env, entry)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

// 测试目标：运行仅代理到本用例 API 的 Vite 与桌面、移动浏览器
// 预期效果：拥有并清理测试服务生命周期，不复用用户开发服务器
func runTimelineBrowser(t *testing.T, apiURL string, ids []uint) map[string]string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("真实浏览器验收需要当前进程 PATH 中的 Node")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位前端测试目录")
	}
	frontend := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", "frontend"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	webURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	resultsDir := t.TempDir()
	env := timelineBrowserEnv(map[string]string{
		"GOFEED_TIMELINE_API":     apiURL,
		"GOFEED_TIMELINE_WEB":     webURL,
		"GOFEED_TIMELINE_IDS":     string(encodedIDs),
		"GOFEED_TIMELINE_RESULTS": resultsDir,
		"PW_HEADLESS":             "1",
	})
	output, err := os.Create(filepath.Join(t.TempDir(), "vite.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	vite := exec.Command(node, filepath.Join(frontend, "node_modules", "vite", "bin", "vite.js"),
		"--config", "vite.config.timeline-live.ts", "--port", fmt.Sprint(port))
	vite.Dir, vite.Env, vite.Stdout, vite.Stderr = frontend, env, output, output
	if err := vite.Start(); err != nil {
		t.Fatal(err)
	}
	t.Logf("本用例服务: API=%s Vite=%s Node PID=%d", apiURL, webURL, vite.Process.Pid)
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = vite.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = vite.Process.Kill()
			select {
			case <-done:
				t.Logf("Vite 测试进程已退出 PID=%d", vite.Process.Pid)
			case <-time.After(5 * time.Second):
				t.Error("Vite 测试进程未按时退出")
			}
		}
	})
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case <-done:
			t.Fatalf("Vite 提前退出: %v", waitErr)
		default:
		}
		response, err := client.Get(webURL)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("隔离 Vite 服务未按时就绪")
		}
		time.Sleep(50 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	playwright := exec.CommandContext(ctx, node, filepath.Join(frontend, "node_modules", "@playwright", "test", "cli.js"),
		"test", "--config=playwright.config.timeline-live.ts", "--project=chromium", "--project=Mobile Chrome", "--workers=1", "--reporter=line")
	playwright.Dir, playwright.Env = frontend, env
	result, err := playwright.CombinedOutput()
	if err != nil {
		t.Fatalf("真实 Timeline 浏览器验收失败: %v\n%s", err, result)
	}
	t.Logf("真实 Timeline 桌面/移动浏览器通过: %s", strings.TrimSpace(string(result)))
	bodies := make(map[string]string, 2)
	for _, project := range []string{"chromium", "Mobile Chrome"} {
		body, err := os.ReadFile(filepath.Join(resultsDir, project+".json"))
		if err != nil {
			t.Fatalf("读取浏览器响应记录失败: %v", err)
		}
		bodies[project] = string(body)
	}
	return bodies
}

// 测试目标：验证浏览器到真实 Go API、MySQL 与可选 Redis 的完整首页读取链路
// 预期效果：13 条视频真实分页，缓存关闭与开启展示一致且有实际未命中、回源、回填和命中证据
func TestTimelineBrowserLive(t *testing.T) {
	if os.Getenv("GOFEED_TIMELINE_BROWSER") != "1" {
		t.Skip("设置 GOFEED_TIMELINE_BROWSER=1 显式运行隔离真实浏览器验收")
	}
	env := newFeedTestEnv(t)
	events := &timelineCacheEvents{counts: make(map[string]int)}
	originalOutput := log.Writer()
	log.SetOutput(io.MultiWriter(originalOutput, events))
	t.Cleanup(func() { log.SetOutput(originalOutput) })
	uncached, client := env.newServer(t, nil)
	register(t, client, uncached.URL, "timeline_browser_author", "timeline-browser-password-123")
	session := login(t, client, uncached.URL, "timeline_browser_author", "timeline-browser-password-123")
	items := publishFeedVideos(t, env, uncached.URL, session.AccessToken, client, 13)
	var databaseName string
	if err := env.gdb.Raw("SELECT DATABASE()").Scan(&databaseName).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("隔离 MySQL 库: %s; 13 条可见视频; 退出时由 testutil.Main 删除", databaseName)
	setFeedVideoPublishedAt(t, env, items[1].ID, feedBaseTime)
	ids := make([]uint, 0, len(items))
	ids = append(ids, items[1].ID, items[0].ID)
	for _, item := range items[2:] {
		ids = append(ids, item.ID)
	}
	var baseline map[string]string
	if !t.Run("cache_disabled", func(t *testing.T) {
		baseline = runTimelineBrowser(t, uncached.URL, ids)
		if len(env.recorder.reads()) != 0 || len(env.recorder.writes()) != 0 || len(events.snapshot()) != 0 {
			t.Fatal("关闭缓存时不应访问 Redis 页缓存或产生缓存事件")
		}
	}) {
		return
	}
	cached, _ := env.newServer(t, env.livePageCache(t))
	t.Run("cache_enabled", func(t *testing.T) {
		bodies := runTimelineBrowser(t, cached.URL, ids)
		for project, body := range bodies {
			if body != baseline[project] {
				t.Errorf("缓存开启前后 %s 浏览器首屏及分页响应应逐字节一致", project)
			}
		}
		counts := events.snapshot()
		for result, want := range map[string]int{"first_page": 4, "miss": 1, "mysql_read": 5, "write_ok": 1, "hit": 3} {
			if counts[result] != want {
				t.Errorf("缓存事件 %s got=%d want=%d; events=%v", result, counts[result], want, counts)
			}
		}
		if len(env.recorder.reads()) != 4 || len(env.recorder.writes()) != 1 {
			t.Fatalf("真实 Redis 访问不符 reads=%v writes=%v", env.recorder.reads(), env.recorder.writes())
		}
		for _, key := range env.recorder.reads() {
			if key != env.recorder.writes()[0] {
				t.Fatal("重复浏览器分页应使用相同精确缓存键")
			}
		}
		if !env.recorder.keyExists(t, env.recorder.writes()[0]) {
			t.Fatal("真实 Redis 回填键不存在")
		}
		t.Logf("实际 Feed 缓存观测: %v; Redis reads=4 writes=1", counts)
	})
}
