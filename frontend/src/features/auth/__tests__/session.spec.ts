import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  authenticatedRequest,
  clearSession,
  currentUser,
  isAuthenticated,
  login,
  register,
} from '../session'

const signedInSession = {
  access_token: 'first-access-token',
  refresh_token: 'first-refresh-token',
  expires_at: '2026-08-26T08:00:00Z',
  user: { id: 42, username: 'alice' },
}

function installStorage() {
  const data = new Map<string, string>()
  const storage: Storage = {
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => data.set(key, value),
    removeItem: (key) => data.delete(key),
    clear: () => data.clear(),
    key: () => null,
    get length() {
      return data.size
    },
  }
  Object.defineProperty(window, 'localStorage', { configurable: true, value: storage })
  return storage
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

describe('auth session', () => {
  beforeEach(() => {
    installStorage()
    clearSession()
  })

  afterEach(() => {
    clearSession()
    vi.unstubAllGlobals()
  })

  it('persists the token pair returned by login', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(signedInSession))
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })

    expect(fetchMock).toHaveBeenCalledWith('/api/user/login', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ username: 'alice', password: 'password-123' }),
    }))
    expect(isAuthenticated.value).toBe(true)
    expect(currentUser.value).toEqual({ id: 42, username: 'alice' })
    expect(window.localStorage.getItem('gofeed.auth.session')).toContain('first-access-token')
  })

  it('refreshes once and retries a protected request after a 401 response', async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(signedInSession))
      .mockResolvedValueOnce(jsonResponse({ error: 'invalid or expired token' }, 401))
      .mockResolvedValueOnce(jsonResponse({
        ...signedInSession,
        access_token: 'second-access-token',
        refresh_token: 'second-refresh-token',
      }))
      .mockResolvedValueOnce(jsonResponse({ ok: true }))
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    await expect(authenticatedRequest<{ ok: boolean }>('/api/video/auth/drafts/7/publish', { method: 'POST' }))
      .resolves.toEqual({ ok: true })

    const firstProtectedRequest = fetchMock.mock.calls[1]
    const refreshRequest = fetchMock.mock.calls[2]
    const retriedRequest = fetchMock.mock.calls[3]
    expect(firstProtectedRequest?.[1]).toEqual(expect.objectContaining({
      headers: expect.objectContaining({ get: expect.any(Function) }),
    }))
    expect(new Headers(firstProtectedRequest?.[1]?.headers).get('Authorization')).toBe('Bearer first-access-token')
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/user/refresh', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ refresh_token: 'first-refresh-token' }),
    }))
    expect(new Headers(retriedRequest?.[1]?.headers).get('Authorization')).toBe('Bearer second-access-token')
    expect(refreshRequest?.[0]).toBe('/api/user/refresh')
  })

  it('registers with the public user endpoint without creating a session', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      user: { id: 43, username: 'new-user' },
    }, 201))
    vi.stubGlobal('fetch', fetchMock)

    await expect(register({ username: 'new-user', password: 'password-123' }))
      .resolves.toEqual({ user: { id: 43, username: 'new-user' } })

    expect(fetchMock).toHaveBeenCalledWith('/api/user/register', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ username: 'new-user', password: 'password-123' }),
    }))
    expect(isAuthenticated.value).toBe(false)
  })
})
