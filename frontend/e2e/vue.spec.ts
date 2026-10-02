import { expect, test, type Page } from '@playwright/test'

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

async function mockPublicFeed(page: Page) {
  await page.route((url) => url.pathname === '/api/feed', async (route) => {
    const url = new URL(route.request().url())
    const isNextPage = url.searchParams.get('cursor') === 'next-page'
    const body = isNextPage
      ? {
          items: [
            { ...firstVideo, title: '更新后的首屏视频' },
            {
              ...firstVideo,
              id: 8,
              title: '第二条视频',
              play_url: '/static/videos/8/second.mp4',
              cover_url: '/static/covers/8/second.jpg',
              author: { id: 8, username: 'second-author' },
            },
          ],
        }
      : { items: [firstVideo], next_cursor: 'next-page' }

    await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
  })
}

test('shows the mocked public feed', async ({ page }) => {
  await mockPublicFeed(page)
  await page.goto('/')

  await expect(page.locator('.app-brand:visible, .mobile-nav:visible').first()).toBeVisible()
  await expect(
    page
      .locator('.sidebar-nav__link:visible, .mobile-nav__link:visible')
      .filter({ hasText: '发现' })
      .first(),
  ).toBeVisible()
  await expect(page.getByRole('heading', { name: '最新视频' })).toBeVisible()
  await expect(page.getByRole('link', { name: '首屏视频' })).toBeVisible()
})

test('merges a paginated overlap without duplicate videos', async ({ page }) => {
  await mockPublicFeed(page)
  await page.goto('/')

  const feed = page.getByRole('main', { name: '最新视频' })
  // 首页视频恰好填满视口时，浏览器布局吸附可能先触发滚动加载；只需确认首页已渲染即可滚动
  await expect(page.getByRole('link', { name: '首屏视频' })).toBeVisible()
  await feed.evaluate((element) => {
    element.scrollTo({ top: element.scrollHeight })
    element.dispatchEvent(new Event('scroll'))
  })

  await expect(page.getByRole('link', { name: '更新后的首屏视频' })).toBeVisible()
  await expect(page.getByRole('link', { name: '第二条视频' })).toBeVisible()
  await expect(feed.locator('.short-video')).toHaveCount(2)
})

test('loads more public users with the versioned pagination contract', async ({ page }) => {
  const requests: string[] = []
  await page.route(
    (url) => url.pathname === '/api/user',
    async (route) => {
      const url = new URL(route.request().url())
      requests.push(url.search)
      const body = url.searchParams.get('cursor') === 'users-page-2'
        ? {
            users: [
              { id: 2, username: 'bob' },
              { id: 3, username: 'cora', bio: '第二页用户' },
            ],
          }
        : {
            users: [
              { id: 1, username: 'alice' },
              { id: 2, username: 'bob' },
            ],
            next_cursor: 'users-page-2',
          }
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
    },
  )
  await page.goto('/users')

  await expect(page.getByRole('heading', { name: '用户' })).toBeVisible()
  await expect(page.locator('.user-item')).toHaveCount(2)
  await page.getByRole('button', { name: '加载更多用户' }).click()

  await expect(page.getByText('@cora')).toBeVisible()
  await expect(page.locator('.user-item')).toHaveCount(3)
  expect(requests).toEqual(['?limit=20', '?limit=20&cursor=users-page-2'])
})

test('redirects an anonymous like to sign in with the feed as return target', async ({ page }) => {
  await mockPublicFeed(page)
  await page.goto('/')

  // 首页视频恰好填满视口时浏览器可能自动加载第二页，点赞按钮需限定到第一个视频卡片
  await page.getByRole('button', { name: '点赞，当前 0 个赞' }).first().click()

  await expect(page).toHaveURL(/\/login\?redirect=\/$/)
})

test('redirects an anonymous comment to sign in with the detail page as return target', async ({
  page,
}) => {
  await page.route('**/api/video/7', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ video: firstVideo }),
    })
  })
  await page.route('**/api/video/7/comments?*', async (route) => {
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ items: [] }) })
  })
  await page.goto('/video/7')

  await page.getByRole('link', { name: '登录后发表评论' }).click()

  await expect(page).toHaveURL(/\/login\?redirect=\/video\/7$/)
})

test('redirects an anonymous follow to sign in with the profile as return target', async ({
  page,
}) => {
  await page.route('**/api/user/7/profile', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        account: firstVideo.author,
        video_count: 1,
        total_likes: 0,
        follower_count: 0,
        vlogger_count: 0,
      }),
    })
  })
  await page.route(
    (url) => url.pathname === '/api/video',
    async (route) => {
      expect(new URL(route.request().url()).search).toBe('?limit=12&author_id=7')
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ items: [firstVideo] }),
      })
    },
  )
  await page.goto('/users/7')

  await page.getByRole('button', { name: '关注', exact: true }).click()

  await expect(page).toHaveURL(/\/login\?redirect=\/users\/7$/)
})

test('keeps author pagination on the legacy endpoint after visiting Timeline', async ({ page }) => {
  await mockPublicFeed(page)
  const requests: URL[] = []
  const authorCursor = 'legacy-author+/= &'
  await page.route('**/api/user/7/profile', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        account: firstVideo.author,
        video_count: 2,
        total_likes: 0,
        follower_count: 0,
        vlogger_count: 0,
      }),
    })
  })
  await page.route((url) => url.pathname === '/api/video', async (route) => {
    const url = new URL(route.request().url())
    requests.push(url)
    expect(url.searchParams.get('author_id')).toBe('7')
    expect(url.searchParams.get('limit')).toBe('12')
    expect(url.searchParams.has('scene')).toBe(false)
    const body = url.searchParams.has('cursor')
      ? { items: [{ ...firstVideo, id: 8, title: '作者第二页' }] }
      : { items: [firstVideo], next_cursor: authorCursor }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
  })
  await page.goto('/')
  await page.getByRole('link', { name: '@first-author' }).first().click()
  await expect(page).toHaveURL(/\/users\/7$/)
  await page.getByRole('button', { name: '加载更多', exact: true }).click()
  await expect(page.getByRole('link', { name: '作者第二页' })).toBeVisible()
  expect(requests).toHaveLength(2)
  expect(requests[0]?.searchParams.has('cursor')).toBe(false)
  expect(requests[1]?.searchParams.get('cursor')).toBe(authorCursor)
})
