import { ref, type Ref } from 'vue'

import { ApiError } from './api'

export type RateLimitCountdown = {
  seconds: Ref<number>
  start: (retryAfterSeconds: number) => void
  clear: () => void
}

export const rateLimitedStatus = 429

// 只有服务端 429 且 Retry-After 为正整数秒时才给出可重试等待时间
export function rateLimitRetrySeconds(error: unknown): number | null {
  if (!(error instanceof ApiError) || error.status !== rateLimitedStatus) {
    return null
  }
  return error.retryAfterSeconds
}

// 等待提示只为说明可再次提交的时间，依据仅来自服务端 Retry-After
export function rateLimitMessage(seconds: number): string {
  return `请求过于频繁，请 ${seconds} 秒后重试`
}

// 限流等待倒计时：按服务端给定的绝对截止时间推进，归零后清除定时器并恢复可提交状态
// 以截止时间而非每秒递减的计数为准，定时器被浏览器节流时也不会延长实际等待时间
export function useRateLimitCountdown(): RateLimitCountdown {
  const seconds = ref(0)
  let timer: ReturnType<typeof setInterval> | undefined
  let deadline = 0

  function remainingSeconds() {
    const remaining = deadline - Date.now()
    return remaining > 0 ? Math.ceil(remaining / 1000) : 0
  }

  function clear() {
    if (timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
    deadline = 0
    seconds.value = 0
  }

  function tick() {
    seconds.value = remainingSeconds()
    if (seconds.value <= 0) {
      clear()
    }
  }

  function start(retryAfterSeconds: number) {
    clear()
    if (!Number.isSafeInteger(retryAfterSeconds) || retryAfterSeconds <= 0) {
      return
    }

    deadline = Date.now() + retryAfterSeconds * 1000
    seconds.value = retryAfterSeconds
    timer = setInterval(tick, 1000)
  }

  return { seconds, start, clear }
}
