export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

type ErrorResponse = {
  error?: string
}

function isErrorResponse(value: unknown): value is ErrorResponse {
  return typeof value === 'object' && value !== null && 'error' in value
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')

  const response = await fetch(path, { ...init, headers })
  const contentType = response.headers.get('content-type') ?? ''
  const body: unknown = contentType.includes('application/json') ? await response.json() : null

  if (!response.ok) {
    const message = isErrorResponse(body) && typeof body.error === 'string'
      ? body.error
      : '请求失败，请稍后重试'
    throw new ApiError(response.status, message)
  }

  return body as T
}

const apiStatusMessages: Record<number, string> = {
  400: '提交内容未通过校验，请检查后重试',
  401: '登录状态已失效，请重新登录',
  403: '当前账号无权执行此操作',
  404: '内容不存在或已被删除',
  409: '内容状态已变化，请刷新后重试',
  413: '请求内容超过大小限制',
  429: '请求过于频繁，请稍后重试',
  503: '服务暂时不可用，请稍后重试',
}

// 服务端 error 字段面向接口调试；界面提示按状态码固定为中文，不把英文原文直接透给用户
// overrides 供调用点覆盖语义特殊的场景，例如登录失败的 401 表示凭据错误而非会话失效
export function apiUserMessage(
  error: unknown,
  fallback: string,
  overrides?: Record<number, string>,
): string {
  if (!(error instanceof ApiError)) {
    return fallback
  }
  const override = overrides?.[error.status]
  if (override !== undefined) {
    return override
  }
  const mapped = apiStatusMessages[error.status]
  if (mapped !== undefined) {
    return mapped
  }
  return error.status >= 500 ? '服务暂时不可用，请稍后重试' : error.message
}
