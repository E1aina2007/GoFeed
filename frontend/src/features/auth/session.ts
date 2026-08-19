import { computed, readonly, ref } from 'vue'

import { ApiError, request } from '@/lib/api'

const storageKey = 'gofeed.auth.session'

export type AuthUser = {
  id: number
  username: string
}

export type AuthSession = {
  access_token: string
  refresh_token: string
  expires_at: string
  user: AuthUser
}

export type LoginInput = {
  username: string
  password: string
}

export type RegisterInput = LoginInput

export type RegisterResponse = {
  user: AuthUser
}

function browserStorage(): Storage | undefined {
  if (typeof window === 'undefined') {
    return undefined
  }

  try {
    const storage = window.localStorage
    return typeof storage.getItem === 'function'
      && typeof storage.setItem === 'function'
      && typeof storage.removeItem === 'function'
      ? storage
      : undefined
  } catch {
    return undefined
  }
}

function readStoredSession(): AuthSession | null {
  const storage = browserStorage()
  if (!storage) {
    return null
  }

  const stored = storage.getItem(storageKey)
  if (!stored) {
    return null
  }

  try {
    const value: unknown = JSON.parse(stored)
    if (
      typeof value === 'object'
      && value !== null
      && 'access_token' in value
      && 'refresh_token' in value
      && 'expires_at' in value
      && 'user' in value
      && typeof value.access_token === 'string'
      && typeof value.refresh_token === 'string'
      && typeof value.expires_at === 'string'
      && typeof value.user === 'object'
      && value.user !== null
      && 'id' in value.user
      && 'username' in value.user
      && typeof value.user.id === 'number'
      && typeof value.user.username === 'string'
    ) {
      return value as AuthSession
    }
  } catch {
    // Ignore malformed local storage and require the user to sign in again.
  }

  storage.removeItem(storageKey)
  return null
}

const session = ref<AuthSession | null>(readStoredSession())
let refreshInFlight: Promise<AuthSession> | undefined

function persistSession(nextSession: AuthSession | null) {
  session.value = nextSession
  const storage = browserStorage()
  if (!storage) {
    return
  }

  if (nextSession) {
    storage.setItem(storageKey, JSON.stringify(nextSession))
  } else {
    storage.removeItem(storageKey)
  }
}

function jsonRequest<T>(path: string, body: unknown) {
  return request<T>(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export const currentSession = readonly(session)
export const currentUser = computed(() => session.value?.user ?? null)
export const isAuthenticated = computed(() => session.value !== null)

export async function login(input: LoginInput) {
  const nextSession = await jsonRequest<AuthSession>('/api/user/login', input)
  persistSession(nextSession)
  return nextSession
}

export function register(input: RegisterInput) {
  return jsonRequest<RegisterResponse>('/api/user/register', input)
}

export function clearSession() {
  persistSession(null)
}

async function refreshSession() {
  const refreshToken = session.value?.refresh_token
  if (!refreshToken) {
    throw new ApiError(401, '登录状态已失效，请重新登录')
  }

  if (!refreshInFlight) {
    refreshInFlight = jsonRequest<AuthSession>('/api/user/refresh', { refresh_token: refreshToken })
      .then((nextSession) => {
        persistSession(nextSession)
        return nextSession
      })
      .catch((error: unknown) => {
        clearSession()
        if (error instanceof ApiError) {
          throw new ApiError(401, '登录状态已失效，请重新登录')
        }
        throw error
      })
      .finally(() => {
        refreshInFlight = undefined
      })
  }

  return refreshInFlight
}

function withAuthorization(init: RequestInit, accessToken: string) {
  const headers = new Headers(init.headers)
  headers.set('Authorization', `Bearer ${accessToken}`)
  return { ...init, headers }
}

export async function withAuthenticatedSession<T>(operation: (accessToken: string) => Promise<T>): Promise<T> {
  const activeSession = session.value
  if (!activeSession) {
    throw new ApiError(401, '请先登录后再继续')
  }

  try {
    return await operation(activeSession.access_token)
  } catch (error) {
    if (!(error instanceof ApiError) || error.status !== 401) {
      throw error
    }
  }

  const refreshedSession = await refreshSession()
  return operation(refreshedSession.access_token)
}

export function authenticatedRequest<T>(path: string, init: RequestInit = {}) {
  return withAuthenticatedSession((accessToken) => request<T>(path, withAuthorization(init, accessToken)))
}

export async function logout() {
  if (!session.value) {
    return
  }

  try {
    await authenticatedRequest<null>('/api/user/auth/logout', { method: 'POST' })
  } finally {
    clearSession()
  }
}
