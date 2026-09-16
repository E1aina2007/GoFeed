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

// 限流等待倒计时：每秒递减，归零后清除定时器并恢复可提交状态
export function useRateLimitCountdown(): RateLimitCountdown {
  const seconds = ref(0)
  let timer: ReturnType<typeof setInterval> | undefined

  function clear() {
    if (timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
    seconds.value = 0
  }

  function start(retryAfterSeconds: number) {
    clear()
    if (!Number.isSafeInteger(retryAfterSeconds) || retryAfterSeconds <= 0) {
      return
    }

    seconds.value = retryAfterSeconds
    timer = setInterval(() => {
      seconds.value -= 1
      if (seconds.value <= 0) {
        clear()
      }
    }, 1000)
  }

  return { seconds, start, clear }
}
