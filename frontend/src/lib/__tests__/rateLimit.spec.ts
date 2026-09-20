import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, request } from '../api'
import { rateLimitMessage, rateLimitRetrySeconds, useRateLimitCountdown } from '../rateLimit'

function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json', ...headers },
  })
}

describe('rateLimitRetrySeconds', () => {
  it('reads the positive integer second count carried by a 429 ApiError', () => {
    expect(rateLimitRetrySeconds(new ApiError(429, 'rate limit exceeded', 30))).toBe(30)
  })

  it('returns null for other statuses, missing or invalid retry-after values', () => {
    expect(rateLimitRetrySeconds(new ApiError(429, 'rate limit exceeded'))).toBeNull()
    expect(rateLimitRetrySeconds(new ApiError(409, 'username already exists', 30))).toBeNull()
    expect(rateLimitRetrySeconds(new Error('网络故障'))).toBeNull()
    expect(rateLimitRetrySeconds(undefined)).toBeNull()
  })
})

describe('request on a 429 response', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('keeps the server retry-after seconds on the error without retrying by itself', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        jsonResponse({ error: 'rate limit exceeded' }, 429, { 'Retry-After': '12' }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const error: unknown = await request('/api/user/login', { method: 'POST' }).catch(
      (reason: unknown) => reason,
    )

    expect(error).toBeInstanceOf(ApiError)
    expect(rateLimitRetrySeconds(error)).toBe(12)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('treats a missing or non numeric retry-after header as no wait time', async () => {
    const missing = vi
      .fn<typeof fetch>()
      .mockResolvedValue(jsonResponse({ error: 'rate limit exceeded' }, 429))
    vi.stubGlobal('fetch', missing)
    const withoutHeader: unknown = await request('/api/user/login', { method: 'POST' }).catch(
      (reason: unknown) => reason,
    )
    expect(rateLimitRetrySeconds(withoutHeader)).toBeNull()

    const invalid = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        jsonResponse({ error: 'rate limit exceeded' }, 429, {
          'Retry-After': 'Wed, 21 Oct 2026 07:28:00 GMT',
        }),
      )
    vi.stubGlobal('fetch', invalid)
    const withDate: unknown = await request('/api/user/login', { method: 'POST' }).catch(
      (reason: unknown) => reason,
    )
    expect(rateLimitRetrySeconds(withDate)).toBeNull()
  })
})

describe('rateLimitMessage', () => {
  it('states the server provided wait time in seconds', () => {
    expect(rateLimitMessage(30)).toBe('请求过于频繁，请 30 秒后重试')
    expect(rateLimitMessage(1)).toBe('请求过于频繁，请 1 秒后重试')
  })
})

describe('useRateLimitCountdown', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('counts down second by second and releases the submit state at zero', () => {
    vi.useFakeTimers()
    const countdown = useRateLimitCountdown()

    countdown.start(3)
    expect(countdown.seconds.value).toBe(3)

    vi.advanceTimersByTime(1000)
    expect(countdown.seconds.value).toBe(2)

    vi.advanceTimersByTime(2000)
    expect(countdown.seconds.value).toBe(0)

    // 归零后定时器必须停止，继续推进时间不会出现负值
    vi.advanceTimersByTime(5000)
    expect(countdown.seconds.value).toBe(0)
  })

  it('restarts from the newest retry-after value instead of stacking timers', () => {
    vi.useFakeTimers()
    const countdown = useRateLimitCountdown()

    countdown.start(5)
    vi.advanceTimersByTime(2000)
    countdown.start(4)
    expect(countdown.seconds.value).toBe(4)

    vi.advanceTimersByTime(1000)
    expect(countdown.seconds.value).toBe(3)

    countdown.clear()
    expect(countdown.seconds.value).toBe(0)
    vi.advanceTimersByTime(3000)
    expect(countdown.seconds.value).toBe(0)
  })

  it('ignores values that are not positive integer seconds', () => {
    const countdown = useRateLimitCountdown()

    countdown.start(0)
    expect(countdown.seconds.value).toBe(0)

    countdown.start(-3)
    expect(countdown.seconds.value).toBe(0)
  })

  // 浏览器在页面繁忙时会节流定时器，等待时间不能被拉长
  it('keeps the server deadline when the timer fires later than one second', () => {
    vi.useFakeTimers()
    const countdown = useRateLimitCountdown()

    countdown.start(3)
    expect(countdown.seconds.value).toBe(3)

    // 一次回调跨越了整段等待期，倒计时必须直接归零而不是只减一
    vi.advanceTimersByTime(3000)
    expect(countdown.seconds.value).toBe(0)

    vi.advanceTimersByTime(4000)
    expect(countdown.seconds.value).toBe(0)
  })

  it('uses an absolute deadline instead of counting interval ticks', () => {
    vi.useFakeTimers()
    const countdown = useRateLimitCountdown()

    countdown.start(4)
    // 真实时间已经越过截止点，即使定时器回调被推迟也应立即归零
    vi.setSystemTime(Date.now() + 4000)
    vi.advanceTimersByTime(1000)
    expect(countdown.seconds.value).toBe(0)
  })

  it('does not let a stale wait deadline shorten a newer wait', () => {
    vi.useFakeTimers()
    const countdown = useRateLimitCountdown()

    countdown.start(10)
    vi.advanceTimersByTime(1000)
    // 新窗口以新的 startsAt 为基准，旧的截止时间不能提前结束等待
    countdown.start(2)
    expect(countdown.seconds.value).toBe(2)

    vi.advanceTimersByTime(1000)
    expect(countdown.seconds.value).toBe(1)

    vi.advanceTimersByTime(1000)
    expect(countdown.seconds.value).toBe(0)
  })
})
