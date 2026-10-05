import { afterEach, describe, expect, it, vi } from 'vitest'

import { clearSession, currentUser, login } from '@/features/auth/session'
import { deleteAccount, updateName, updatePassword, updateProfile, uploadAvatar } from '../api'

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

  it('updates the authenticated account and clears it after account deletion', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(session))
      .mockResolvedValueOnce(jsonResponse({ message: 'username updated successfully' }))
      .mockResolvedValueOnce(jsonResponse({ message: 'profile updated successfully' }))
      .mockResolvedValueOnce(jsonResponse({ message: 'password updated; sign in again' }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    await updateName('alice-new')
    await updateProfile({ bio: '新简介' })
    await expect(updatePassword('password-123', 'password-456')).resolves.toMatchObject({
      message: expect.any(String),
    })
    expect(currentUser.value).toMatchObject({ username: 'alice-new', bio: '新简介' })
    await expect(deleteAccount()).resolves.toBeNull()

    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/user/auth/name',
      expect.objectContaining({ method: 'PATCH' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/user/auth/profile',
      expect.objectContaining({ method: 'PATCH' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      '/api/user/auth/password',
      expect.objectContaining({ method: 'PATCH' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      5,
      '/api/user/auth',
      expect.objectContaining({ method: 'DELETE' }),
    )
  })

  it('uploads an avatar as multipart data and updates the session path', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const avatar = new File(['png'], 'avatar.png', { type: 'image/png' })
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(session))
      .mockResolvedValueOnce(jsonResponse({ avatar_url: '/static/avatars/42/20260824/avatar.png' }))
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    await expect(uploadAvatar(avatar)).resolves.toEqual({
      avatar_url: '/static/avatars/42/20260824/avatar.png',
    })

    const init = fetchMock.mock.calls[1]?.[1]
    if (!init) {
      throw new Error('avatar upload request was not sent')
    }
    expect(init.method).toBe('POST')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer access-token')
    expect(init.body).toBeInstanceOf(FormData)
    expect((init.body as FormData).get('file')).toBe(avatar)
    expect(currentUser.value?.avatar_url).toBe('/static/avatars/42/20260824/avatar.png')
  })
})
