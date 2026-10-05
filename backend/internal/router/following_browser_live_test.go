package router

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gofeed/internal/social"
)

// followingLiveSpecTS 由测试写入临时目录后交给 Playwright 执行，不落入仓库前端目录
const followingLiveSpecTS = `import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'

import { expect, test } from '@playwright/test'

const authorB = Number(process.env.GOFEED_FOLLOWING_AUTHOR_B)
const authorC = Number(process.env.GOFEED_FOLLOWING_AUTHOR_C)
const resultsDir = process.env.GOFEED_FOLLOWING_RESULTS

const entries = []
const apiRequests = []

test('真实 Following 关注流桌面与移动全流程', async ({ page }, testInfo) => {
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname.startsWith('/api/')) {
      apiRequests.push({ url: url.pathname + url.search, auth: request.headers()['authorization'] !== undefined })
    }
  })
  await page.route('**/static/videos/**', (route) => {
    route.fulfill({ contentType: 'video/webm', path: resolve('e2e/fixtures/playable.webm') })
  })
  await page.route('**/static/covers/**', (route) => {
    route.fulfill({
      contentType: 'image/png',
      body: Buffer.from(
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=',
        'base64',
      ),
    })
  })

  // 每次记录后即时落盘，失败时已收集的请求与响应记录仍然保留
  const flush = () => {
    writeFileSync(resolve(resultsDir, testInfo.project.name + '.json'), JSON.stringify({ entries, apiRequests }))
  }
  const record = async (label, response) => {
    const url = new URL(response.url())
    entries.push({
      label,
      url: url.pathname + url.search,
      status: response.status(),
      cacheControl: response.headers()['cache-control'] || '',
      vary: response.headers()['vary'] || '',
      body: await response.text(),
    })
    flush()
  }
  const feedResponse = (withCursor) => (response) => {
    const url = new URL(response.url())
    return url.pathname === '/api/feed' && url.searchParams.get('scene') === 'following'
      && url.searchParams.has('cursor') === withCursor
  }
  const timelineResponse = (withCursor) => (response) => {
    const url = new URL(response.url())
    return url.pathname === '/api/feed' && url.searchParams.get('scene') === 'timeline'
      && url.searchParams.has('cursor') === withCursor
  }
  const feedEl = page.locator('.short-feed')
  const cards = page.locator('.short-video')
  const scrollFeed = async () => {
    await feedEl.evaluate((element) => {
      element.scrollTo({ top: element.scrollHeight })
      element.dispatchEvent(new Event('scroll'))
    })
  }
  const openFollowing = async (labelFirst, labelMore, navigate) => {
    if (navigate) {
      await page.goto('/')
    }
    const firstPromise = page.waitForResponse(feedResponse(false))
    await page.locator('.feed-tab').filter({ hasText: '关注' }).click()
    const first = await firstPromise
    expect(first.status()).toBe(200)
    await record(labelFirst, first)
    const firstBody = JSON.parse(entries[entries.length - 1].body)
    if (firstBody.next_cursor) {
      await expect(cards).toHaveCount(12)
      const secondPromise = page.waitForResponse(feedResponse(true))
      await scrollFeed()
      const second = await secondPromise
      expect(second.status()).toBe(200)
      await record(labelMore, second)
    } else {
      await expect(page.getByText('还没有可看的关注视频')).toBeVisible()
      await expect(cards).toHaveCount(0)
    }
  }
  const login = async (username) => {
    await page.goto('/login')
    await page.getByLabel('用户名', { exact: true }).fill(username)
    await page.getByLabel('密码', { exact: true }).fill('following-live-password-123')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await expect(page.locator('.account-state__name')).toHaveText(username)
    await page.waitForURL('**/')
    await expect(cards).toHaveCount(12)
    flush()
  }
  const toggleFollow = async (authorID, wantPressed, toastText) => {
    await page.goto('/users/' + authorID)
    const button = page.locator('.follow-button')
    await expect(button).toHaveAttribute('aria-pressed', String(!wantPressed))
    await button.click()
    await expect(page.locator('.toast__message').filter({ hasText: toastText })).toBeVisible()
    await expect(button).toHaveAttribute('aria-pressed', String(wantPressed))
    flush()
  }

  // 1 匿名 Timeline 首屏与续页
  let firstPromise = page.waitForResponse(timelineResponse(false))
  await page.goto('/')
  const timelineFirst = await firstPromise
  expect(timelineFirst.status()).toBe(200)
  await record('timeline_first', timelineFirst)
  await expect(cards).toHaveCount(12)
  const timelineMore = page.waitForResponse(timelineResponse(true))
  await scrollFeed()
  const timelineSecond = await timelineMore
  expect(timelineSecond.status()).toBe(200)
  await record('timeline_second', timelineSecond)
  await expect(cards).toHaveCount(17)

  // 2 作者主页旧接口与旧游标续页
  const authorPage = (withCursor) => (response) => {
    const url = new URL(response.url())
    return url.pathname === '/api/video' && url.searchParams.get('author_id') === String(authorB)
      && url.searchParams.has('cursor') === withCursor
  }
  firstPromise = page.waitForResponse(authorPage(false))
  await page.goto('/users/' + authorB)
  const authorPageFirst = await firstPromise
  expect(authorPageFirst.status()).toBe(200)
  await record('author_first', authorPageFirst)
  await expect(page.locator('.video-list-item')).toHaveCount(12)
  const authorMore = page.waitForResponse(authorPage(true))
  await page.getByRole('button', { name: '加载更多' }).click()
  const authorPageSecond = await authorMore
  expect(authorPageSecond.status()).toBe(200)
  await record('author_second', authorPageSecond)
  await expect(page.locator('.video-list-item')).toHaveCount(14)

  // 3 登录观看者并读取关注流历史视频
  await login('following-live-viewer')
  await openFollowing('following_first', 'following_second', false)

  // 4 取关作者 C 后下一次查询排除其视频
  await toggleFollow(authorC, false, '已取消关注')
  await openFollowing('following_after_unfollow', 'following_after_unfollow_more', true)

  // 受控失败注入点：仅当 GOFEED_FOLLOWING_CONTROLLED_FAILURE=after_unfollow 时触发，用于验证失败证据保留与夹具恢复
  if (process.env.GOFEED_FOLLOWING_CONTROLLED_FAILURE === 'after_unfollow') {
    flush()
    throw new Error('受控失败注入：取关作者 C 之后预期失败')
  }

  // 5 重新关注后刷新恢复
  await toggleFollow(authorC, true, '已关注')
  await openFollowing('following_after_refollow', 'following_after_refollow_more', true)

  // 6 退出后关注流回到登录提示；移动端侧边栏隐藏无退出按钮，执行应用同款登出序列
  if (testInfo.project.name === 'chromium') {
    await page.getByRole('button', { name: '退出' }).click()
  } else {
    await page.evaluate(async () => {
      const raw = localStorage.getItem('gofeed.auth.session')
      const token = raw ? JSON.parse(raw).access_token : ''
      if (token) {
        await fetch('/api/user/auth/logout', { method: 'POST', headers: { Authorization: 'Bearer ' + token } })
      }
      localStorage.removeItem('gofeed.auth.session')
    })
    await page.reload()
  }
  await expect(page.locator('.account-state__name')).toHaveText('未登录')
  await page.locator('.feed-tab').filter({ hasText: '关注' }).click()
  await expect(page.locator('.feed-signin-link')).toBeVisible()

  // 7 换无关注用户为空态，再关注作者 B 读取历史视频
  await login('following-live-other-viewer')
  await openFollowing('following_empty', null, false)
  await toggleFollow(authorB, true, '已关注')
  await openFollowing('following_other_first', 'following_other_second', true)

  // 8 恢复初始关注关系
  await toggleFollow(authorB, false, '已取消关注')

  flush()
})
`

// followingLiveEntry 保存单条真实响应的契约证据
type followingLiveEntry struct {
	Label        string `json:"label"`
	URL          string `json:"url"`
	Status       int    `json:"status"`
	CacheControl string `json:"cacheControl"`
	Vary         string `json:"vary"`
	Body         string `json:"body"`
}

type followingLiveRequest struct {
	URL  string `json:"url"`
	Auth bool   `json:"auth"`
}

type followingLiveResult struct {
	Entries  []followingLiveEntry   `json:"entries"`
	Requests []followingLiveRequest `json:"apiRequests"`
}

// followingBrowserFixture 聚合本用例创建的账号、媒体条目与环境，用于项目开始前确认与结束后恢复关注集合
type followingBrowserFixture struct {
	env           *feedTestEnv
	viewerID      uint
	otherViewerID uint
	authorB       uint
	authorC       uint
	itemsB        []draftItem
	itemsC        []draftItem
}

// 测试目标：为浏览器联调准备真实发布链路与当前关注关系的隔离环境
// 预期效果：四个账号经生产接口注册，17 条视频全部先于关注关系发布，形成可见历史集合
func setupFollowingBrowserFixture(t *testing.T, env *feedTestEnv, base string, client *http.Client) followingBrowserFixture {
	t.Helper()
	register(t, client, base, "following-live-viewer", "following-live-password-123")
	viewer := login(t, client, base, "following-live-viewer", "following-live-password-123")
	register(t, client, base, "following-live-other-viewer", "following-live-password-123")
	otherViewer := login(t, client, base, "following-live-other-viewer", "following-live-password-123")
	register(t, client, base, "following-live-author-b", "following-live-password-123")
	authorB := login(t, client, base, "following-live-author-b", "following-live-password-123")
	register(t, client, base, "following-live-author-c", "following-live-password-123")
	authorC := login(t, client, base, "following-live-author-c", "following-live-password-123")
	itemsB := publishFeedVideos(t, env, base, authorB.AccessToken, client, 14)
	itemsC := make([]draftItem, 0, 3)
	for index := 0; index < 3; index++ {
		item := publishCompleteVideo(t, env.gdb, client, base, authorC.AccessToken, fmt.Sprintf("关注作者C视频 %d", index+1))
		setFeedVideoPublishedAt(t, env, item.ID, feedBaseTime.Add(-time.Hour-time.Duration(index)*10*time.Second))
		itemsC = append(itemsC, item)
	}
	for _, followee := range []uint{authorB.UserID, authorC.UserID} {
		if err := env.gdb.Create(&social.Follow{FollowerID: viewer.UserID, FolloweeID: followee}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return followingBrowserFixture{
		env:           env,
		viewerID:      viewer.UserID,
		otherViewerID: otherViewer.UserID,
		authorB:       authorB.UserID,
		authorC:       authorC.UserID,
		itemsB:        itemsB,
		itemsC:        itemsC,
	}
}

// 测试目标：把隔离库中本用例创建的关注关系精确恢复为初始状态并读回校验
// 预期效果：观看者恰好关注作者 B 和 C，另一观看者不关注作者 B，其他数据不受影响
func ensureFollowingFixtureRelations(fixture followingBrowserFixture) error {
	if err := fixture.env.gdb.Where("follower_id = ? AND followee_id IN ?", fixture.viewerID, []uint{fixture.authorB, fixture.authorC}).Delete(&social.Follow{}).Error; err != nil {
		return fmt.Errorf("重置观看者关注失败: %w", err)
	}
	if err := fixture.env.gdb.Where("follower_id = ? AND followee_id = ?", fixture.otherViewerID, fixture.authorB).Delete(&social.Follow{}).Error; err != nil {
		return fmt.Errorf("移除另一观看者关注失败: %w", err)
	}
	for _, followee := range []uint{fixture.authorB, fixture.authorC} {
		if err := fixture.env.gdb.Create(&social.Follow{FollowerID: fixture.viewerID, FolloweeID: followee}).Error; err != nil {
			return fmt.Errorf("恢复观看者关注失败: %w", err)
		}
	}
	var follows []social.Follow
	if err := fixture.env.gdb.Where("follower_id IN ?", []uint{fixture.viewerID, fixture.otherViewerID}).Find(&follows).Error; err != nil {
		return fmt.Errorf("读回关注集合失败: %w", err)
	}
	want := map[uint]map[uint]bool{fixture.viewerID: {fixture.authorB: true, fixture.authorC: true}, fixture.otherViewerID: {}}
	got := map[uint]map[uint]bool{fixture.viewerID: {}, fixture.otherViewerID: {}}
	for _, row := range follows {
		got[row.FollowerID][row.FolloweeID] = true
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("关注集合 got=%v want=%v", got, want)
	}
	return nil
}

// 测试目标：确定本轮浏览器证据的持久化根目录并使用绝对路径
// 预期效果：设置 GOFEED_FOLLOWING_ARTIFACTS 时证据持久保存到报告目录，未设置时退回临时目录并明示
func followingArtifactsRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("GOFEED_FOLLOWING_ARTIFACTS")
	if root == "" {
		t.Logf("未设置 GOFEED_FOLLOWING_ARTIFACTS，本轮浏览器证据保存在临时目录，用例结束即删除")
		return t.TempDir()
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("解析证据根目录失败: %v", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatalf("创建证据根目录失败: %v", err)
	}
	return abs
}

// 测试目标：按缓存模式分配独立证据子目录并在重跑遇到同名目录时追加序号
// 预期效果：每轮运行的证据目录唯一，不覆盖前一轮产物
func followingArtifactsDir(t *testing.T, root, mode string) string {
	t.Helper()
	base := filepath.Join(root, mode)
	candidate := base
	for index := 2; ; index++ {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			break
		}
		candidate = fmt.Sprintf("%s-%d", base, index)
	}
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("创建证据子目录失败: %v", err)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		t.Fatalf("解析证据子目录失败: %v", err)
	}
	t.Logf("缓存模式 %s 的浏览器证据目录: %s", mode, abs)
	return abs
}

// 测试目标：把联调实际执行的命令与退出码写入证据目录
// 预期效果：审查者可以核对命令行、工作目录和退出码
func writeFollowingCommandRecord(t *testing.T, path, label string, command *exec.Cmd, exit string) {
	t.Helper()
	record := fmt.Sprintf("阶段: %s\n工作目录: %s\n命令: %s\n退出: %s\n", label, command.Dir, command.String(), exit)
	if err := os.WriteFile(path, []byte(record), 0o644); err != nil {
		t.Errorf("写入命令记录失败: %v", err)
	}
}

// 测试目标：按缓存模式启动隔离 Vite 并逐个浏览器项目执行 Playwright
// 预期效果：每个项目开始前确认初始关注集合，结束无论成败恢复夹具；恢复与证据保存互不阻断
func runFollowingBrowser(t *testing.T, fixture followingBrowserFixture, mode, apiURL string, onProjectResult func(t *testing.T, project string, result followingLiveResult)) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal("真实浏览器验收需要当前进程 PATH 中的 Node")
	}
	frontend := followingFrontendDir(t)
	absFrontend, err := filepath.Abs(frontend)
	if err != nil {
		t.Fatal(err)
	}
	webURL, portArg := followingFreeWebURL(t)
	artifacts := followingArtifactsDir(t, followingArtifactsRoot(t), mode)
	specDir := t.TempDir()
	specPath := filepath.Join(specDir, "following-live.spec.ts")
	if err := os.WriteFile(specPath, []byte(followingLiveSpecTS), 0o600); err != nil {
		t.Fatal(err)
	}
	baseBrowserEnv := timelineBrowserEnv(map[string]string{
		"GOFEED_TIMELINE_API":       apiURL,
		"GOFEED_FOLLOWING_AUTHOR_B": fmt.Sprint(fixture.authorB),
		"GOFEED_FOLLOWING_AUTHOR_C": fmt.Sprint(fixture.authorC),
		// 先清空继承的受控失败变量，注入只按项目追加，保证后续视口不受污染
		"GOFEED_FOLLOWING_CONTROLLED_FAILURE": "",
		"NODE_PATH":                           filepath.Join(absFrontend, "node_modules"),
	})
	startFollowingVite(t, frontend, baseBrowserEnv, artifacts, portArg, webURL, apiURL)
	for _, project := range []string{"chromium", "Mobile Chrome"} {
		t.Run(project, func(t *testing.T) {
			projectDir := filepath.Join(artifacts, project)
			resultsDir := filepath.Join(projectDir, "results")
			if err := os.MkdirAll(resultsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			// 项目开始前确认初始关注集合，集合不符时直接失败，浏览器不会继承被污染的状态
			if err := ensureFollowingFixtureRelations(fixture); err != nil {
				t.Fatalf("浏览器项目 %s 开始前关注集合不在初始状态: %v", project, err)
			}
			// 项目结束无论成败都恢复初始关注集合；恢复失败用 Error 报告，不静默吞掉
			t.Cleanup(func() {
				if err := ensureFollowingFixtureRelations(fixture); err != nil {
					t.Errorf("浏览器项目 %s 结束后恢复初始关注集合失败: %v", project, err)
				} else {
					t.Logf("浏览器项目 %s 结束后已恢复初始关注集合", project)
				}
			})
			browserEnv := append([]string(nil), baseBrowserEnv...)
			browserEnv = append(browserEnv, "GOFEED_FOLLOWING_RESULTS="+resultsDir)
			// 受控失败注入只作用于第一个项目，后续视口必须以干净状态运行以证明恢复有效
			if project == "chromium" {
				if controlledFailure := os.Getenv("GOFEED_FOLLOWING_CONTROLLED_FAILURE"); controlledFailure != "" {
					browserEnv = append(browserEnv, "GOFEED_FOLLOWING_CONTROLLED_FAILURE="+controlledFailure)
				}
			}
			result := runFollowingPlaywrightProject(t, frontend, specDir, projectDir, project, webURL, browserEnv)
			if onProjectResult != nil {
				onProjectResult(t, project, result)
			}
		})
	}
}

// 测试目标：解析两套联调用例共用的前端目录
// 预期效果：目录随测试源码定位，不依赖进程工作目录
func followingFrontendDir(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位前端测试目录")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", "frontend"))
}

// 测试目标：分配一个空闲回环端口作为隔离 Vite 地址
// 预期效果：联调服务不与开发端口冲突
func followingFreeWebURL(t *testing.T) (string, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), strconv.Itoa(port)
}

// 测试目标：启动随用例隔离的 Vite 并确认就绪，进程回收交给 t.Cleanup
// 预期效果：两套联调用例共用同一启动与回收路径，日志落在各自证据目录
func startFollowingVite(t *testing.T, frontend string, env []string, artifactsDir, portArg, webURL, apiURL string) {
	t.Helper()
	output, err := os.Create(filepath.Join(artifactsDir, "vite.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	viteScript := filepath.Join(frontend, "node_modules", "vite", "bin", "vite.js")
	vite := exec.Command("node", viteScript, "--config", "vite.config.timeline-live.ts", "--port", portArg)
	vite.Dir, vite.Env, vite.Stdout, vite.Stderr = frontend, env, output, output
	if err := vite.Start(); err != nil {
		t.Fatal(err)
	}
	writeFollowingCommandRecord(t, filepath.Join(artifactsDir, "vite-command.txt"), "vite", vite, "由测试清理终止")
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
	deadline := time.Now().Add(30 * time.Second)
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
}

// 测试目标：构造指向临时 spec 目录的 Playwright 配置
// 预期效果：两套联调 spec 共用同一配置形态，输出目录与 baseURL 按运行注入
func followingPlaywrightConfig(testDir, outputDir, webURL, testMatch string) string {
	return fmt.Sprintf(`export default {
  testDir: %q,
  testMatch: %q,
  timeout: 240000,
  expect: { timeout: 15000 },
  retries: 0,
  workers: 1,
  reporter: 'line',
  outputDir: %q,
  use: { baseURL: %q, headless: true, screenshot: 'only-on-failure', trace: 'retain-on-failure' },
  projects: [
    { name: 'chromium', use: { viewport: { width: 1280, height: 720 } } },
    { name: 'Mobile Chrome', use: { viewport: { width: 393, height: 851 }, isMobile: true, hasTouch: true } },
  ],
}
`, filepath.ToSlash(testDir), testMatch, filepath.ToSlash(outputDir), webURL)
}

// 测试目标：执行单个浏览器项目的 Playwright 并在成功与失败路径都保存证据
// 预期效果：命令、退出码与输出日志先于失败退出落盘，trace 在证据目录内完成脱敏后保留
func executeFollowingPlaywright(t *testing.T, frontend, configPath, project, projectDir string, env []string) {
	t.Helper()
	outputDir := filepath.Join(projectDir, "playwright-output")
	playwrightScript := filepath.Join(frontend, "node_modules", "@playwright", "test", "cli.js")
	playwright := exec.Command("node", playwrightScript, "test", "--config", configPath, "--project", project, "--workers=1", "--reporter=line")
	playwright.Dir, playwright.Env = frontend, env
	var output bytes.Buffer
	playwright.Stdout, playwright.Stderr = &output, &output
	// 测试目标：限制单个项目的浏览器执行时长，超时后强制结束进程
	// 预期效果：挂起的浏览器不会拖垮整个测试进程的 25 分钟上限
	if err := playwright.Start(); err != nil {
		t.Fatal(err)
	}
	timeout := time.AfterFunc(10*time.Minute, func() { _ = playwright.Process.Kill() })
	runErr := playwright.Wait()
	timeout.Stop()
	// 证据先于失败退出落盘：输出日志、退出码与命令记录在任何断言之前保存
	outputPath := filepath.Join(projectDir, "playwright-output.log")
	if err := os.WriteFile(outputPath, output.Bytes(), 0o644); err != nil {
		t.Errorf("保存 Playwright 输出失败: %v", err)
	}
	exitDescription := "0"
	if runErr != nil {
		exitDescription = fmt.Sprintf("非零（%v）", runErr)
		if playwright.ProcessState != nil {
			exitDescription = fmt.Sprintf("%d（%v）", playwright.ProcessState.ExitCode(), runErr)
		}
	}
	writeFollowingCommandRecord(t, filepath.Join(projectDir, "command.txt"), "playwright "+project, playwright, exitDescription)
	// 成败两条路径都必须在证据目录留下已脱敏的 trace
	followingSanitizeTraceOutputs(t, outputDir)
	if runErr != nil {
		followingScrubCredentialFiles(t, projectDir)
		t.Fatalf("真实 Following 浏览器验收失败（项目 %s）: %v\n输出已保存: %s", project, runErr, outputPath)
	}
	followingScrubCredentialFiles(t, projectDir)
}

// 测试目标：为单个浏览器项目执行既有全流程 spec 并回传其脱敏请求与响应记录
// 预期效果：复用统一的命令执行与证据保存路径
func runFollowingPlaywrightProject(t *testing.T, frontend, specDir, projectDir, project, webURL string, env []string) followingLiveResult {
	t.Helper()
	outputDir := filepath.Join(projectDir, "playwright-output")
	configPath := filepath.Join(projectDir, "playwright.following-live.config.ts")
	if err := os.WriteFile(configPath, []byte(followingPlaywrightConfig(specDir, outputDir, webURL, "following-live.spec.ts")), 0o600); err != nil {
		t.Fatal(err)
	}
	executeFollowingPlaywright(t, frontend, configPath, project, projectDir, env)
	raw, err := os.ReadFile(filepath.Join(projectDir, "results", project+".json"))
	if err != nil {
		t.Fatalf("读取浏览器结果失败: %v", err)
	}
	var parsed followingLiveResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("解析浏览器结果失败: %v", err)
	}
	return parsed
}

// followingTraceJWT 匹配 JWT 三段式形态，用于对 trace 文本做兜底抹除
var followingTraceJWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

// followingCredentialPattern 匹配本用例的合成夹具密码形态，持久化证据中统一抹除
var followingCredentialPattern = regexp.MustCompile(`following-[a-z0-9-]*password-[a-z0-9-]*`)

// followingTraceEntry 保存待重写的压缩包条目
type followingTraceEntry struct {
	header *zip.FileHeader
	data   []byte
}

// 测试目标：对 Playwright 输出目录内的全部 trace 压缩包执行脱敏
// 预期效果：持久化证据不包含 Authorization 或令牌；脱敏失败时删除该 trace 并报告
func followingSanitizeTraceOutputs(t *testing.T, outputDir string) {
	t.Helper()
	var traces []string
	err := filepath.WalkDir(outputDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == "trace.zip" {
			traces = append(traces, path)
		}
		return nil
	})
	if err != nil {
		t.Errorf("遍历 Playwright 输出失败: %v", err)
		return
	}
	for _, trace := range traces {
		if err := sanitizeFollowingTraceZip(trace); err != nil {
			if removeErr := os.Remove(trace); removeErr != nil {
				t.Errorf("脱敏失败的 trace 无法删除 %s: %v", trace, removeErr)
			}
			t.Errorf("trace 脱敏失败已删除 %s: %v", trace, err)
		}
	}
}

// 测试目标：把输出目录内文本文件中的夹具密码抹除
// 预期效果：error-context、输出日志等证据文件不包含完整夹具密码
func followingScrubCredentialFiles(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 32<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scrubbed := followingCredentialPattern.ReplaceAll(data, []byte("[REDACTED]"))
		if !bytes.Equal(data, scrubbed) {
			return os.WriteFile(path, scrubbed, 0o644)
		}
		return nil
	})
	if err != nil {
		t.Errorf("抹除证据文件中的夹具密码失败: %v", err)
	}
}

// 测试目标：抹除 trace 压缩包中的认证头、登录注册刷新请求与认证响应体后重写
// 预期效果：重写后的 trace 保留行为与网络结构证据但不包含完整令牌或密码
func sanitizeFollowingTraceZip(path string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	entries := make([]followingTraceEntry, 0, len(reader.File))
	pending := make(map[string]bool)
	for _, file := range reader.File {
		opened, err := file.Open()
		if err != nil {
			reader.Close()
			return err
		}
		data, err := io.ReadAll(opened)
		opened.Close()
		if err != nil {
			reader.Close()
			return err
		}
		name := filepath.ToSlash(file.Name)
		// 条目名带 chunk 序号前缀，如 0-trace.network，按后缀匹配
		if strings.HasSuffix(name, "trace.trace") || strings.HasSuffix(name, "trace.network") {
			data = sanitizeFollowingTraceNDJSON(data, pending)
		} else if bytes.Contains(data, []byte("following-")) {
			data = followingCredentialPattern.ReplaceAll(data, []byte("[REDACTED]"))
		}
		entries = append(entries, followingTraceEntry{header: &file.FileHeader, data: data})
	}
	if err := reader.Close(); err != nil {
		return err
	}
	for index := range entries {
		if sha1File, ok := strings.CutPrefix(filepath.ToSlash(entries[index].header.Name), "resources/"); ok && pending[sha1File] {
			entries[index].data = []byte(`{"redacted":"auth response body"}`)
		}
	}
	temporary := path + ".sanitizing"
	if err := writeFollowingZip(temporary, entries); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, path)
}

// 测试目标：逐行处理 trace NDJSON，抹除认证载荷并登记待清除的请求与响应资源
// 预期效果：结构化字段被抹除后仍保持 NDJSON 结构可被 trace 查看器读取
func sanitizeFollowingTraceNDJSON(data []byte, pending map[string]bool) []byte {
	var builder strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			builder.WriteString("\n")
			continue
		}
		trimmed = followingTraceJWT.ReplaceAllString(trimmed, "[REDACTED_JWT]")
		trimmed = followingCredentialPattern.ReplaceAllString(trimmed, "[REDACTED]")
		var event map[string]any
		if err := json.Unmarshal([]byte(trimmed), &event); err == nil && event["type"] == "resource-snapshot" {
			if redactFollowingResourceSnapshot(event, pending) {
				if reencoded, err := json.Marshal(event); err == nil {
					trimmed = string(reencoded)
				}
			}
		}
		builder.WriteString(trimmed)
		builder.WriteString("\n")
	}
	return []byte(builder.String())
}

// 测试目标：抹除认证请求头并定位登录注册刷新请求与响应的内联和资源载荷
// 预期效果：Authorization 头不可恢复，认证接口的载荷被登记为待清除
func redactFollowingResourceSnapshot(event map[string]any, pending map[string]bool) bool {
	changed := false
	snapshot, _ := event["snapshot"].(map[string]any)
	if snapshot == nil {
		return false
	}
	request, _ := snapshot["request"].(map[string]any)
	response, _ := snapshot["response"].(map[string]any)
	requestURL, _ := request["url"].(string)
	authTarget := strings.Contains(requestURL, "/api/user/login") || strings.Contains(requestURL, "/api/user/register") || strings.Contains(requestURL, "/api/user/refresh")
	if request != nil {
		if redactFollowingHeaders(request["headers"]) {
			changed = true
		}
		if authTarget {
			if postData, ok := request["postData"].(map[string]any); ok {
				if _, has := postData["text"]; has {
					postData["text"] = "[REDACTED]"
					changed = true
				}
				if _, has := postData["params"]; has {
					postData["params"] = []any{}
					changed = true
				}
				if sha1, ok := postData["_sha1"].(string); ok && sha1 != "" {
					pending[sha1] = true
					changed = true
				}
			}
		}
	}
	if response != nil {
		if redactFollowingHeaders(response["headers"]) {
			changed = true
		}
		if authTarget {
			if content, ok := response["content"].(map[string]any); ok {
				if sha1, ok := content["_sha1"].(string); ok && sha1 != "" {
					pending[sha1] = true
					changed = true
				}
			}
		}
	}
	return changed
}

// 测试目标：抹除请求或响应头中的 Authorization 值
// 预期效果：头结构保留，认证值替换为固定占位符
func redactFollowingHeaders(raw any) bool {
	headers, ok := raw.([]any)
	if !ok {
		return false
	}
	changed := false
	for _, item := range headers {
		header, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := header["name"].(string); strings.EqualFold(name, "authorization") {
			header["value"] = "[REDACTED]"
			changed = true
		}
	}
	return changed
}

// 测试目标：把脱敏后的条目重写为 trace 压缩包
// 预期效果：条目名与压缩属性保留，输出文件可被 trace 查看器打开
func writeFollowingZip(path string, entries []followingTraceEntry) error {
	target, err := os.Create(path)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(target)
	for _, entry := range entries {
		writerEntry, err := writer.CreateHeader(entry.header)
		if err != nil {
			writer.Close()
			target.Close()
			return err
		}
		if _, err := writerEntry.Write(entry.data); err != nil {
			writer.Close()
			target.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		target.Close()
		return err
	}
	return target.Close()
}

var followingLiveLabels = []string{
	"timeline_first", "timeline_second",
	"author_first", "author_second",
	"following_first", "following_second",
	"following_after_unfollow", "following_after_unfollow_more",
	"following_after_refollow", "following_after_refollow_more",
	"following_empty",
	"following_other_first", "following_other_second",
}

// 测试目标：按标签核对响应序列、私有响应头、认证头与关注集合的可见性
// 预期效果：关注流只含关注作者的历史视频，取关/重关注/换用户在下一次查询生效
func assertFollowingLiveResult(t *testing.T, result followingLiveResult, itemsB, itemsC []draftItem) {
	t.Helper()
	if len(result.Entries) != len(followingLiveLabels) {
		t.Fatalf("条目数=%d want=%d labels=%v", len(result.Entries), len(followingLiveLabels), entryLabels(result.Entries))
	}
	byLabel := make(map[string]followingLiveEntry, len(result.Entries))
	for index, entry := range result.Entries {
		if entry.Label != followingLiveLabels[index] {
			t.Fatalf("第 %d 个条目标签=%s want=%s", index, entry.Label, followingLiveLabels[index])
		}
		byLabel[entry.Label] = entry
	}
	idsOf := func(items ...[]draftItem) []uint {
		ids := make([]uint, 0)
		for _, group := range items {
			for _, item := range group {
				ids = append(ids, item.ID)
			}
		}
		return ids
	}
	firstB := itemsB[:12]
	restB := itemsB[12:]
	page2WithC := idsOf(restB, itemsC)
	wantRestB := idsOf(restB)
	bodyIDs := func(label string) []uint {
		var page feedTimelineResponse
		if err := json.Unmarshal([]byte(byLabel[label].Body), &page); err != nil {
			t.Fatalf("解析 %s 响应失败: %v", label, err)
		}
		return followingIDs(page)
	}
	expectIDs := map[string][]uint{
		"timeline_first":                idsOf(firstB),
		"timeline_second":               page2WithC,
		"author_first":                  idsOf(firstB),
		"author_second":                 wantRestB,
		"following_first":               idsOf(firstB),
		"following_second":              page2WithC,
		"following_after_unfollow":      idsOf(firstB),
		"following_after_unfollow_more": wantRestB,
		"following_after_refollow":      idsOf(firstB),
		"following_after_refollow_more": page2WithC,
		"following_empty":               {},
		"following_other_first":         idsOf(firstB),
		"following_other_second":        wantRestB,
	}
	for label, want := range expectIDs {
		entry := byLabel[label]
		if entry.Status != 200 {
			t.Fatalf("%s 状态=%d body=%s", label, entry.Status, entry.Body)
		}
		if got := bodyIDs(label); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s 视频序列=%v want=%v", label, got, want)
		}
	}
	if byLabel["following_after_refollow_more"].Body != byLabel["following_second"].Body {
		t.Fatal("重新关注后的续页应与取关前逐字节一致")
	}
	var emptyPage feedTimelineResponse
	if err := json.Unmarshal([]byte(byLabel["following_empty"].Body), &emptyPage); err != nil || emptyPage.NextCursor != "" {
		t.Fatalf("空页=%+v err=%v", emptyPage, err)
	}
	for _, entry := range result.Entries {
		switch {
		case strings.HasPrefix(entry.Label, "following"):
			if entry.CacheControl != "private, no-store" || !strings.Contains(strings.ToLower(entry.Vary), "authorization") {
				t.Fatalf("%s 私有头缺失 cache-control=%q vary=%q", entry.Label, entry.CacheControl, entry.Vary)
			}
			if !strings.Contains(entry.URL, "scene=following") {
				t.Fatalf("%s 场景参数=%s", entry.Label, entry.URL)
			}
		default:
			if entry.CacheControl != "" {
				t.Fatalf("%s 不应带私有缓存头 cache-control=%q", entry.Label, entry.CacheControl)
			}
		}
	}
	for _, request := range result.Requests {
		switch {
		case strings.Contains(request.URL, "/api/feed") && strings.Contains(request.URL, "scene=following"):
			if !request.Auth {
				t.Fatalf("关注请求缺少 Authorization: %s", request.URL)
			}
		case strings.Contains(request.URL, "/api/feed"):
			if request.Auth {
				t.Fatalf("Timeline 请求不应携带 Authorization: %s", request.URL)
			}
		case request.URL == "/api/video" || strings.HasPrefix(request.URL, "/api/video?"):
			if request.Auth {
				t.Fatalf("旧接口列表请求不应携带 Authorization: %s", request.URL)
			}
		}
	}
}

func entryLabels(entries []followingLiveEntry) []string {
	labels := make([]string, 0, len(entries))
	for _, entry := range entries {
		labels = append(labels, entry.Label)
	}
	return labels
}

// 测试目标：临时接管生产日志采集 Feed 缓存事件并在用例后恢复
// 预期效果：事件计数只在接管窗口内累计
func setFollowingLogCapture(t *testing.T, events *timelineCacheEvents) {
	t.Helper()
	original := log.Writer()
	log.SetOutput(io.MultiWriter(original, events))
	t.Cleanup(func() { log.SetOutput(original) })
}

// 测试目标：真实浏览器验证 Timeline 匿名、作者页旧接口、Following 登录流与动态关注集合
// 预期效果：同一隔离库与 fixture 下，缓存关闭与开启两套服务的浏览器响应逐字节一致，缓存事件与 Redis 访问只来自 Timeline
func TestFollowingBrowserLive(t *testing.T) {
	if os.Getenv("GOFEED_FOLLOWING_BROWSER") != "1" {
		t.Skip("设置 GOFEED_FOLLOWING_BROWSER=1 显式运行隔离真实浏览器验收")
	}
	env := newFeedTestEnv(t)
	uncached, client := env.newServer(t, nil)
	cached, _ := env.newServer(t, env.livePageCache(t))
	fixture := setupFollowingBrowserFixture(t, env, uncached.URL, client)
	baseline := make(map[string]followingLiveResult, 2)
	if !t.Run("cache_disabled", func(t *testing.T) {
		events := &timelineCacheEvents{counts: make(map[string]int)}
		setFollowingLogCapture(t, events)
		runFollowingBrowser(t, fixture, "cache_disabled", uncached.URL, func(t *testing.T, project string, result followingLiveResult) {
			assertFollowingLiveResult(t, result, fixture.itemsB, fixture.itemsC)
			baseline[project] = result
		})
		if len(events.snapshot()) != 0 || len(env.recorder.reads()) != 0 || len(env.recorder.writes()) != 0 {
			t.Fatalf("关闭缓存时不应有缓存事件或 Redis 访问 events=%v reads=%v writes=%v",
				events.snapshot(), env.recorder.reads(), env.recorder.writes())
		}
	}) {
		return
	}
	t.Run("cache_enabled", func(t *testing.T) {
		events := &timelineCacheEvents{counts: make(map[string]int)}
		setFollowingLogCapture(t, events)
		runFollowingBrowser(t, fixture, "cache_enabled", cached.URL, func(t *testing.T, project string, result followingLiveResult) {
			assertFollowingLiveResult(t, result, fixture.itemsB, fixture.itemsC)
			assertSameFollowingResults(t, project, baseline[project], result)
		})
		counts := events.snapshot()
		if counts["miss"]+counts["hit"] != 2 || counts["miss"] < 1 || counts["write_ok"] != counts["miss"] {
			t.Fatalf("缓存读写事件应只来自两次 Timeline 续页 got=%v", counts)
		}
		if counts["mysql_read"] != counts["first_page"]+counts["miss"] {
			t.Fatalf("mysql_read=%d 应为首屏与未命中回源之和 got=%v", counts["mysql_read"], counts)
		}
		if counts["first_page"] < 8 {
			t.Fatalf("首屏读取次数异常 got=%v", counts)
		}
		if len(env.recorder.reads()) != 2 || len(env.recorder.writes()) != counts["write_ok"] {
			t.Fatalf("Redis 访问 reads=%v writes=%v", env.recorder.reads(), env.recorder.writes())
		}
		for _, key := range env.recorder.reads() {
			if !strings.Contains(key, ":timeline:") {
				t.Fatalf("缓存键应属于 Timeline 页: %s", key)
			}
		}
		for _, key := range env.recorder.writes() {
			if !strings.Contains(key, ":timeline:") {
				t.Fatalf("缓存键应属于 Timeline 页: %s", key)
			}
		}
		t.Logf("实际 Timeline 缓存观测（Following 全程零参与）: %v; Redis reads=2 writes=%d", counts, counts["write_ok"])
	})
}

// followingScenariosSpecTS 前端冻结源码的四个衔接场景：登录回跳、切换归零、400 首屏恢复、401 刷新成功与失败
// 全部请求由真实后端应答；仅用 route 改写一次游标参数制造真实 400，用无效 access token 配合真实 refresh token 制造真实 401
// 无效令牌在 evaluate 回调内以数组拼接构造，仅用于触发真实后端的 401，本身不是任何有效凭据
const followingScenariosSpecTS = `import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'

import { expect, test, type Page, type Response, type TestInfo } from '@playwright/test'

const resultsDir = process.env.GOFEED_FOLLOWING_RESULTS
const viewerName = 'following-live-viewer'

const slug = (value: string) => value.replace(/\s+/g, '_')

function mediaRoutes(page: Page) {
  page.route('**/static/videos/**', (route) => {
    route.fulfill({ contentType: 'video/webm', path: resolve('e2e/fixtures/playable.webm') })
  })
  page.route('**/static/covers/**', (route) => {
    route.fulfill({
      contentType: 'image/png',
      body: Buffer.from(
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=',
        'base64',
      ),
    })
  })
}

function feedResponse(scene: string, withCursor: boolean) {
  return (response: Response) => {
    const url = new URL(response.url())
    return url.pathname === '/api/feed' && url.searchParams.get('scene') === scene
      && url.searchParams.has('cursor') === withCursor
  }
}

function refreshResponse() {
  return (response: Response) => new URL(response.url()).pathname === '/api/user/refresh'
}

function recorder(testInfo: TestInfo) {
  const entries: { label: string; url: string; status: number }[] = []
  const apiRequests: { url: string; auth: boolean }[] = []
  const flush = () => writeFileSync(
    resolve(resultsDir, slug(testInfo.project.name) + '-' + slug(testInfo.title) + '.json'),
    JSON.stringify({ title: testInfo.title, project: testInfo.project.name, entries, apiRequests }),
  )
  const watch = (page: Page) => {
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.pathname.startsWith('/api/')) {
        apiRequests.push({ url: url.pathname + url.search, auth: request.headers()['authorization'] !== undefined })
      }
    })
  }
  const record = async (label: string, response: Response) => {
    const url = new URL(response.url())
    entries.push({ label, url: url.pathname + url.search, status: response.status() })
    flush()
  }
  const mark = (label: string, detail: string) => {
    entries.push({ label, url: detail, status: 0 })
    flush()
  }
  return { entries, apiRequests, flush, watch, record, mark }
}

async function uiLogin(page: Page, username: string) {
  await page.goto('/login')
  await page.getByLabel('用户名', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill('following-live-password-123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('.account-state__name')).toHaveText(username)
  await page.waitForURL('**/')
  await expect(page.locator('.short-video')).toHaveCount(12)
}

async function scrollFeed(page: Page, ratio: number) {
  await page.locator('.short-feed').evaluate((element, value) => {
    element.scrollTo({ top: element.scrollHeight * value })
    element.dispatchEvent(new Event('scroll'))
  }, ratio)
}

// 1 登录回跳：未登录关注页 → 登录入口携带 scene 参数 → 登录后自动回到关注场景
test('未登录关注页登录后返回关注场景', async ({ page }, testInfo) => {
  const rec = recorder(testInfo)
  rec.watch(page)
  mediaRoutes(page)
  await page.goto('/')
  await page.locator('.feed-tab').filter({ hasText: '关注' }).click()
  const signin = page.locator('.feed-signin-link')
  await expect(signin).toBeVisible()
  const href = await signin.getAttribute('href')
  expect(href).toContain('/login')
  expect(href).toContain('scene=following')
  rec.mark('signin_redirect_href', href)
  await signin.click()
  await expect(page).toHaveURL(/\/login/)
  const followingPromise = page.waitForResponse(feedResponse('following', false))
  await page.getByLabel('用户名', { exact: true }).fill(viewerName)
  await page.getByLabel('密码', { exact: true }).fill('following-live-password-123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  const following = await followingPromise
  await rec.record('following_after_login', following)
  expect(following.status()).toBe(200)
  await expect(page).toHaveURL(/scene=following/)
  await expect(page.locator('.account-state__name')).toHaveText(viewerName)
  await expect(page.locator('.short-video')).toHaveCount(12)
  rec.flush()
})

// 2 多卡片滚动后切换场景：双向切换都从顶部首卡开始，切回缓存场景不重放请求
test('多卡片滚动后切换场景归零', async ({ page }, testInfo) => {
  const rec = recorder(testInfo)
  rec.watch(page)
  mediaRoutes(page)
  await uiLogin(page, viewerName)
  const cards = page.locator('.short-video')
  const timelineMore = page.waitForResponse(feedResponse('timeline', true))
  await scrollFeed(page, 1)
  const timelineSecond = await timelineMore
  await rec.record('timeline_second', timelineSecond)
  expect(timelineSecond.status()).toBe(200)
  await expect(cards).toHaveCount(17)
  rec.mark('timeline_scrolled_cards', '17')
  const followingFirst = page.waitForResponse(feedResponse('following', false))
  await page.locator('.feed-tab').filter({ hasText: '关注' }).click()
  const following = await followingFirst
  await rec.record('following_first', following)
  expect(following.status()).toBe(200)
  await expect(page).toHaveURL(/scene=following/)
  await expect(cards).toHaveCount(12)
  const topAfterSwitch = await page.locator('.short-feed').evaluate((element) => element.scrollTop)
  expect(topAfterSwitch).toBe(0)
  rec.mark('following_switch_scroll_top', String(topAfterSwitch))
  await scrollFeed(page, 0.5)
  await page.locator('.feed-tab').filter({ hasText: '最新' }).click()
  await expect(page).not.toHaveURL(/scene=following/)
  await expect(cards).toHaveCount(17)
  const topAfterBack = await page.locator('.short-feed').evaluate((element) => element.scrollTop)
  expect(topAfterBack).toBe(0)
  rec.mark('timeline_switch_back_scroll_top', String(topAfterBack))
  const timelineRequestCount = rec.apiRequests.filter((request) => request.url.includes('/api/feed')
    && request.url.includes('scene=timeline')).length
  expect(timelineRequestCount).toBe(2)
  rec.flush()
})

// 3 分页 400：route 改写一次游标参数由真实后端返回 400，重试清空旧游标并首屏重载归零
test('分页 400 后重试归零重载首屏', async ({ page }, testInfo) => {
  const rec = recorder(testInfo)
  rec.watch(page)
  mediaRoutes(page)
  let tampered = false
  page.route('**/api/feed*', (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/feed' && url.searchParams.get('scene') === 'following'
      && url.searchParams.has('cursor') && !tampered) {
      tampered = true
      url.searchParams.set('cursor', 'scenarios-tampered-cursor')
      void route.continue({ url: url.toString() })
      return
    }
    void route.continue()
  })
  await uiLogin(page, viewerName)
  const cards = page.locator('.short-video')
  const firstFollowing = page.waitForResponse(feedResponse('following', false))
  await page.locator('.feed-tab').filter({ hasText: '关注' }).click()
  const first = await firstFollowing
  await rec.record('following_first', first)
  expect(first.status()).toBe(200)
  await expect(cards).toHaveCount(12)
  const tamperedPromise = page.waitForResponse(feedResponse('following', true))
  await scrollFeed(page, 1)
  const tamperedResponse = await tamperedPromise
  await rec.record('following_tampered_400', tamperedResponse)
  expect(tamperedResponse.status()).toBe(400)
  await expect(page.locator('.stream-status--error')).toBeVisible()
  await expect(cards).toHaveCount(12)
  const retryPromise = page.waitForResponse(feedResponse('following', false))
  await page.getByRole('button', { name: '重试' }).click()
  const retry = await retryPromise
  await rec.record('following_retry_reload', retry)
  expect(retry.status()).toBe(200)
  expect(new URL(retry.url()).searchParams.has('cursor')).toBe(false)
  await expect(page.locator('.stream-status--error')).toHaveCount(0)
  await expect(cards).toHaveCount(12)
  const topAfterRetry = await page.locator('.short-feed').evaluate((element) => element.scrollTop)
  expect(topAfterRetry).toBe(0)
  rec.mark('retry_scroll_top', String(topAfterRetry))
  rec.flush()
})

// 4 真实 401 刷新成功：无效 access token + 真实 refresh token → 401 → 真实刷新 → 自动重试成功
test('真实 401 后刷新会话成功并重试', async ({ page }, testInfo) => {
  const rec = recorder(testInfo)
  rec.watch(page)
  mediaRoutes(page)
  // 并行 waitForResponse 会命中同一首个响应，且 401 交换可能在 goto 的 load 事件前完成：
  // 监听必须在导航与触发动作之前注册，按序收集响应
  const followingResponses: Response[] = []
  page.on('response', (response) => {
    if (feedResponse('following', false)(response)) {
      followingResponses.push(response)
    }
  })
  const refreshResponses: Response[] = []
  page.on('response', (response) => {
    if (refreshResponse()(response)) {
      refreshResponses.push(response)
    }
  })
  await uiLogin(page, viewerName)
  await page.evaluate(() => {
    const raw = localStorage.getItem('gofeed.auth.session')
    if (!raw) {
      throw new Error('会话不存在')
    }
    const session = JSON.parse(raw)
    session.access_token = ['scenarios', 'invalid', 'access'].join('-')
    localStorage.setItem('gofeed.auth.session', JSON.stringify(session))
  })
  await page.goto('/?scene=following')
  await expect.poll(() => followingResponses.length, { timeout: 15000 }).toBeGreaterThanOrEqual(1)
  const unauthorized = followingResponses[0]
  await rec.record('following_401', unauthorized)
  expect(unauthorized.status()).toBe(401)
  await expect.poll(() => refreshResponses.length, { timeout: 15000 }).toBeGreaterThanOrEqual(1)
  const refreshed = refreshResponses[0]
  await rec.record('refresh_session', refreshed)
  expect(refreshed.status()).toBe(200)
  await expect.poll(() => followingResponses.length, { timeout: 15000 }).toBeGreaterThanOrEqual(2)
  const retried = followingResponses[1]
  await rec.record('following_after_refresh', retried)
  expect(retried.status()).toBe(200)
  await expect(page.locator('.account-state__name')).toHaveText(viewerName)
  await expect(page.locator('.short-video')).toHaveCount(12)
  rec.flush()
})

// 5 真实 401 刷新失败：access 与 refresh token 均无效 → 刷新 401 → 会话清除并展示登录入口
test('真实 401 后刷新失败展示登录入口', async ({ page }, testInfo) => {
  const rec = recorder(testInfo)
  rec.watch(page)
  mediaRoutes(page)
  await uiLogin(page, viewerName)
  await page.evaluate(() => {
    const raw = localStorage.getItem('gofeed.auth.session')
    if (!raw) {
      throw new Error('会话不存在')
    }
    const session = JSON.parse(raw)
    session.access_token = ['scenarios', 'invalid', 'access'].join('-')
    session.refresh_token = ['scenarios', 'invalid', 'refresh'].join('-')
    localStorage.setItem('gofeed.auth.session', JSON.stringify(session))
  })
  // 401 交换可能在 goto 的 load 事件前完成，监听必须先于导航注册
  const followingResponses: Response[] = []
  page.on('response', (response) => {
    if (feedResponse('following', false)(response)) {
      followingResponses.push(response)
    }
  })
  const refreshResponses: Response[] = []
  page.on('response', (response) => {
    if (refreshResponse()(response)) {
      refreshResponses.push(response)
    }
  })
  await page.goto('/?scene=following')
  await expect.poll(() => followingResponses.length, { timeout: 15000 }).toBeGreaterThanOrEqual(1)
  const unauthorized = followingResponses[0]
  await rec.record('following_401', unauthorized)
  expect(unauthorized.status()).toBe(401)
  await expect.poll(() => refreshResponses.length, { timeout: 15000 }).toBeGreaterThanOrEqual(1)
  const refreshed = refreshResponses[0]
  await rec.record('refresh_failed', refreshed)
  expect(refreshed.status()).toBe(401)
  await expect(page.locator('.feed-signin-link')).toBeVisible()
  await expect(page.locator('.account-state__name')).toHaveText('未登录')
  rec.flush()
})
`

// followingScenarioEntry 保存单个场景用例的响应记录或标记
type followingScenarioEntry struct {
	Label  string `json:"label"`
	URL    string `json:"url"`
	Status int    `json:"status"`
}

// followingScenarioRecord 保存单个场景用例在单个浏览器项目下的完整记录
type followingScenarioRecord struct {
	Title       string                   `json:"title"`
	Project     string                   `json:"project"`
	Entries     []followingScenarioEntry `json:"entries"`
	APIRequests []followingLiveRequest   `json:"apiRequests"`
}

// 测试目标：为前端衔接场景启动隔离 Vite 并逐个浏览器项目执行场景 spec
// 预期效果：每个项目开始前确认初始关注集合，结束无论成败恢复夹具并保存脱敏证据
func runFollowingScenariosBrowser(t *testing.T, fixture followingBrowserFixture, apiURL string, onProject func(t *testing.T, project string, records []followingScenarioRecord)) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal("真实浏览器验收需要当前进程 PATH 中的 Node")
	}
	frontend := followingFrontendDir(t)
	absFrontend, err := filepath.Abs(frontend)
	if err != nil {
		t.Fatal(err)
	}
	webURL, portArg := followingFreeWebURL(t)
	artifacts := followingArtifactsDir(t, followingArtifactsRoot(t), "scenarios")
	specDir := t.TempDir()
	specPath := filepath.Join(specDir, "following-scenarios.spec.ts")
	if err := os.WriteFile(specPath, []byte(followingScenariosSpecTS), 0o600); err != nil {
		t.Fatal(err)
	}
	browserEnvBase := timelineBrowserEnv(map[string]string{
		"GOFEED_TIMELINE_API": apiURL,
		"NODE_PATH":           filepath.Join(absFrontend, "node_modules"),
	})
	startFollowingVite(t, frontend, browserEnvBase, artifacts, portArg, webURL, apiURL)
	for _, project := range []string{"chromium", "Mobile Chrome"} {
		t.Run(project, func(t *testing.T) {
			projectDir := filepath.Join(artifacts, project)
			resultsDir := filepath.Join(projectDir, "results")
			if err := os.MkdirAll(resultsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			// 项目开始前确认初始关注集合，集合不符时直接失败，浏览器不会继承被污染的状态
			if err := ensureFollowingFixtureRelations(fixture); err != nil {
				t.Fatalf("浏览器项目 %s 开始前关注集合不在初始状态: %v", project, err)
			}
			// 项目结束无论成败都恢复初始关注集合；恢复失败用 Error 报告，不静默吞掉
			t.Cleanup(func() {
				if err := ensureFollowingFixtureRelations(fixture); err != nil {
					t.Errorf("浏览器项目 %s 结束后恢复初始关注集合失败: %v", project, err)
				} else {
					t.Logf("浏览器项目 %s 结束后已恢复初始关注集合", project)
				}
			})
			browserEnv := append([]string(nil), browserEnvBase...)
			browserEnv = append(browserEnv, "GOFEED_FOLLOWING_RESULTS="+resultsDir)
			records := runFollowingScenariosProject(t, frontend, specDir, projectDir, project, webURL, browserEnv)
			if onProject != nil {
				onProject(t, project, records)
			}
		})
	}
}

// 测试目标：为单个浏览器项目执行场景 spec 并回传全部场景记录
// 预期效果：复用统一的命令执行与证据保存路径，按用例标题整理记录
func runFollowingScenariosProject(t *testing.T, frontend, specDir, projectDir, project, webURL string, env []string) []followingScenarioRecord {
	t.Helper()
	outputDir := filepath.Join(projectDir, "playwright-output")
	configPath := filepath.Join(projectDir, "playwright.following-scenarios.config.ts")
	if err := os.WriteFile(configPath, []byte(followingPlaywrightConfig(specDir, outputDir, webURL, "following-scenarios.spec.ts")), 0o600); err != nil {
		t.Fatal(err)
	}
	executeFollowingPlaywright(t, frontend, configPath, project, projectDir, env)
	matches, err := filepath.Glob(filepath.Join(projectDir, "results", "*.json"))
	if err != nil {
		t.Fatalf("枚举场景结果失败: %v", err)
	}
	records := make([]followingScenarioRecord, 0, len(matches))
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取场景结果失败: %v", err)
		}
		var record followingScenarioRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatalf("解析场景结果失败: %v", err)
		}
		records = append(records, record)
	}
	return records
}

// 测试目标：核对每个场景的响应序列、状态与认证头标志
// 预期效果：登录回跳、切换归零、400 首屏恢复、401 刷新成功与失败全部留有真实后端证据
func assertFollowingScenarioRecords(t *testing.T, project string, records []followingScenarioRecord) {
	t.Helper()
	if len(records) != 5 {
		t.Fatalf("%s 场景记录数=%d want=5 titles=%v", project, len(records), recordTitles(records))
	}
	byTitle := make(map[string]followingScenarioRecord, len(records))
	for _, record := range records {
		byTitle[record.Title] = record
	}
	scenarioStatus := func(title, label string, want int) {
		t.Helper()
		record, ok := byTitle[title]
		if !ok {
			t.Fatalf("%s 缺少场景记录 %s", project, title)
		}
		for _, entry := range record.Entries {
			if entry.Label == label {
				if entry.Status != want {
					t.Fatalf("%s %s %s 状态=%d want=%d", project, title, label, entry.Status, want)
				}
				return
			}
		}
		t.Fatalf("%s %s 缺少记录 %s", project, title, label)
	}
	scenarioStatus("未登录关注页登录后返回关注场景", "following_after_login", 200)
	scenarioStatus("多卡片滚动后切换场景归零", "timeline_second", 200)
	scenarioStatus("多卡片滚动后切换场景归零", "following_first", 200)
	scenarioStatus("分页 400 后重试归零重载首屏", "following_tampered_400", 400)
	scenarioStatus("分页 400 后重试归零重载首屏", "following_retry_reload", 200)
	scenarioStatus("真实 401 后刷新会话成功并重试", "following_401", 401)
	scenarioStatus("真实 401 后刷新会话成功并重试", "refresh_session", 200)
	scenarioStatus("真实 401 后刷新会话成功并重试", "following_after_refresh", 200)
	scenarioStatus("真实 401 后刷新失败展示登录入口", "following_401", 401)
	scenarioStatus("真实 401 后刷新失败展示登录入口", "refresh_failed", 401)
	for _, record := range records {
		if record.Title != "分页 400 后重试归零重载首屏" {
			continue
		}
		for _, entry := range record.Entries {
			if entry.Label == "following_retry_reload" && strings.Contains(entry.URL, "cursor=") {
				t.Fatalf("%s 重试首屏不应携带游标: %s", project, entry.URL)
			}
		}
	}
	for _, record := range records {
		for _, request := range record.APIRequests {
			switch {
			case strings.Contains(request.URL, "scene=following"):
				if !request.Auth {
					t.Fatalf("%s %s 关注请求缺少 Authorization: %s", project, record.Title, request.URL)
				}
			case strings.Contains(request.URL, "scene=timeline"):
				if request.Auth {
					t.Fatalf("%s %s Timeline 请求不应携带 Authorization: %s", project, record.Title, request.URL)
				}
			case strings.Contains(request.URL, "/api/user/refresh"):
				if request.Auth {
					t.Fatalf("%s %s 刷新请求不应携带 Authorization: %s", project, record.Title, request.URL)
				}
			}
		}
	}
}

func recordTitles(records []followingScenarioRecord) []string {
	titles := make([]string, 0, len(records))
	for _, record := range records {
		titles = append(titles, record.Title)
	}
	return titles
}

// 测试目标：真实浏览器验证前端冻结源码的四项衔接场景
// 预期效果：登录回跳、多卡片切换归零、400 游标失效首屏恢复、真实 401 刷新成功与失败全部通过
func TestFollowingBrowserLiveFrontendScenarios(t *testing.T) {
	if os.Getenv("GOFEED_FOLLOWING_BROWSER") != "1" {
		t.Skip("设置 GOFEED_FOLLOWING_BROWSER=1 显式运行隔离真实浏览器联调场景")
	}
	env := newFeedTestEnv(t)
	uncached, client := env.newServer(t, nil)
	fixture := setupFollowingBrowserFixture(t, env, uncached.URL, client)
	runFollowingScenariosBrowser(t, fixture, uncached.URL, func(t *testing.T, project string, records []followingScenarioRecord) {
		assertFollowingScenarioRecords(t, project, records)
	})
}

// 测试目标：逐条比较两套装配的浏览器记录并输出首个差异细节
// 预期效果：失败时日志能定位差异条目的字段与正文片段
func assertSameFollowingResults(t *testing.T, project string, baseline, current followingLiveResult) {
	t.Helper()
	if len(baseline.Entries) != len(current.Entries) {
		t.Fatalf("%s 条目数=%d 基线=%d", project, len(current.Entries), len(baseline.Entries))
	}
	for index, want := range baseline.Entries {
		got := current.Entries[index]
		if want == got {
			continue
		}
		t.Fatalf("%s 第 %d 条记录不同 label=%s\nurl: %s vs %s\nstatus: %d vs %d\ncache-control: %q vs %q\nvary: %q vs %q\nbody 长度: %d vs %d\nbody 片段: %s vs %s",
			project, index, want.Label, want.URL, got.URL, want.Status, got.Status,
			want.CacheControl, got.CacheControl, want.Vary, got.Vary,
			len(want.Body), len(got.Body), truncateForLog(want.Body), truncateForLog(got.Body))
	}
	if len(baseline.Requests) != len(current.Requests) {
		limit := len(baseline.Requests)
		if len(current.Requests) < limit {
			limit = len(current.Requests)
		}
		for index := 0; index < limit; index++ {
			if baseline.Requests[index] != current.Requests[index] {
				t.Errorf("%s 请求序列在 %d 处分叉:", project, index)
				for offset := -2; offset <= 2; offset++ {
					position := index + offset
					if position < 0 || position >= limit {
						continue
					}
					t.Errorf("  [%d] 基线=%s(auth=%v) 当前=%s(auth=%v)", position,
						baseline.Requests[position].URL, baseline.Requests[position].Auth,
						current.Requests[position].URL, current.Requests[position].Auth)
				}
				break
			}
		}
		t.Fatalf("%s 请求数=%d 基线=%d", project, len(current.Requests), len(baseline.Requests))
	}
	for index, want := range baseline.Requests {
		if want != current.Requests[index] {
			t.Fatalf("%s 第 %d 个请求不同: %s(auth=%v) vs %s(auth=%v)",
				project, index, want.URL, want.Auth, current.Requests[index].URL, current.Requests[index].Auth)
		}
	}
}

func truncateForLog(body string) string {
	if len(body) > 300 {
		return body[:300]
	}
	return body
}

type timelineCacheEvents struct {
	mu     sync.Mutex
	event  string
	counts map[string]int
}

// 测试目标：采集生产 Feed 应用层的缓存事件
// 预期效果：用真实命中、回源和回填观测证明缓存参与而非比较相同响应
func (e *timelineCacheEvents) Write(data []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, line := range strings.Split(string(data), "\n") {
		event := e.event
		if event == "" {
			event = "feed_page_cache"
		}
		if !strings.Contains(line, "event="+event+" ") {
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
	cardEvents := &timelineCacheEvents{event: "feed_card_cache", counts: make(map[string]int)}
	originalOutput := log.Writer()
	log.SetOutput(io.MultiWriter(originalOutput, events, cardEvents))
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
	cardCache, recorder := env.liveCardCache(t)
	both, _ := env.newServer(t, env.livePageCache(t), cardCache)
	t.Run("page_and_card_enabled", func(t *testing.T) {
		if len(cardEvents.snapshot()) != 0 {
			t.Fatal("先前关闭卡片缓存时出现访问")
		}
		bodies := runTimelineBrowser(t, both.URL, ids)
		for project, body := range bodies {
			if body != baseline[project] {
				t.Errorf("卡片缓存 %s JSON 变化", project)
			}
		}
		counts := cardEvents.snapshot()
		for result, want := range map[string]int{"miss": 1, "mysql_read": 1, "write_ok": 1, "hit": 3} {
			if counts[result] != want {
				t.Errorf("卡片事件 %s got=%d want=%d events=%v", result, counts[result], want, counts)
			}
		}
		if recorder.reads != 4 || recorder.writes != 1 {
			t.Fatalf("卡片脚本 reads=%d writes=%d", recorder.reads, recorder.writes)
		}
		for _, key := range recorder.base.writes() {
			if !recorder.base.keyExists(t, key) {
				t.Fatal("真实卡片键不存在")
			}
		}
		t.Logf("实际 Feed 卡片缓存观测: %v; Redis batch reads=4 writes=1", counts)
	})
}
