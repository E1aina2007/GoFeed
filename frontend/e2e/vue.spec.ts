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

  await expect(page.getByRole('link', { name: 'GoFeed 首页' })).toBeVisible()
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
