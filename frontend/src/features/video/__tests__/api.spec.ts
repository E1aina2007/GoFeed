import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api'
import { clearSession, login } from '@/features/auth/session'
import {
  createDraft,
  discardDraft,
  deleteVideo,
  getDraft,
  getPublishedVideo,
  getVideoStatus,
  listMyVideos,
  listPublishedVideos,
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

  it('requests the public feed with its cursor', async () => {
    const controller = new AbortController()
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(
        JSON.stringify({
          items: [],
          next_cursor: 'next-page',
        }),
        {
          headers: { 'content-type': 'application/json' },
        },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const result = await listPublishedVideos({
      cursor: 'current-page',
      limit: 8,
      signal: controller.signal,
    })

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/video?limit=8&cursor=current-page',
      expect.objectContaining({ signal: controller.signal }),
    )
    expect(result.next_cursor).toBe('next-page')
  })

  it('requests a published video and filters the public feed by author', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ video: { id: 7 } }), {
          headers: { 'content-type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ items: [] }), {
          headers: { 'content-type': 'application/json' },
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    await expect(getPublishedVideo(7)).resolves.toEqual({ video: { id: 7 } })
    await listPublishedVideos({ authorID: 42 })

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/video/7', expect.any(Object))
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/video?limit=12&author_id=42',
      expect.any(Object),
    )
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

  it('exposes the server error for a failed request', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(JSON.stringify({ error: 'invalid cursor' }), {
          status: 400,
          headers: { 'content-type': 'application/json' },
        }),
      ),
    )

    await expect(listPublishedVideos()).rejects.toEqual(new ApiError(400, 'invalid cursor'))
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

  it('rejects invalid draft IDs before making an authenticated request', async () => {
    const fetchMock = vi.fn<typeof fetch>()
    vi.stubGlobal('fetch', fetchMock)

    for (const draftID of [0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
      await expect(getDraft(draftID)).rejects.toEqual(new ApiError(400, '草稿 ID 无效'))
      await expect(discardDraft(draftID)).rejects.toEqual(new ApiError(400, '草稿 ID 无效'))
    }
    expect(fetchMock).not.toHaveBeenCalled()
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

  it('rejects invalid video IDs before querying processing status', async () => {
    const fetchMock = vi.fn<typeof fetch>()
    vi.stubGlobal('fetch', fetchMock)

    for (const videoID of [0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
      await expect(getVideoStatus(videoID)).rejects.toEqual(new ApiError(400, '视频 ID 无效'))
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
