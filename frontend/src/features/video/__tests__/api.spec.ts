import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api'
import { clearSession, login } from '@/features/auth/session'
import { deleteVideo, getPublishedVideo, listMyVideos, listPublishedVideos, publishVideo, uploadCover, uploadVideo } from '../api'

type MockUploadResponse = {
  status: number
  body: unknown
}

class MockXMLHttpRequest {
  static requests: MockXMLHttpRequest[] = []
  static responses: MockUploadResponse[] = []

  status = 0
  responseText = ''
  method = ''
  path = ''
  body: FormData | undefined
  headers = new Map<string, string>()
  private readonly listeners = new Map<string, Array<() => void>>()
  private readonly progressListeners: Array<(event: { lengthComputable: boolean, loaded: number, total: number }) => void> = []

  upload = {
    addEventListener: (type: string, listener: (event: { lengthComputable: boolean, loaded: number, total: number }) => void) => {
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
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      items: [],
      next_cursor: 'next-page',
    }), {
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await listPublishedVideos({ cursor: 'current-page', limit: 8 })

    expect(fetchMock).toHaveBeenCalledWith('/api/video?limit=8&cursor=current-page', expect.any(Object))
    expect(result.next_cursor).toBe('next-page')
  })

  it('requests a published video and filters the public feed by author', async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ video: { id: 7 } }), {
        headers: { 'content-type': 'application/json' },
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [] }), {
        headers: { 'content-type': 'application/json' },
      }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(getPublishedVideo(7)).resolves.toEqual({ video: { id: 7 } })
    await listPublishedVideos({ authorID: 42 })

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/video/7', expect.any(Object))
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/video?limit=12&author_id=42', expect.any(Object))
  })

  it('lists and deletes videos through the authenticated endpoints', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify(session), {
        headers: { 'content-type': 'application/json' },
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [], next_cursor: 'next' }), {
        headers: { 'content-type': 'application/json' },
      }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    await expect(listMyVideos({ cursor: 'current-page', limit: 5 })).resolves.toEqual({ items: [], next_cursor: 'next' })
    await expect(deleteVideo(7)).resolves.toBeNull()

    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/video/auth/mine?limit=5&cursor=current-page', expect.objectContaining({
      headers: expect.objectContaining({ get: expect.any(Function) }),
    }))
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/video/auth/7', expect.objectContaining({ method: 'DELETE' }))
  })

  it('exposes the server error for a failed request', async () => {
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ error: 'invalid cursor' }), {
      status: 400,
      headers: { 'content-type': 'application/json' },
    })))

    await expect(listPublishedVideos()).rejects.toEqual(new ApiError(400, 'invalid cursor'))
  })

  it('uploads video and cover media with the authenticated session', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(session), {
      headers: { 'content-type': 'application/json' },
    })))
    MockXMLHttpRequest.responses = [
      { status: 201, body: { play_url: '/static/videos/42/demo.mp4', play_file_name: 'demo.mp4', play_original_name: 'demo.mp4' } },
      { status: 201, body: { cover_url: '/static/covers/42/cover.png', cover_file_name: 'cover.png', cover_original_name: 'cover.png' } },
    ]
    MockXMLHttpRequest.requests = []
    vi.stubGlobal('XMLHttpRequest', MockXMLHttpRequest)
    await login({ username: 'alice', password: 'password-123' })

    const progress: number[] = []
    const video = new File(['video'], 'demo.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'cover.png', { type: 'image/png' })

    await expect(uploadVideo(video, (value) => progress.push(value))).resolves.toEqual({
      play_url: '/static/videos/42/demo.mp4',
      play_file_name: 'demo.mp4',
      play_original_name: 'demo.mp4',
    })
    await expect(uploadCover(cover)).resolves.toEqual({
      cover_url: '/static/covers/42/cover.png',
      cover_file_name: 'cover.png',
      cover_original_name: 'cover.png',
    })

    expect(progress).toEqual([0.5])
    expect(MockXMLHttpRequest.requests.map((request) => request.path)).toEqual([
      '/api/video/auth/upload/video',
      '/api/video/auth/upload/cover',
    ])
    expect(MockXMLHttpRequest.requests.map((request) => request.method)).toEqual(['POST', 'POST'])
    expect(MockXMLHttpRequest.requests.map((request) => request.headers.get('Authorization'))).toEqual([
      'Bearer access-token',
      'Bearer access-token',
    ])
    expect(MockXMLHttpRequest.requests[0]?.body?.get('file')).toBe(video)
    expect(MockXMLHttpRequest.requests[1]?.body?.get('file')).toBe(cover)
  })

  it('publishes with the authenticated media payload', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify(session), {
        headers: { 'content-type': 'application/json' },
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ video: { id: 100 } }), {
        headers: { 'content-type': 'application/json' },
      }))
    vi.stubGlobal('fetch', fetchMock)
    await login({ username: 'alice', password: 'password-123' })

    await publishVideo({
      title: '我的第一条视频',
      description: '视频介绍',
      play_url: '/static/videos/42/demo.mp4',
      play_file_name: 'demo.mp4',
      play_original_name: '我的视频.mp4',
      cover_url: '/static/covers/42/cover.png',
      cover_file_name: 'cover.png',
      cover_original_name: '封面.png',
    })

    expect(fetchMock).toHaveBeenLastCalledWith('/api/video/auth/publish', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ get: expect.any(Function) }),
      body: JSON.stringify({
        title: '我的第一条视频',
        description: '视频介绍',
        play_url: '/static/videos/42/demo.mp4',
        play_file_name: 'demo.mp4',
        play_original_name: '我的视频.mp4',
        cover_url: '/static/covers/42/cover.png',
        cover_file_name: 'cover.png',
        cover_original_name: '封面.png',
      }),
    }))
    const requestInit = fetchMock.mock.calls[1]?.[1]
    expect(new Headers(requestInit?.headers).get('Authorization')).toBe('Bearer access-token')
  })
})
