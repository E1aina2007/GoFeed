import { afterEach, describe, expect, it, vi } from 'vitest'

import { clearSession, login } from '@/features/auth/session'
import {
  createComment,
  createFollow,
  createLike,
  getFollowState,
  getLikeState,
  removeComment,
  removeFollow,
  removeLike,
} from '../api'

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

const session = {
  access_token: 'access-token',
  refresh_token: 'refresh-token',
  expires_at: '2026-08-26T08:00:00Z',
  user: { id: 42, username: 'alice' },
}

describe('social api', () => {
  afterEach(() => {
    clearSession()
    vi.unstubAllGlobals()
  })

  it('uses authenticated routes for like, comment, and follow changes', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(session))
      .mockResolvedValueOnce(jsonResponse({ liked: false, likes_count: 3 }))
      .mockResolvedValueOnce(jsonResponse({ liked: true, likes_count: 4 }))
      .mockResolvedValueOnce(jsonResponse({ liked: false, likes_count: 3 }))
      .mockResolvedValueOnce(
        jsonResponse(
          {
            comment: {
              id: 12,
              video_id: 7,
              author: { id: 42, username: 'alice' },
              content: '很精彩',
              created_at: '2026-08-26T08:00:00Z',
            },
          },
          201,
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(jsonResponse({ following: false, follower_count: 8 }))
      .mockResolvedValueOnce(jsonResponse({ following: true, follower_count: 9 }))
      .mockResolvedValueOnce(jsonResponse({ following: false, follower_count: 8 }))
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    await expect(getLikeState(7)).resolves.toEqual({ liked: false, likes_count: 3 })
    await expect(createLike(7)).resolves.toEqual({ liked: true, likes_count: 4 })
    await expect(removeLike(7)).resolves.toEqual({ liked: false, likes_count: 3 })
    await expect(createComment(7, ' 很精彩 ')).resolves.toMatchObject({ comment: { id: 12 } })
    await expect(removeComment(7, 12)).resolves.toBeNull()
    await expect(getFollowState(8)).resolves.toEqual({ following: false, follower_count: 8 })
    await expect(createFollow(8)).resolves.toEqual({ following: true, follower_count: 9 })
    await expect(removeFollow(8)).resolves.toEqual({ following: false, follower_count: 8 })

    const likeRequest = fetchMock.mock.calls[2]?.[1]
    const commentRequest = fetchMock.mock.calls[4]?.[1]
    const followRequest = fetchMock.mock.calls[8]?.[1]
    expect(new Headers(likeRequest?.headers).get('Authorization')).toBe('Bearer access-token')
    expect(likeRequest?.method).toBe('PUT')
    expect(commentRequest?.method).toBe('POST')
    expect(commentRequest?.body).toBe(JSON.stringify({ content: '很精彩' }))
    expect(followRequest?.method).toBe('DELETE')
  })
})
