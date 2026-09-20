import { expect, test, type APIRequestContext, type APIResponse, type Locator } from '@playwright/test'

// 该用例会真实占用登录限流额度并按整窗等待，只在显式启用时运行
const liveAPI = process.env.GOFEED_E2E_REAL_API === '1'

// 注册额度为 5 次/小时/IP，且限流按来源地址计数；
// 因此默认只在一个浏览器项目上运行，需要换项目时显式指定
const liveProject = process.env.GOFEED_E2E_PROJECT ?? 'chromium'

// 服务端固定窗口为 10 次/分钟，前 10 次业务失败同样计数
const loginLimitPerWindow = 10

function alertTextSeconds(text: string) {
  return Number(text.replace(/\D+/g, ''))
}

// 取一次带数字的倒计时读数，并确认它在同一秒内稳定，避免把每秒一次的字号变化误判为稳定值
async function stableCountdownSeconds(alert: Locator, deadlineMilliseconds = 5_000) {
  const deadline = Date.now() + deadlineMilliseconds
  let previous = ''

  while (Date.now() < deadline) {
    const current = await alert.innerText()
    if (current === previous && /\d/.test(current)) {
      return alertTextSeconds(current)
    }
    previous = current
  }

  return /\d/.test(previous) ? alertTextSeconds(previous) : 0
}

function retryAfterSecondsOf(response: APIResponse) {
  const header = response.headers()['retry-after']
  return header === undefined ? null : Number(header)
}

async function probeLogin(request: APIRequestContext, username: string, password: string) {
  return request.post('/api/user/login', { data: { username, password } })
}

// 真实窗口可能被其他请求占用，先等到窗口清零再开始，避免把前置状态当成断言失败
async function waitForCleanLoginWindow(
  request: APIRequestContext,
  username: string,
  deadlineMilliseconds: number,
) {
  const deadline = Date.now() + deadlineMilliseconds
  let response = await probeLogin(request, username, 'definitely-wrong-123')

  while (response.status() === 429 && Date.now() < deadline) {
    const retryAfter = retryAfterSecondsOf(response)
    const pauseSeconds = Math.min(retryAfter === null ? 5 : retryAfter + 1, 30)
    await new Promise((resolve) => setTimeout(resolve, pauseSeconds * 1000))
    response = await probeLogin(request, username, 'definitely-wrong-123')
  }

  return response
}

// 限流按来源地址计数，同一地址可能已有其他流量；
// 额度未用尽时继续补满，已限流则返回该真实响应
async function exhaustLoginWindow(request: APIRequestContext, username: string) {
  let response = await probeLogin(request, username, 'definitely-wrong-123')

  for (let attempt = 2; attempt <= loginLimitPerWindow; attempt += 1) {
    if (response.status() === 429) {
      break
    }
    expect(response.status()).toBe(401)
    response = await probeLogin(request, username, 'definitely-wrong-123')
  }

  return response
}

test.describe('登录限流（真实后端）', () => {
  // 该用例会真实占用限流额度并按整窗等待，默认套件必须条件跳过
  // eslint-disable-next-line playwright/no-skipped-test
  test.skip(!liveAPI, '设置 GOFEED_E2E_REAL_API=1 后针对真实后端运行')

  test('展示服务端 Retry-After 倒计时，窗口结束后可重新登录', async ({ page }, testInfo) => {
    test.setTimeout(300_000)
    // 条件跳过：真实限流额度按来源地址共享，不能与其他项目并发抢占
    // eslint-disable-next-line playwright/no-skipped-test
    test.skip(testInfo.project.name !== liveProject, `真实用例默认只在 ${liveProject} 上运行`)

    const username = `e2elive${Date.now().toString().slice(-6)}`
    const password = 'live-password-123'

    // 只隔离需要登录态的私有列表，公开 Feed 保持真实请求
    await page.route('**/api/video/auth/mine**', async (route) => {
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ items: [] }) })
    })

    // 真实注册账号，作为限流窗口结束后成功登录的凭据；
    // 注册额度也是服务端限流的一部分，用尽时必须如实跳过而不是伪造凭据
    const registration = await page.request.post('/api/user/register', {
      data: { username, password },
    })
    // eslint-disable-next-line playwright/no-skipped-test
    test.skip(
      registration.status() === 429,
      '注册额度 5 次/小时已用尽，请在窗口清零后重跑真实联调',
    )
    expect(
      [201, 409],
      `注册应成功或提示已存在，实际状态 ${registration.status()}`,
    ).toContain(registration.status())

    // 前置状态：先等到真实窗口清零，再确认业务语义为 401
    const beforeWindow = await waitForCleanLoginWindow(page.request, username, 180_000)
    expect(beforeWindow.status()).toBe(401)

    // 限流按来源地址计数，同一地址可能已有其他流量
    const priming = await exhaustLoginWindow(page.request, username)
    expect(priming.status(), '窗口额度用尽后必须返回 429').toBe(429)

    await page.goto('/login')
    const form = page.locator('.account-form')
    await form.getByLabel('用户名', { exact: true }).fill(username)
    await form.getByLabel('密码', { exact: true }).fill(password)
    const submit = form.getByRole('button', { name: '登录', exact: true })

    const [loginResponse] = await Promise.all([
      page.waitForResponse((response) => response.url().includes('/api/user/login')),
      submit.click(),
    ])
    const retryAfterHeader = loginResponse.headers()['retry-after']
    expect(
      loginResponse.status(),
      `页面提交应命中限流，实际状态 ${loginResponse.status()}，Retry-After=${retryAfterHeader ?? '-'}`,
    ).toBe(429)

    const alert = form.locator('[role="alert"]')
    await expect(alert).toHaveText(/^请求过于频繁，请 \d+ 秒后重试$/)
    await expect(submit).toBeDisabled()

    // 界面等待时间只能来自服务端 Retry-After，不能自行编造
    const firstShown = await stableCountdownSeconds(alert)
    expect(firstShown, '倒计时读数必须来自服务端 Retry-After，而不是空文案').toBeGreaterThan(0)
    expect(firstShown).toBeLessThanOrEqual(60)

    // 真实窗口按 Redis TTL 到期；界面显示的是 HTTP 往返前的 TTL，可能大于建键后的剩余窗口，
    // 因此按“倒计时归零 + 一次采样间隔”等待，而不是按界面首值放大等待
    await expect(submit).toBeEnabled({ timeout: (firstShown * 1000) + 15_000 })
    await expect(alert).toBeHidden()

    await submit.click()
    await expect(page).toHaveURL(/\/$/)
    await expect(page.locator('.account-state__name')).toHaveText(username)

    // 刷新后仍保持登录态，并真实读取公开 Feed
    const feedResponsePromise = page.waitForResponse((response) =>
      response.url().includes('/api/video?'),
    )
    await page.reload()
    const feedResponse = await feedResponsePromise
    expect(feedResponse.status()).toBe(200)

    const feed = (await feedResponse.json()) as { items: { title: string }[] }
    expect(feedResponse.headers()['content-type']).toContain('application/json')
    expect(feed.items.length, '真实库中应存在公开视频，否则本条无法验证 Feed 渲染').toBeGreaterThan(0)

    await expect(page.locator('.account-state__name')).toHaveText(username)
    // 页面必须渲染服务端真实返回的视频，而不是空态
    await expect(page.getByRole('link', { name: feed.items[0]!.title, exact: true })).toBeVisible()
  })
})
