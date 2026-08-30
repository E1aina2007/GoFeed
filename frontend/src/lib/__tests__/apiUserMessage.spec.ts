import { describe, expect, it } from 'vitest'

import { ApiError, apiUserMessage } from '../api'

describe('apiUserMessage', () => {
  it('maps known statuses to fixed Chinese messages regardless of the server text', () => {
    expect(apiUserMessage(new ApiError(400, 'invalid cursor'), '回退文案')).toBe(
      '提交内容未通过校验，请检查后重试',
    )
    expect(apiUserMessage(new ApiError(401, 'invalid or expired token'), '回退文案')).toBe(
      '登录状态已失效，请重新登录',
    )
    expect(apiUserMessage(new ApiError(404, 'video not found'), '回退文案')).toBe(
      '内容不存在或已被删除',
    )
    expect(
      apiUserMessage(new ApiError(503, 'engagement stats temporarily unavailable'), '回退文案'),
    ).toBe('服务暂时不可用，请稍后重试')
  })

  it('lets a call site override the mapping for status-specific semantics', () => {
    expect(
      apiUserMessage(new ApiError(401, 'invalid username or password'), '回退文案', {
        401: '用户名或密码错误',
      }),
    ).toBe('用户名或密码错误')
  })

  it('keeps the fallback for non-ApiError values and unmapped client error statuses', () => {
    expect(apiUserMessage(new Error('网络故障'), '回退文案')).toBe('回退文案')
    expect(apiUserMessage(undefined, '回退文案')).toBe('回退文案')
    expect(apiUserMessage(new ApiError(418, "I'm a teapot"), '回退文案')).toBe("I'm a teapot")
  })

  it('maps any other 5xx failure to the unavailable message', () => {
    expect(apiUserMessage(new ApiError(502, 'bad gateway'), '回退文案')).toBe(
      '服务暂时不可用，请稍后重试',
    )
  })
})
