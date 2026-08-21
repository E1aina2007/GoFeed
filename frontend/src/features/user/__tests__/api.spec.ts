import { afterEach, describe, expect, it, vi } from 'vitest'

import { clearSession, currentUser, login } from '@/features/auth/session'
import { ApiError } from '@/lib/api'
import { deleteAccount, getUser, getUserProfile, listUsers, updateName, updatePassword, updateProfile } from '../api'

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

describe('user api', () => {
  afterEach(() => {
    clearSession()
    vi.unstubAllGlobals()
  })

  it('loads public user resources and validates IDs before requesting', async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ users: [{ id: 1, username: 'alice' }] }))
      .mockResolvedValueOnce(jsonResponse({ user: { id: 1, username: 'alice' } }))
      .mockResolvedValueOnce(jsonResponse({ account: { id: 1, username: 'alice' }, video_count: 2, total_likes: 0, follower_count: 0, vlogger_count: 0 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listUsers()).resolves.toEqual({ users: [{ id: 1, username: 'alice' }] })
    await expect(getUser(1)).resolves.toEqual({ user: { id: 1, username: 'alice' } })
    await expect(getUserProfile(1)).resolves.toMatchObject({ video_count: 2 })
    expect(() => getUser(0)).toThrow(new ApiError(400, '用户 ID 无效'))

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/user', expect.any(Object))
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/user/1', expect.any(Object))
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/user/1/profile', expect.any(Object))
  })

  it('updates the authenticated account and clears it after account deletion', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(session))
      .mockResolvedValueOnce(jsonResponse({ message: 'username updated successfully' }))
      .mockResolvedValueOnce(jsonResponse({ message: 'profile updated successfully' }))
      .mockResolvedValueOnce(jsonResponse({ message: 'password updated; sign in again' }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    await updateName('alice-new')
    await updateProfile({ bio: '新简介' })
    await expect(updatePassword('password-123', 'password-456')).resolves.toMatchObject({ message: expect.any(String) })
    expect(currentUser.value).toMatchObject({ username: 'alice-new', bio: '新简介' })
    await expect(deleteAccount()).resolves.toBeNull()

    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/user/auth/name', expect.objectContaining({ method: 'PATCH' }))
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/user/auth/profile', expect.objectContaining({ method: 'PATCH' }))
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/user/auth/password', expect.objectContaining({ method: 'PATCH' }))
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/user/auth', expect.objectContaining({ method: 'DELETE' }))
  })
})
