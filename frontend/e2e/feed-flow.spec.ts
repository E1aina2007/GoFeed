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

// 用登录态验证「已登录访客」的 Feed 首屏也能正常渲染与分页。
// 同时 mock 掉 LikeButton 的点赞状态请求：本机 8080 后端若在运行，
// 未 mock 的认证请求会以假令牌得到 401，触发会话刷新失败并清空登录态，干扰用例
async function signIn(page: Page) {
  await page.addInitScript((value) => {
    window.localStorage.setItem('gofeed.auth.session', JSON.stringify(value))
  }, session)
  await page.route('**/api/video/auth/*/like', async (route) => {
    if (route.request().method() !== 'GET') {
      await route.fulfill({ status: 405, contentType: 'application/json', body: '{}' })
      return
    }
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ liked: false, likes_count: 0 }),
    })
  })
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

test('clears the invalidated cursor after a page error and reloads on retry', async ({ page }) => {
  const feedRequests: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/feed') {
      feedRequests.push(request.url())
    }
  })
  let firstPageLoads = 0
  await page.route(
    (url) => url.pathname === '/api/feed',
    async (route) => {
      const url = new URL(route.request().url())
      if (url.searchParams.has('cursor')) {
        // 400 表示游标已被服务端判定失效，客户端必须清空它
        await route.fulfill({
          status: 400,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'invalid cursor' }),
        })
        return
      }
      firstPageLoads += 1
      const body = firstPageLoads === 1
        ? feedBody([firstVideo], 'page-2')
        : feedBody([{ ...firstVideo, title: '重新加载的首屏视频' }])
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
    },
  )

  await page.goto('/')
  const feed = page.getByRole('main', { name: '最新视频' })
  await expect(feed.getByRole('link', { name: '首屏视频' })).toBeVisible()

  await scrollFeedToBottom(page)

  const errorStatus = feed.locator('.stream-status--error')
  await expect(errorStatus).toContainText('分页状态已失效')
  const sentPageRequests = () =>
    feedRequests.filter((request) => new URL(request).searchParams.get('cursor') !== null)
  expect(sentPageRequests()).toHaveLength(1)

  // 游标已清空：继续滚动不得再产生分页请求
  await scrollFeedToBottom(page)
  await expect(errorStatus).toBeVisible()
  expect(sentPageRequests()).toHaveLength(1)

  await errorStatus.getByRole('button', { name: '重试' }).click()

  // 重试重新加载首屏，而不是复用失效游标
  await expect(feed.getByRole('link', { name: '重新加载的首屏视频' })).toBeVisible()
  expect(sentPageRequests()).toHaveLength(1)
  expect(firstPageLoads).toBe(2)
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

test.describe('Following 场景', () => {
  test('shows the sign-in entry without requesting the following scene when signed out', async ({
    page,
  }) => {
    const requests = trackFeedRequests(page)
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
    const feed = page.getByRole('main', { name: '最新视频' })
    await expect(feed.getByRole('link', { name: '首屏视频' })).toBeVisible()

    await page.getByRole('button', { name: '关注', exact: true }).click()

    const signin = page.locator('section.feed-message[role="status"]')
    await expect(signin).toContainText('登录后查看关注作者的最新视频')
    await expect(signin.getByRole('link', { name: '登录' })).toBeVisible()
    // 只有 Timeline 首屏请求，未登录不发 Following 请求
    expect(requests).toHaveLength(1)
  })

  test('loads the following scene with the session and reuses the cached timeline', async ({
    page,
  }) => {
    const feedRequests: string[] = []
    const followingAuthHeaders: string[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.pathname !== '/api/feed') {
        return
      }
      feedRequests.push(request.url())
      if (url.searchParams.get('scene') === 'following') {
        followingAuthHeaders.push(request.headers().authorization ?? '')
      }
    })
    await page.route(
      (url) => url.pathname === '/api/feed',
      async (route) => {
        const url = new URL(route.request().url())
        if (url.searchParams.get('scene') === 'following') {
          await route.fulfill({
            contentType: 'application/json',
            body: JSON.stringify(
              feedBody([
                {
                  ...firstVideo,
                  id: 21,
                  title: '关注作者的视频',
                  author: { id: 21, username: 'followed-author' },
                },
              ]),
            ),
          })
          return
        }
        await route.fulfill({
          contentType: 'application/json',
          body: JSON.stringify(feedBody([firstVideo])),
        })
      },
    )

    await signIn(page)
    await page.goto('/')
    const feed = page.getByRole('main', { name: '最新视频' })
    await expect(feed.getByRole('link', { name: '首屏视频' })).toBeVisible()
    expect(feedRequests).toHaveLength(1)

    await page.getByRole('button', { name: '关注', exact: true }).click()
    await expect(feed.getByRole('link', { name: '关注作者的视频' })).toBeVisible()
    expect(feedRequests.map((request) => new URL(request).search)).toEqual([
      '?scene=timeline&limit=12',
      '?scene=following&limit=12',
    ])
    // Following 请求必须携带会话 Bearer 凭据
    expect(followingAuthHeaders).toEqual(['Bearer e2e-access-token'])

    // 切回最新：复用已加载的 Timeline，不重新请求
    await page.getByRole('button', { name: '最新', exact: true }).click()
    await expect(feed.getByRole('link', { name: '首屏视频' })).toBeVisible()
    expect(feedRequests).toHaveLength(2)
  })

  test('renders the following empty state', async ({ page }) => {
    await signIn(page)
    await page.route(
      (url) => url.pathname === '/api/feed',
      async (route) => {
        const url = new URL(route.request().url())
        const body = url.searchParams.get('scene') === 'following' ? feedBody([]) : feedBody([firstVideo])
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
      },
    )

    await page.goto('/')
    await page.getByRole('button', { name: '关注', exact: true }).click()

    const emptySection = page.locator('section.feed-message[role="alert"]')
    await expect(emptySection).toContainText('还没有可看的关注视频')
    await expect(emptySection.getByRole('button')).toHaveCount(0)
  })

  test('recovers the following scene through the retry button after a transient failure', async ({
    page,
  }) => {
    await signIn(page)
    let followingAttempts = 0
    await page.route(
      (url) => url.pathname === '/api/feed',
      async (route) => {
        const url = new URL(route.request().url())
        if (url.searchParams.get('scene') !== 'following') {
          await route.fulfill({
            contentType: 'application/json',
            body: JSON.stringify(feedBody([firstVideo])),
          })
          return
        }
        followingAttempts += 1
        if (followingAttempts <= 3) {
          // 三次有界自动重试全部失败，才能观察到错误态与手动重试
          await route.fulfill({
            status: 503,
            contentType: 'application/json',
            body: JSON.stringify({ error: 'feed temporarily unavailable' }),
          })
          return
        }
        await route.fulfill({
          contentType: 'application/json',
          body: JSON.stringify(
            feedBody([{ ...firstVideo, id: 21, title: '关注作者的视频' }]),
          ),
        })
      },
    )

    await page.goto('/')
    await page.getByRole('button', { name: '关注', exact: true }).click()

    const errorSection = page.locator('section.feed-message[role="alert"]')
    await expect(errorSection).toContainText('服务暂时不可用')
    await errorSection.getByRole('button', { name: '重试' }).click()

    await expect(
      page
        .getByRole('main', { name: '最新视频' })
        .getByRole('link', { name: '关注作者的视频' }),
    ).toBeVisible()
    expect(followingAttempts).toBe(4)
  })

  test('clears an invalid following cursor after a 400 and reloads on retry', async ({ page }) => {
    await signIn(page)
    const feedRequests: string[] = []
    page.on('request', (request) => {
      if (new URL(request.url()).pathname === '/api/feed') {
        feedRequests.push(request.url())
      }
    })
    let firstPageLoads = 0
    await page.route(
      (url) => url.pathname === '/api/feed',
      async (route) => {
        const url = new URL(route.request().url())
        if (url.searchParams.get('scene') !== 'following') {
          await route.fulfill({
            contentType: 'application/json',
            body: JSON.stringify(feedBody([firstVideo])),
          })
          return
        }
        if (url.searchParams.has('cursor')) {
          await route.fulfill({
            status: 400,
            contentType: 'application/json',
            body: JSON.stringify({ error: 'invalid feed cursor' }),
          })
          return
        }
        firstPageLoads += 1
        const body = firstPageLoads === 1
          ? feedBody([{ ...firstVideo, id: 21, title: '关注首屏视频' }], 'following-page-2')
          : feedBody([{ ...firstVideo, id: 23, title: '重新加载的关注首屏' }])
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
      },
    )

    await page.goto('/')
    const feed = page.getByRole('main', { name: '最新视频' })
    await page.getByRole('button', { name: '关注', exact: true }).click()
    await expect(feed.getByRole('link', { name: '关注首屏视频' })).toBeVisible()

    await scrollFeedToBottom(page)
    const errorStatus = feed.locator('.stream-status--error')
    await expect(errorStatus).toContainText('分页状态已失效')
    const followingPagedRequests = () =>
      feedRequests.filter((request) => {
        const url = new URL(request)
        return url.searchParams.get('scene') === 'following' && url.searchParams.has('cursor')
      })
    expect(followingPagedRequests()).toHaveLength(1)

    // 游标已清空：继续滚动不得再产生 Following 分页请求
    await scrollFeedToBottom(page)
    await expect(errorStatus).toBeVisible()
    expect(followingPagedRequests()).toHaveLength(1)

    await errorStatus.getByRole('button', { name: '重试' }).click()

    await expect(feed.getByRole('link', { name: '重新加载的关注首屏' })).toBeVisible()
    expect(followingPagedRequests()).toHaveLength(1)
  })
})

// 按前缀批量生成卡片：滚动重置与分页用例需要足够多且可滚动的内容
function sceneCards(prefix: string, count: number, startID = 1) {
  return Array.from({ length: count }, (_, index) => ({
    ...firstVideo,
    id: startID + index,
    title: `${prefix} ${index + 1}`,
    play_url: `/static/videos/${startID + index}/play.mp4`,
    cover_url: `/static/covers/${startID + index}/cover.jpg`,
    author: { id: startID + index, username: `${prefix}-author-${index + 1}` },
  }))
}

test.describe('场景 URL 与滚动', () => {
  test('returns to the following scene after signing in through the redirect', async ({ page }) => {
    const followingAuthHeaders: string[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.pathname === '/api/feed' && url.searchParams.get('scene') === 'following') {
        followingAuthHeaders.push(request.headers().authorization ?? '')
      }
    })
    await page.route('**/api/user/login', async (route) => {
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify(session) })
    })
    // 登录后卡片渲染 LikeButton，同样需要隔离真实后端的点赞状态请求
    await page.route('**/api/video/auth/*/like', async (route) => {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ liked: false, likes_count: 0 }),
      })
    })
    await page.route(
      (url) => url.pathname === '/api/feed',
      async (route) => {
        const url = new URL(route.request().url())
        const body = url.searchParams.get('scene') === 'following'
          ? feedBody([{ ...firstVideo, id: 21, title: '关注作者的视频' }])
          : feedBody([firstVideo])
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
      },
    )

    await page.goto('/')
    const feed = page.getByRole('main', { name: '最新视频' })
    await expect(feed.getByRole('link', { name: '首屏视频' })).toBeVisible()

    // 未登录切换到 Following：登录入口的 redirect 必须携带 scene=following
    await page.getByRole('button', { name: '关注', exact: true }).click()
    const signin = page.locator('section.feed-message[role="status"]')
    await expect(signin).toContainText('登录后查看关注作者的最新视频')
    await expect(signin.getByRole('link', { name: '登录' })).toHaveAttribute(
      'href',
      '/login?redirect=/?scene=following',
    )

    await signin.getByRole('link', { name: '登录' }).click()
    await expect(page).toHaveURL(/\/login\?redirect=/)
    await page.getByLabel('用户名', { exact: true }).fill('e2e-user')
    await page.getByLabel('密码', { exact: true }).fill('password-123')
    await page.getByRole('button', { name: '登录', exact: true }).click()

    // 登录成功回到 /?scene=following：自动进入 Following 并携带 Bearer 发起认证首屏
    await expect(page).toHaveURL(/\?scene=following$/)
    await expect(feed.getByRole('link', { name: '关注作者的视频' })).toBeVisible()
    expect(followingAuthHeaders).toEqual(['Bearer e2e-access-token'])
  })

  test('shows the sign-in entry without any feed request when opening the scene URL signed out', async ({
    page,
  }) => {
    const requests = trackFeedRequests(page)
    await page.route(
      (url) => url.pathname === '/api/feed',
      async (route) => {
        await route.fulfill({
          contentType: 'application/json',
          body: JSON.stringify(feedBody([firstVideo])),
        })
      },
    )

    await page.goto('/?scene=following')

    const signin = page.locator('section.feed-message[role="status"]')
    await expect(signin).toContainText('登录后查看关注作者的最新视频')
    // 未登录直达场景 URL：不发任何 Feed 请求
    expect(requests).toHaveLength(0)
  })

  test('returns the feed to the top when switching between scenes', async ({ page }) => {
    const requests = trackFeedRequests(page)
    await page.route(
      (url) => url.pathname === '/api/feed',
      async (route) => {
        const url = new URL(route.request().url())
        const body = url.searchParams.get('scene') === 'following'
          ? feedBody(sceneCards('关注视频', 4))
          : feedBody(sceneCards('时间线视频', 6), 'page-2')
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
      },
    )

    await signIn(page)
    await page.goto('/')
    const feed = page.getByRole('main', { name: '最新视频' })
    await expect(feed.getByRole('link', { name: '时间线视频 1' })).toBeVisible()

    // 滚到 Timeline 中部：第三张卡片顶部恰好是 scroll-snap 吸附点
    const scrollToMiddle = () =>
      feed.evaluate((element) => {
        element.scrollTop = element.clientHeight * 2
        element.dispatchEvent(new Event('scroll'))
      })
    await scrollToMiddle()
    await expect.poll(() => feed.evaluate((element) => element.scrollTop)).toBeGreaterThan(0)

    // 切换到 Following：容器回到顶部，第一张关注卡片可见，且只发两个首屏请求
    await page.getByRole('button', { name: '关注', exact: true }).click()
    await expect(feed.getByRole('link', { name: '关注视频 1' })).toBeVisible()
    await expect.poll(() => feed.evaluate((element) => element.scrollTop)).toBe(0)
    expect(requests.map((request) => new URL(request).search)).toEqual([
      '?scene=timeline&limit=12',
      '?scene=following&limit=12',
    ])

    // Following 滚到中部后切回 Timeline：同样回到顶部且使用缓存不重新请求
    await scrollToMiddle()
    await page.getByRole('button', { name: '最新', exact: true }).click()
    await expect(feed.getByRole('link', { name: '时间线视频 1' })).toBeVisible()
    await expect.poll(() => feed.evaluate((element) => element.scrollTop)).toBe(0)
    expect(requests).toHaveLength(2)
  })

  test('returns to the top of the first screen when retrying after a pagination 400', async ({
    page,
  }) => {
    const feedRequests: string[] = []
    page.on('request', (request) => {
      if (new URL(request.url()).pathname === '/api/feed') {
        feedRequests.push(request.url())
      }
    })
    let firstPageLoads = 0
    await page.route(
      (url) => url.pathname === '/api/feed',
      async (route) => {
        const url = new URL(route.request().url())
        if (url.searchParams.has('cursor')) {
          await route.fulfill({
            status: 400,
            contentType: 'application/json',
            body: JSON.stringify({ error: 'invalid cursor' }),
          })
          return
        }
        firstPageLoads += 1
        const body = firstPageLoads === 1
          ? feedBody(sceneCards('时间线视频', 3), 'page-2')
          : feedBody([{ ...sceneCards('时间线视频', 3)[0]!, title: '重新加载的首屏视频' }])
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
      },
    )

    await page.goto('/')
    const feed = page.getByRole('main', { name: '最新视频' })
    await expect(feed.getByRole('link', { name: '时间线视频 1' })).toBeVisible()

    await scrollFeedToBottom(page)
    const errorStatus = feed.locator('.stream-status--error')
    await expect(errorStatus).toContainText('分页状态已失效')

    await errorStatus.getByRole('button', { name: '重试' }).click()

    // 重试清空旧分页位置：请求不带旧游标，首屏第一张卡片在顶部可见
    await expect(feed.getByRole('link', { name: '重新加载的首屏视频' })).toBeVisible()
    await expect.poll(() => feed.evaluate((element) => element.scrollTop)).toBe(0)
    const pagedRequests = feedRequests.filter((request) =>
      new URL(request).searchParams.has('cursor'),
    )
    expect(pagedRequests).toHaveLength(1)
    expect(firstPageLoads).toBe(2)
  })

  test('keeps the scroll position across a normal pagination append', async ({ page }) => {
    await page.route(
      (url) => url.pathname === '/api/feed',
      async (route) => {
        const url = new URL(route.request().url())
        const body = url.searchParams.has('cursor')
          ? feedBody(sceneCards('第二页视频', 4, 100))
          : feedBody(sceneCards('时间线视频', 6), 'page-2')
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
      },
    )

    await page.goto('/')
    const feed = page.getByRole('main', { name: '最新视频' })
    await expect(feed.getByRole('link', { name: '时间线视频 1' })).toBeVisible()

    await scrollFeedToBottom(page)
    await expect(feed.getByRole('link', { name: '第二页视频 1' })).toBeVisible()

    // 正常追加分页停留在原位置，不跳回顶部
    await expect.poll(() => feed.evaluate((element) => element.scrollTop)).toBeGreaterThan(0)
    await expect(feed.getByRole('link', { name: '时间线视频 6' })).toBeVisible()
  })
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

  test('pauses the timeline player when switching to the following scene', async ({ page }) => {
    await page.route('**/static/videos/**', async (route) => {
      await route.fulfill({ contentType: 'video/webm', path: playableVideo })
    })
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

    const pauseState = () =>
      player.evaluate((element) => (element as HTMLVideoElement).paused)
    await expect.poll(pauseState, { timeout: 10_000 }).toBe(false)

    // 切换后旧场景播放器会随列表卸载：先在页面里保留元素引用再切换
    await page.evaluate(() => {
      ;(window as unknown as { __scenePlayer?: Element }).__scenePlayer = document.querySelector(
        '.short-video__player',
      )
    })

    // 未登录直接切换：切换动作本身必须先暂停旧场景的播放器
    await page.getByRole('button', { name: '关注', exact: true }).click()
    const paused = await page.evaluate(
      () => (window as unknown as { __scenePlayer?: HTMLVideoElement }).__scenePlayer?.paused,
    )
    expect(paused).toBe(true)
  })
})
