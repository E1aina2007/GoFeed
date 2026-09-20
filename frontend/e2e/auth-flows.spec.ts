import { expect, test, type Page } from '@playwright/test'

const session = {
  access_token: 'e2e-access-token',
  refresh_token: 'e2e-refresh-token',
  expires_at: '2027-01-01T00:00:00Z',
  user: { id: 42, username: 'e2e-user' },
}

const myVideo = {
  id: 7,
  title: '我的第一条视频',
  description: '待删除的视频',
  play_url: '/static/videos/42/mine.mp4',
  play_file_name: 'mine.mp4',
  play_original_name: 'mine.mp4',
  cover_url: '/static/covers/42/mine.jpg',
  cover_file_name: 'mine.jpg',
  cover_original_name: 'mine.jpg',
  published_at: '2026-08-23T08:00:00Z',
  likes_count: 0,
  comments_count: 0,
  author: { id: 42, username: 'e2e-user' },
}

async function mockEmptyFeed(page: Page) {
  await page.route('**/api/video**', async (route) => {
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ items: [] }) })
  })
}

test('registers an account and signs in from the login page', async ({ page }) => {
  await mockEmptyFeed(page)
  await page.route('**/api/user/register', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ user: { id: 42, username: 'e2e-user' } }),
    })
  })
  await page.route('**/api/user/login', async (route) => {
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify(session) })
  })

  await page.goto('/register')
  await page.getByLabel('用户名', { exact: true }).fill('e2e-user')
  await page.getByLabel('密码', { exact: true }).fill('password-123')
  await page.getByLabel('确认密码').fill('password-123')
  await page.getByRole('button', { name: '注册', exact: true }).click()

  await expect(page).toHaveURL(/\/login\?registered=1$/)
  await expect(page.getByText('注册成功，请使用新账号登录')).toBeVisible()

  await page.getByLabel('用户名', { exact: true }).fill('e2e-user')
  await page.getByLabel('密码', { exact: true }).fill('password-123')
  await page.getByRole('button', { name: '登录', exact: true }).click()

  await expect(page).toHaveURL(/\/$/)
  await expect(page.locator('.account-state__name')).toHaveText('e2e-user')
})

test('waits out the login rate limit window before a successful retry', async ({ page }) => {
  let loginAttempts = 0
  await mockEmptyFeed(page)
  await page.route('**/api/user/login', async (route) => {
    loginAttempts += 1
    if (loginAttempts === 1) {
      await route.fulfill({
        status: 429,
        headers: { 'Retry-After': '3' },
        contentType: 'application/json',
        body: JSON.stringify({ error: 'rate limit exceeded' }),
      })
      return
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify(session) })
  })

  await page.goto('/login')
  await page.getByLabel('用户名', { exact: true }).fill('e2e-user')
  await page.getByLabel('密码', { exact: true }).fill('password-123')
  const submit = page.getByRole('button', { name: '登录', exact: true })
  await submit.click()

  const alert = page.getByRole('alert')
  await expect(alert).toHaveText('请求过于频繁，请 3 秒后重试')
  await expect(submit).toBeDisabled()

  // 等待期结束前保持禁用，倒计时归零后才恢复手动重试
  // 3 秒窗口在 WebKit 上可能因渲染与轮询开销逼近 5 秒，这里使用显式超时预算
  await expect(submit).toBeEnabled({ timeout: 20_000 })
  expect(loginAttempts).toBe(1)

  await submit.click()
  await expect(page).toHaveURL(/\/$/)
  expect(loginAttempts).toBe(2)
})

test('rejects mismatched password confirmation without calling the API', async ({ page }) => {
  await page.goto('/register')
  await page.getByLabel('用户名', { exact: true }).fill('e2e-user')
  await page.getByLabel('密码', { exact: true }).fill('password-123')
  await page.getByLabel('确认密码').fill('different-123')
  await page.getByRole('button', { name: '注册', exact: true }).click()

  await expect(page.getByText('两次输入的密码不一致')).toBeVisible()
  await expect(page).toHaveURL(/\/register$/)
})

test('deletes one of my videos through the confirm dialog', async ({ page }) => {
  await page.addInitScript((value) => {
    window.localStorage.setItem('gofeed.auth.session', JSON.stringify(value))
  }, session)
  await page.route('**/api/video/auth/mine**', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ items: [myVideo] }),
    })
  })
  await page.route('**/api/video/auth/7', async (route) => {
    if (route.request().method() !== 'DELETE') {
      await route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"not found"}' })
      return
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: 'null' })
  })
  await page.goto('/mine')

  const item = page.locator('.video-list-item')
  await expect(item).toHaveCount(1)

  await page.getByRole('button', { name: '删除视频' }).click()
  const dialog = page.getByRole('alertdialog')
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText('确定删除“我的第一条视频”吗？')

  await dialog.getByRole('button', { name: '取消' }).click()
  await expect(dialog).toBeHidden()
  await expect(item).toHaveCount(1)

  await page.getByRole('button', { name: '删除视频' }).click()
  await dialog.getByRole('button', { name: '删除', exact: true }).click()

  await expect(item).toHaveCount(0)
  await expect(page.locator('.success-message')).toHaveText('视频已删除')
})
