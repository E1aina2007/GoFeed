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
