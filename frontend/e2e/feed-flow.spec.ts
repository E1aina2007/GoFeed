import { resolve } from 'node:path'

import { expect, test, type Page } from '@playwright/test'

// 公开 Feed 的浏览器行为回归（公共 Feed API 全部由路由 mock，不触碰真实后端）
const firstVideo = {
  id: 7,
  title: '首屏视频',
  description: '第一条公开视频',
  play_url: '/static/videos/7/first.mp4',
  play_file_name: 'first.mp4',
  play_original_name: 'first.mp4',
  cover_url: '/static/covers/7/first.jpg',
  cover_file_name: 'first.jpg',
  cover_original_name: 'first.jpg',
  published_at: '2026-08-23T08:00:00Z',
  likes_count: 0,
  comments_count: 0,
  author: { id: 7, username: 'first-author' },
}

const secondVideo = {
  ...firstVideo,
  id: 8,
  title: '第二页视频',
  play_url: '/static/videos/8/second.mp4',
  cover_url: '/static/covers/8/second.jpg',
  author: { id: 8, username: 'second-author' },
}

const session = {
  access_token: 'e2e-access-token',
  refresh_token: 'e2e-refresh-token',
  expires_at: '2027-01-01T00:00:00Z',
  user: { id: 42, username: 'e2e-user' },
}

// 用登录态验证「已登录访客」的 Feed 首屏也能正常渲染与分页
async function signIn(page: Page) {
  await page.addInitScript((value) => {
    window.localStorage.setItem('gofeed.auth.session', JSON.stringify(value))
  }, session)
}

function feedBody(items: unknown[], nextCursor?: string) {
  return { items, ...(nextCursor ? { next_cursor: nextCursor } : {}) }
}

// 统计公共 Feed 请求，用于断言「不该再发请求」的行为
function trackFeedRequests(page: Page) {
  const requests: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/feed') {
      requests.push(request.url())
    }
  })
  return requests
}

// 滚到 Feed 底部触发分页；等一帧后再补发一次 scroll 事件，覆盖移动端 scroll-snap
// （mandatory 吸附会把一次 scrollTo 弹回原位）与「卡片铺满视口后浏览器自动吸附」两种情况
async function scrollFeedToBottom(page: Page) {
  const feed = page.getByRole('main', { name: '最新视频' })
  await feed.evaluate((element) => {
    element.scrollTo({ top: element.scrollHeight })
    element.dispatchEvent(new Event('scroll'))
  })
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => resolve(null))))
  await feed.evaluate((element) => {
    element.dispatchEvent(new Event('scroll'))
  })
}

// 极小的可播放 WebM（由 Playwright 自带 ffmpeg 生成，见 e2e/fixtures/），
// 仅用于验证「离开路由 / 页面隐藏时真的暂停播放」
const playableVideo = resolve('e2e/fixtures/playable.webm')

test('loads the second page on scroll and ignores a duplicated video', async ({ page }) => {
  const requests = trackFeedRequests(page)
  const cursorToken = 'feed-page-2+/= &?中文'
  await page.route(
    (url) => url.pathname === '/api/feed',
    async (route) => {
      const url = new URL(route.request().url())
      expect(url.searchParams.get('scene')).toBe('timeline')
      expect(url.searchParams.get('limit')).toBe('12')
      expect([...url.searchParams.keys()].sort()).toEqual(
        url.searchParams.has('cursor') ? ['cursor', 'limit', 'scene'] : ['limit', 'scene'],
      )
      const cursor = url.searchParams.get('cursor')
      const body = cursor === cursorToken
        // 第二页故意带回首页已有视频，用于验证按 ID 去重
        ? feedBody([{ ...firstVideo, title: '更新后的首屏视频' }, secondVideo])
        : feedBody([firstVideo], cursorToken)
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
    },
  )

  await page.goto('/')
  const feed = page.getByRole('main', { name: '最新视频' })
  const cards = feed.locator('.short-video')
  // 卡片恰好铺满视口时浏览器可能自动吸附并触发分页，因此不假设首屏只有一张卡
  await expect(cards).not.toHaveCount(0)
  await scrollFeedToBottom(page)

  await expect(cards).toHaveCount(2)
  expect(new URL(requests[0]!).search).toBe('?scene=timeline&limit=12')
  expect(new URL(requests[1]!).searchParams.get('cursor')).toBe(cursorToken)
  await expect(feed.getByRole('link', { name: '更新后的首屏视频' })).toBeVisible()
  await expect(feed.getByRole('link', { name: '第二页视频' })).toBeVisible()
  // 第二页与首屏有重复视频：同一游标只能请求一次，重复项必须被替换而不是新增卡片
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          performance
            .getEntriesByType('resource')
            .filter((entry) => new URL(entry.name).searchParams.has('cursor')).length,
      ),
    )
    .toBe(1)
  // 末页没有 next_cursor：滚动到底部不得再产生分页请求
  await scrollFeedToBottom(page)
  await expect(cards).toHaveCount(2)
  await expect(feed.locator('.stream-status')).toHaveText('已经到底了')
})

test('shows the end-of-feed status and never loads another page', async ({ page }) => {
  const requests = trackFeedRequests(page)
  await page.route(
    (url) => url.pathname === '/api/feed',
    async (route) => {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify(feedBody([firstVideo, secondVideo])),
      })
    },
  )

  await page.goto('/')
  const feed = page.getByRole('main', { name: '最新视频' })
  await expect(feed.locator('.short-video')).toHaveCount(2)
  await expect(feed.locator('.stream-status')).toHaveText('已经到底了')

  // 缺少 next_cursor 时滚动不得触发任何新请求
  await scrollFeedToBottom(page)
  await expect(feed.locator('.stream-status')).toHaveText('已经到底了')
  expect(requests).toHaveLength(1)
})

test('renders the error state and recovers through the retry button', async ({ page }) => {
  let attempts = 0
  await page.route(
    (url) => url.pathname === '/api/feed',
    async (route) => {
      attempts += 1
      if (attempts === 1) {
        await route.fulfill({
          status: 400,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'invalid cursor' }),
        })
        return
      }
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify(feedBody([firstVideo])),
      })
    },
  )

  await page.goto('/')

  // 400 不可重试，页面必须直接给出错误提示与重试入口，而不是停在骨架屏
  const errorSection = page.locator('section.feed-message[role="alert"]')
  await expect(errorSection).toContainText('分页状态已失效，请重新加载')
  await expect(page.locator('.short-video--skeleton')).toHaveCount(0)

  await errorSection.getByRole('button', { name: '重试' }).click()

  await expect(page.getByRole('main', { name: '最新视频' }).locator('.short-video')).toHaveCount(1)
  await expect(page.getByRole('link', { name: '首屏视频' })).toBeVisible()
  await expect(errorSection).toBeHidden()
  expect(attempts).toBe(2)
})

test('renders the empty state without a retry entry', async ({ page }) => {
  await page.route(
    (url) => url.pathname === '/api/feed',
    async (route) => {
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify(feedBody([])) })
    },
  )

  await page.goto('/')

  const emptySection = page.locator('section.feed-message[role="alert"]')
  await expect(emptySection).toContainText('暂时没有公开视频')
  // 空态不是失败，不应给出重试入口
  await expect(emptySection.getByRole('button')).toHaveCount(0)
  await expect(page.locator('.short-video')).toHaveCount(0)
})

test('keeps the loaded feed and retries the same cursor after a page error', async ({ page }) => {
  const feedRequests: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/feed') {
      feedRequests.push(request.url())
    }
  })
  const pagedRequests: string[] = []
  await page.route(
    (url) => url.pathname === '/api/feed',
    async (route) => {
      const cursor = new URL(route.request().url()).searchParams.get('cursor')
      if (!cursor) {
        await route.fulfill({
          contentType: 'application/json',
          body: JSON.stringify(feedBody([firstVideo], 'page-2')),
        })
        return
      }
      if (!pagedRequests.includes(cursor)) {
        pagedRequests.push(cursor)
        // 该游标的第一次请求失败，用于验证「分页失败只影响追加、且重试沿用同一游标」
        await route.fulfill({
          status: 400,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'invalid cursor' }),
        })
        return
      }
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify(feedBody([secondVideo])),
      })
    },
  )

  await page.goto('/')
  const feed = page.getByRole('main', { name: '最新视频' })
  // 卡片恰好铺满视口时浏览器可能自动吸附到底部并请求下一页，这里只要首屏渲染完成即可继续
  await expect(feed.getByRole('link', { name: '首屏视频' })).toBeVisible()

  // 造成一次分页：卡片铺满视口时浏览器可能已自动吸附到底部并请求下一页，这里滚动一次保证发生过
  await scrollFeedToBottom(page)

  const errorStatus = feed.locator('.stream-status--error')
  await expect(errorStatus).toContainText('分页状态已失效')
  const sentPageRequests = () =>
    feedRequests.filter((request) => new URL(request).searchParams.get('cursor') !== null)
  expect(sentPageRequests()).toHaveLength(1)
  await scrollFeedToBottom(page)
  await expect(errorStatus).toBeVisible()
  await expect(errorStatus.getByRole('button', { name: '重试' })).toBeInViewport()
  expect(sentPageRequests()).toHaveLength(1)
  await expect(feed.locator('.short-video')).toHaveCount(1)

  await errorStatus.getByRole('button', { name: '重试' }).click()
  await expect(feed.getByRole('link', { name: '第二页视频' })).toBeVisible({ timeout: 10_000 })
  await expect(feed.getByRole('link', { name: '首屏视频' })).toBeVisible()

  const pagedCursors = feedRequests
    .map((request) => new URL(request).searchParams.get('cursor'))
    .filter((cursor): cursor is string => cursor !== null)
  expect(pagedCursors).toHaveLength(2)
  expect(pagedCursors.every((cursor) => cursor === 'page-2')).toBe(true)
  await expect(feed.locator('.short-video')).toHaveCount(2)
  await expect(feed.locator('.stream-status--error')).toHaveCount(0)
})

test('aborts the in-flight first page when the visitor leaves the feed route', async ({ page }) => {
  // 大响应体让首屏请求保持在途，离开路由才能观察到真实取消
  const hugeBody = JSON.stringify(
    feedBody([{ ...firstVideo, description: 'x'.repeat(150_000) }], 'page-2'),
  )
  await page.route(
    (url) => url.pathname === '/api/feed',
    async (route) => {
      // 首屏请求不 fulfill（挂起），其它请求正常返回，避免路由跳转被阻塞
      if (!new URL(route.request().url()).searchParams.has('cursor')) {
        await new Promise((resolve) => setTimeout(resolve, 30_000))
        await route.fulfill({ contentType: 'application/json', body: hugeBody }).catch(() => {})
        return
      }
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify(feedBody([])) })
    },
  )

  const feedRequest = page.waitForRequest((request) => {
    const url = new URL(request.url())
    return url.pathname === '/api/feed' && !url.searchParams.has('cursor')
  })
  await page.goto('/')
  const pending = await feedRequest

  expect(pending.failure()).toBeNull()

  // 离开 Feed 路由后组件卸载，在途首屏请求必须被真正 abort
  // 各内核的取消文案不同（chromium net::ERR_ABORTED / firefox NS_BINDING_ABORTED / webkit Load request cancelled）
  await page.getByRole('link', { name: '用户' }).first().click()
  await expect(page).toHaveURL(/\/users$/)
  await expect.poll(() => pending.failure()?.errorText ?? '').toMatch(/abort|cancel/i)
})

test('renders the feed for a signed-in visitor', async ({ page }) => {
  await signIn(page)
  await page.route(
    (url) => url.pathname === '/api/feed',
    async (route) => {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify(feedBody([firstVideo, secondVideo])),
      })
    },
  )

  await page.goto('/')

  const feed = page.getByRole('main', { name: '最新视频' })
  await expect(feed.locator('.short-video')).toHaveCount(2)
  await expect(page.locator('.account-state__name')).toHaveText('e2e-user')
})

test.describe('播放状态', () => {
  test.use({ viewport: { width: 1280, height: 800 } })

  // eslint-disable-next-line playwright/no-skipped-test -- 真实媒体播放只在 chromium 验证，其它内核跳过
  test.skip(
    ({ browserName }) => browserName !== 'chromium',
    '真实媒体播放只在 chromium 验证',
  )

  test('pauses the playing video when the page becomes hidden', async ({ page }) => {
    // 用真实可播的 webm 响应媒体请求，才能观察到真正的暂停行为。
    // 必须整文件 200 返回：Playwright 的 206 分片响应下 Chromium 停在 readyState=1，媒体始终无法推进。
    await page.route('**/static/videos/**', async (route) => {
      await route.fulfill({ contentType: 'video/webm', path: playableVideo })
    })
    // 只放一条，避免第二条卡片抢视口导致首条媒体迟迟不就绪
    await page.route(
      (url) => url.pathname === '/api/feed',
      async (route) => {
        await route.fulfill({
          contentType: 'application/json',
          body: JSON.stringify(feedBody([firstVideo])),
        })
      },
    )

    await page.goto('/')
    const player = page.locator('.short-video__player').first()
    await expect(player).toBeAttached()
    await expect(player).toBeInViewport()

    // 无头 Chromium 下正在播放的元素会停在 readyState=1（currentTime 不推进），
    // 因此断言只覆盖契约本身：paused 必须随 visibilitychange 暂停 / 恢复，而不依赖媒体推进。
    const pauseState = () =>
      player.evaluate((element) => (element as HTMLVideoElement).paused)

    await expect.poll(pauseState, { timeout: 10_000 }).toBe(false)

    // 页面隐藏（切标签页/切到后台）时必须暂停播放
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => true })
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await expect.poll(pauseState).toBe(true)

    // 恢复可见后由可见性回调恢复播放
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => false })
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await expect.poll(pauseState).toBe(false)
    const playerHandle = await player.elementHandle()
    await page.getByRole('link', { name: '用户', exact: true }).first().click()
    await expect(page).toHaveURL(/\/users$/)
    expect(await playerHandle?.evaluate((element) => (element as HTMLVideoElement).paused)).toBe(true)
  })
})
