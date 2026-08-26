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
  await page.route('**/api/video**', async (route) => {
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
  await expect(feed.locator('.short-video')).toHaveCount(1)
  await feed.evaluate((element) => {
    element.scrollTo({ top: element.scrollHeight })
    element.dispatchEvent(new Event('scroll'))
  })

  await expect(page.getByRole('link', { name: '更新后的首屏视频' })).toBeVisible()
  await expect(page.getByRole('link', { name: '第二条视频' })).toBeVisible()
  await expect(feed.locator('.short-video')).toHaveCount(2)
})

test('redirects an anonymous like to sign in with the feed as return target', async ({ page }) => {
  await mockPublicFeed(page)
  await page.goto('/')

  await page.getByRole('button', { name: '点赞，当前 0 个赞' }).click()

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
