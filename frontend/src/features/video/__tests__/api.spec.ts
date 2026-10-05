import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api'
import { clearSession, login } from '@/features/auth/session'
import {
  createDraft,
  discardDraft,
  deleteVideo,
  getDraft,
  getVideoStatus,
  listFollowingFeed,
  listMyVideos,
  listTimelineFeed,
  publishDraft,
  uploadCover,
  uploadVideo,
} from '../api'

type MockUploadResponse = {
  status: number
  body: unknown
  deferred?: boolean
}

class MockXMLHttpRequest {
  static requests: MockXMLHttpRequest[] = []
  static responses: MockUploadResponse[] = []

  status = 0
  responseText = ''
  method = ''
  path = ''
  body: FormData | undefined
  aborted = false
  headers = new Map<string, string>()
  private readonly listeners = new Map<string, Array<() => void>>()
  private readonly progressListeners: Array<
    (event: { lengthComputable: boolean; loaded: number; total: number }) => void
  > = []
  private pendingResponse: MockUploadResponse | undefined

  upload = {
    addEventListener: (
      type: string,
      listener: (event: { lengthComputable: boolean; loaded: number; total: number }) => void,
    ) => {
      if (type === 'progress') {
        this.progressListeners.push(listener)
      }
    },
  }

  open(method: string, path: string) {
    this.method = method
    this.path = path
  }

  setRequestHeader(name: string, value: string) {
    this.headers.set(name, value)
  }

  addEventListener(type: string, listener: () => void) {
    const typeListeners = this.listeners.get(type) ?? []
    typeListeners.push(listener)
    this.listeners.set(type, typeListeners)
  }

  send(body: FormData) {
    this.body = body
    MockXMLHttpRequest.requests.push(this)
    for (const listener of this.progressListeners) {
      listener({ lengthComputable: true, loaded: 1, total: 2 })
    }

    const response = MockXMLHttpRequest.responses.shift()
    if (!response) {
      throw new Error('missing mock upload response')
    }
    if (response.deferred) {
      this.pendingResponse = response
      return
    }
    this.complete(response)
  }

  abort() {
    this.aborted = true
    for (const listener of this.listeners.get('abort') ?? []) {
      listener()
    }
  }

  complete(response = this.pendingResponse) {
    if (!response) {
      throw new Error('missing pending mock upload response')
    }
    this.pendingResponse = undefined
    this.status = response.status
    this.responseText = JSON.stringify(response.body)
    for (const listener of this.listeners.get('load') ?? []) {
      listener()
    }
  }
}

describe('listPublishedVideos', () => {
  afterEach(() => {
    clearSession()
    vi.unstubAllGlobals()
  })

  it('passes the opaque Timeline cursor and cancellation signal without adding unsupported options', async () => {
    const cursor = 'opaque+/= &?中文'
    const controller = new AbortController()
    const options = { cursor, limit: 8, signal: controller.signal, authorID: 42 }
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ items: [], next_cursor: cursor }), {
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(listTimelineFeed(options)).resolves.toEqual({ items: [], next_cursor: cursor })
    expect(fetchMock).toHaveBeenCalledExactlyOnceWith(
      '/api/feed?scene=timeline&limit=8&cursor=opaque%2B%2F%3D+%26%3F%E4%B8%AD%E6%96%87',
      expect.objectContaining({ signal: controller.signal }),
    )
  })

  it('exposes Timeline errors without falling back to the legacy list', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ error: 'invalid feed cursor' }), {
        status: 400,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    await expect(listTimelineFeed({ cursor: 'feed-only' })).rejects.toEqual(
      new ApiError(400, 'invalid feed cursor'),
    )
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/feed?scene=timeline&limit=12&cursor=feed-only')
  })

  it('lists and deletes videos through the authenticated endpoints', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(session), {
          headers: { 'content-type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ items: [], next_cursor: 'next' }), {
          headers: { 'content-type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    await expect(listMyVideos({ cursor: 'current-page', limit: 5 })).resolves.toEqual({
      items: [],
      next_cursor: 'next',
    })
    await expect(deleteVideo(7)).resolves.toBeNull()

    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/video/auth/mine?limit=5&cursor=current-page',
      expect.objectContaining({
        headers: expect.objectContaining({ get: expect.any(Function) }),
      }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/video/auth/7',
      expect.objectContaining({ method: 'DELETE' }),
    )
  })

  it('uploads video and cover media to the authenticated draft', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(JSON.stringify(session), {
          headers: { 'content-type': 'application/json' },
        }),
      ),
    )
    MockXMLHttpRequest.responses = [
      {
        status: 201,
        body: {
          draft_id: 7,
          play_url: '/static/videos/42/demo.mp4',
          play_file_name: 'demo.mp4',
          play_original_name: 'demo.mp4',
        },
      },
      {
        status: 201,
        body: {
          draft_id: 7,
          cover_url: '/static/covers/42/cover.png',
          cover_file_name: 'cover.png',
          cover_original_name: 'cover.png',
        },
      },
    ]
    MockXMLHttpRequest.requests = []
    vi.stubGlobal('XMLHttpRequest', MockXMLHttpRequest)
    await login({ username: 'alice', password: 'password-123' })

    const progress: number[] = []
    const video = new File(['video'], 'demo.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'cover.png', { type: 'image/png' })

    await expect(uploadVideo(7, video, (value) => progress.push(value))).resolves.toEqual({
      draft_id: 7,
      play_url: '/static/videos/42/demo.mp4',
      play_file_name: 'demo.mp4',
      play_original_name: 'demo.mp4',
    })
    await expect(uploadCover(7, cover)).resolves.toEqual({
      draft_id: 7,
      cover_url: '/static/covers/42/cover.png',
      cover_file_name: 'cover.png',
      cover_original_name: 'cover.png',
    })

    expect(progress).toEqual([0.5])
    expect(MockXMLHttpRequest.requests.map((request) => request.path)).toEqual([
      '/api/video/auth/drafts/7/play',
      '/api/video/auth/drafts/7/cover',
    ])
    expect(MockXMLHttpRequest.requests.map((request) => request.method)).toEqual(['POST', 'POST'])
    expect(
      MockXMLHttpRequest.requests.map((request) => request.headers.get('Authorization')),
    ).toEqual(['Bearer access-token', 'Bearer access-token'])
    expect(MockXMLHttpRequest.requests[0]?.body?.get('file')).toBe(video)
    expect(MockXMLHttpRequest.requests[1]?.body?.get('file')).toBe(cover)
  })

  it('aborts an in-flight media upload without reporting a network error', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(JSON.stringify(session), {
          headers: { 'content-type': 'application/json' },
        }),
      ),
    )
    MockXMLHttpRequest.responses = [
      {
        status: 201,
        body: {
          draft_id: 7,
          play_url: '/static/videos/42/demo.mp4',
          play_file_name: 'demo.mp4',
          play_original_name: 'demo.mp4',
        },
        deferred: true,
      },
    ]
    MockXMLHttpRequest.requests = []
    vi.stubGlobal('XMLHttpRequest', MockXMLHttpRequest)
    await login({ username: 'alice', password: 'password-123' })

    const controller = new AbortController()
    const file = new File(['video'], 'demo.mp4', { type: 'video/mp4' })
    const upload = uploadVideo(7, file, undefined, controller.signal)
    await vi.waitFor(() => expect(MockXMLHttpRequest.requests).toHaveLength(1))

    controller.abort()

    await expect(upload).rejects.toMatchObject({ name: 'AbortError', message: '上传已取消' })
    expect(MockXMLHttpRequest.requests[0]?.aborted).toBe(true)
  })

  it('rejects immediately when the upload signal was already aborted', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(JSON.stringify(session), {
          headers: { 'content-type': 'application/json' },
        }),
      ),
    )
    MockXMLHttpRequest.requests = []
    vi.stubGlobal('XMLHttpRequest', MockXMLHttpRequest)
    await login({ username: 'alice', password: 'password-123' })

    const controller = new AbortController()
    controller.abort()
    const file = new File(['video'], 'demo.mp4', { type: 'video/mp4' })

    await expect(uploadVideo(7, file, undefined, controller.signal)).rejects.toMatchObject({
      name: 'AbortError',
      message: '上传已取消',
    })
    expect(MockXMLHttpRequest.requests).toHaveLength(0)
  })

  it('reads and discards a draft through authenticated endpoints', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(session), {
          headers: { 'content-type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            draft: {
              id: 7,
              title: '我的第一条视频',
              description: '',
              status: 'draft',
              has_video: true,
              has_cover: false,
              created_at: '2026-08-26T08:00:00Z',
              updated_at: '2026-08-26T08:01:00Z',
            },
          }),
          {
            headers: { 'content-type': 'application/json' },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            draft: {
              id: 7,
              status: 'purging',
              has_video: true,
              has_cover: false,
            },
          }),
          {
            status: 202,
            headers: { 'content-type': 'application/json' },
          },
        ),
      )
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    await expect(getDraft(7)).resolves.toMatchObject({
      draft: { id: 7, has_video: true, has_cover: false },
    })
    await expect(discardDraft(7)).resolves.toMatchObject({ draft: { id: 7, status: 'purging' } })

    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/video/auth/drafts/7',
      expect.objectContaining({
        headers: expect.objectContaining({ get: expect.any(Function) }),
      }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/video/auth/drafts/7',
      expect.objectContaining({ method: 'DELETE' }),
    )
  })

  it('creates and publishes a draft without client media metadata', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(session), {
          headers: { 'content-type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            draft: { id: 7, title: '我的第一条视频', description: '视频介绍', status: 'draft' },
          }),
          {
            headers: { 'content-type': 'application/json' },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            draft: {
              id: 7,
              title: '我的第一条视频',
              description: '视频介绍',
              status: 'processing',
              has_video: true,
              has_cover: true,
            },
          }),
          {
            status: 202,
            headers: { 'content-type': 'application/json' },
          },
        ),
      )
    vi.stubGlobal('fetch', fetchMock)
    await login({ username: 'alice', password: 'password-123' })

    await expect(
      createDraft({
        title: '我的第一条视频',
        description: '视频介绍',
      }),
    ).resolves.toMatchObject({ draft: { id: 7, status: 'draft' } })
    await expect(publishDraft(7)).resolves.toMatchObject({
      draft: { id: 7, status: 'processing' },
    })

    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/video/auth/drafts',
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({ get: expect.any(Function) }),
        body: JSON.stringify({
          title: '我的第一条视频',
          description: '视频介绍',
        }),
      }),
    )
    expect(fetchMock).toHaveBeenLastCalledWith(
      '/api/video/auth/drafts/7/publish',
      expect.objectContaining({
        method: 'POST',
      }),
    )
    const requestInit = fetchMock.mock.calls[2]?.[1]
    expect(requestInit?.body).toBeUndefined()
    expect(new Headers(requestInit?.headers).get('Authorization')).toBe('Bearer access-token')
  })

  it('queries the authenticated video processing status with cancellation support', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(session), {
          headers: { 'content-type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            status: 'published',
            published_at: '2026-08-26T08:01:00Z',
            rejected_at: null,
            rejected_reason: '',
          }),
          { headers: { 'content-type': 'application/json' } },
        ),
      )
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    const controller = new AbortController()
    await expect(getVideoStatus(7, controller.signal)).resolves.toEqual({
      status: 'published',
      published_at: '2026-08-26T08:01:00Z',
      rejected_at: null,
      rejected_reason: '',
    })

    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/video/auth/7/status',
      expect.objectContaining({ signal: controller.signal }),
    )
    const requestInit = fetchMock.mock.calls[1]?.[1]
    expect(new Headers(requestInit?.headers).get('Authorization')).toBe('Bearer access-token')
  })
})

describe('listFollowingFeed', () => {
  afterEach(() => {
    clearSession()
    vi.unstubAllGlobals()
  })

  function sessionResponse(accessToken: string, refreshToken: string) {
    return new Response(
      JSON.stringify({
        access_token: accessToken,
        refresh_token: refreshToken,
        expires_at: '2026-08-26T08:00:00Z',
        user: { id: 42, username: 'alice' },
      }),
      { headers: { 'content-type': 'application/json' } },
    )
  }

  function requestURL(input: Parameters<typeof fetch>[0]) {
    return typeof input === 'string' ? input : input instanceof Request ? input.url : String(input)
  }

  it('rejects before any request while signed out', async () => {
    const fetchMock = vi.fn<typeof fetch>()
    vi.stubGlobal('fetch', fetchMock)

    await expect(listFollowingFeed()).rejects.toEqual(new ApiError(401, '请先登录后再继续'))
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('sends the Bearer token, passes the cursor, and keeps the signal across session recovery', async () => {
    const followingCalls: Array<{ path: string; authorization: string | null }> = []
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return sessionResponse('access-a', 'refresh-1')
      }
      if (url === '/api/user/refresh') {
        return sessionResponse('access-b', 'refresh-2')
      }
      if (url.startsWith('/api/feed?scene=following')) {
        followingCalls.push({
          path: url,
          authorization: new Headers(init?.headers).get('Authorization'),
        })
        if (followingCalls.length === 1) {
          return new Response(JSON.stringify({ error: 'token expired' }), {
            status: 401,
            headers: { 'content-type': 'application/json' },
          })
        }
        return new Response(JSON.stringify({ items: [], next_cursor: 'following-next' }), {
          headers: { 'content-type': 'application/json' },
        })
      }
      return new Response(null, { status: 404 })
    })
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    const controller = new AbortController()
    await expect(
      listFollowingFeed({ cursor: 'following-page', limit: 8, signal: controller.signal }),
    ).resolves.toEqual({ items: [], next_cursor: 'following-next' })

    expect(followingCalls.map((call) => call.path)).toEqual([
      '/api/feed?scene=following&limit=8&cursor=following-page',
      '/api/feed?scene=following&limit=8&cursor=following-page',
    ])
    expect(followingCalls.map((call) => call.authorization)).toEqual([
      'Bearer access-a',
      'Bearer access-b',
    ])
    const lastFeedCall = fetchMock.mock.calls[fetchMock.mock.calls.length - 1]
    expect(lastFeedCall?.[1]?.signal).toBe(controller.signal)
  })

  it('exposes Following server errors without triggering session recovery', async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return sessionResponse('access-a', 'refresh-1')
      }
      return new Response(JSON.stringify({ error: 'invalid feed cursor' }), {
        status: 400,
        headers: { 'content-type': 'application/json' },
      })
    })
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    await expect(listFollowingFeed({ cursor: 'feed-only' })).rejects.toEqual(
      new ApiError(400, 'invalid feed cursor'),
    )
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/feed?scene=following&limit=12&cursor=feed-only')
  })

  it('keeps Timeline anonymous even with an active session', async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return sessionResponse('access-a', 'refresh-1')
      }
      return new Response(JSON.stringify({ items: [] }), {
        headers: { 'content-type': 'application/json' },
      })
    })
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    await expect(listTimelineFeed()).resolves.toEqual({ items: [] })

    const lastCall = fetchMock.mock.calls[fetchMock.mock.calls.length - 1]
    expect(new Headers(lastCall?.[1]?.headers).get('Authorization')).toBeNull()
    expect(lastCall?.[0]).toBe('/api/feed?scene=timeline&limit=12')
  })
})
