import { test, expect } from '@playwright/test'

// See here how to get started:
// https://playwright.dev/docs/intro
test('shows the public feed', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('link', { name: 'GoFeed 首页' })).toBeVisible()
  await expect(page.getByRole('heading', { name: '最新视频' })).toBeVisible()
})
