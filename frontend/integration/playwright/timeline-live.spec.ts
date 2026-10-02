import { resolve } from 'node:path'
import { writeFileSync } from 'node:fs'
import process from 'node:process'

import { expect, test } from '@playwright/test'

import type { VideoListResponse } from '../../src/features/video/api'

const expectedIDs: number[] = JSON.parse(process.env.GOFEED_TIMELINE_IDS ?? '[]')

test('renders and paginates real Timeline JSON, including after a fresh reload', async ({ page }, testInfo) => {
  expect(expectedIDs).toHaveLength(13)
  // 只替换媒体；Feed JSON、排序与游标由隔离 Go API / MySQL 生成
  await page.route('**/static/videos/**', async (route) => {
    await route.fulfill({ contentType: 'video/webm', path: resolve('e2e/fixtures/playable.webm') })
  })
  await page.route('**/static/covers/**', async (route) => {
    await route.fulfill({
      contentType: 'image/png',
      body: Buffer.from(
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=',
        'base64',
      ),
    })
  })
  const requests: URL[] = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname.startsWith('/api/')) {
      requests.push(url)
    }
  })
  const secondPageBodies: string[] = []
  const pageBodies: string[] = []
  const navigations = [() => page.goto('/'), () => page.reload()]
  for (const [round, navigate] of navigations.entries()) {
    const firstResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === '/api/feed' && !url.searchParams.has('cursor')
    })
    await navigate()
    const first = await firstResponse
    expect(first.status()).toBe(200)
    expect(new URL(first.url()).search).toBe('?scene=timeline&limit=12')
    const firstBody: VideoListResponse = await first.json()
    pageBodies.push(await first.text())
    expect(firstBody.items.map((video) => video.id)).toEqual(expectedIDs.slice(0, 12))
    expect(firstBody.next_cursor).toEqual(expect.any(String))
    expect(firstBody.next_cursor).not.toBe('')
    const feed = page.getByRole('main', { name: '最新视频' })
    const cards = feed.locator('.short-video')
    await expect(cards).toHaveCount(12)
    await expect(cards.first()).toContainText(firstBody.items[0]!.title)
    await expect(cards.first()).toContainText(`@${firstBody.items[0]!.author.username}`)
    await expect(cards.first().locator('video')).toHaveAttribute('src', firstBody.items[0]!.play_url)
    await expect(cards.first().locator('video')).toHaveAttribute('poster', firstBody.items[0]!.cover_url)

    const secondResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === '/api/feed' && url.searchParams.has('cursor')
    })
    await feed.evaluate((element) => {
      element.scrollTo({ top: element.scrollHeight })
      element.dispatchEvent(new Event('scroll'))
    })
    const second = await secondResponse
    expect(second.status()).toBe(200)
    const query = new URL(second.url()).searchParams
    expect([...query.keys()].sort()).toEqual(['cursor', 'limit', 'scene'])
    expect(query.get('scene')).toBe('timeline')
    expect(query.get('limit')).toBe('12')
    expect(query.get('cursor')).toBe(firstBody.next_cursor)
    const secondBody: VideoListResponse = await second.json()
    expect(secondBody.items.map((video) => video.id)).toEqual(expectedIDs.slice(12))
    expect(secondBody.next_cursor).toBeUndefined()
    const allItems = [...firstBody.items, ...secondBody.items]
    for (let index = 1; index < allItems.length; index += 1) {
      const newer = allItems[index - 1]!
      const older = allItems[index]!
      expect(
        Date.parse(newer.published_at) > Date.parse(older.published_at)
        || (newer.published_at === older.published_at && newer.id > older.id),
      ).toBe(true)
    }
    secondPageBodies.push(await second.text())
    pageBodies.push(await second.text())
    await expect(cards).toHaveCount(13)
    await expect(cards.locator('h2')).toHaveText(allItems.map((video) => video.title))
    await expect(feed.locator('.stream-status')).toHaveText('已经到底了')
    await feed.evaluate((element) => element.dispatchEvent(new Event('scroll')))
    expect(requests).toHaveLength((round + 1) * 2)
  }
  expect(secondPageBodies[1]).toBe(secondPageBodies[0])
  expect(requests.every((url) => url.pathname === '/api/feed')).toBe(true)
  writeFileSync(
    resolve(process.env.GOFEED_TIMELINE_RESULTS!, `${testInfo.project.name}.json`),
    JSON.stringify(pageBodies),
  )
})
