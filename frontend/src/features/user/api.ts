import { authenticatedRequest, updateCurrentUser, type AuthUser } from '@/features/auth/session'
import { ApiError, request } from '@/lib/api'

export type PublicUser = AuthUser & {
  avatar_url?: string
  bio?: string
}

export type UserProfile = {
  account: PublicUser
  video_count: number
  total_likes: number
  follower_count: number
  vlogger_count: number
}

export type UserListResponse = {
  users: PublicUser[]
}

function validateID(id: number) {
  if (!Number.isSafeInteger(id) || id <= 0) {
    throw new ApiError(400, '用户 ID 无效')
  }
}

export function listUsers() {
  return request<UserListResponse>('/api/user')
}

export function getUser(id: number) {
  validateID(id)
  return request<{ user: PublicUser }>(`/api/user/${id}`)
}

export function getUserProfile(id: number) {
  validateID(id)
  return request<UserProfile>(`/api/user/${id}/profile`)
}

export async function updateName(newUsername: string) {
  const response = await authenticatedRequest<{ message: string }>('/api/user/auth/name', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ new_username: newUsername }),
  })
  updateCurrentUser({ username: newUsername.trim() })
  return response
}

export function updatePassword(oldPassword: string, newPassword: string) {
  return authenticatedRequest<{ message: string }>('/api/user/auth/password', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ old_password: oldPassword, new_password: newPassword }),
  })
}

export async function updateProfile(profile: { avatar_url?: string; bio?: string }) {
  const response = await authenticatedRequest<{ message: string }>('/api/user/auth/profile', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(profile),
  })
  updateCurrentUser(profile)
  return response
}

export function deleteAccount() {
  return authenticatedRequest<null>('/api/user/auth', { method: 'DELETE' })
}
