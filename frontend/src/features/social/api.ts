import { authenticatedRequest } from '@/features/auth/session'
import type { PublicUser } from '@/features/user/api'
import { ApiError, request } from '@/lib/api'

export type LikeState = {
  liked: boolean
  likes_count: number
}

export type FollowState = {
  following: boolean
  follower_count: number
}

export type CommentItem = {
  id: number
  video_id: number
  author: PublicUser
  content: string
  created_at: string
}

export type CommentListResponse = {
  items: CommentItem[]
  next_cursor?: string
}

export type FollowListItem = {
  user: PublicUser
  followed_at: string
}

export type FollowListResponse = {
  items: FollowListItem[]
  next_cursor?: string
}

export type ListOptions = {
  cursor?: string
  limit?: number
}

function validateID(id: number, label: string) {
  if (!Number.isSafeInteger(id) || id <= 0) {
    throw new ApiError(400, `${label} ID 无效`)
  }
}

function listQuery({ cursor, limit = 20 }: ListOptions) {
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 50) {
    throw new ApiError(400, '分页大小无效')
  }

  const query = new URLSearchParams({ limit: String(limit) })
  if (cursor) {
    query.set('cursor', cursor)
  }
  return query.toString()
}

function validateCommentContent(content: string) {
  const normalized = content.trim()
  if (!normalized || normalized.length > 1000) {
    throw new ApiError(400, '评论内容需为 1-1000 个字符')
  }
  return normalized
}

export function getLikeState(videoID: number) {
  validateID(videoID, '视频')
  return authenticatedRequest<LikeState>(`/api/video/auth/${videoID}/like`)
}

export function createLike(videoID: number) {
  validateID(videoID, '视频')
  return authenticatedRequest<LikeState>(`/api/video/auth/${videoID}/like`, { method: 'PUT' })
}

export function removeLike(videoID: number) {
  validateID(videoID, '视频')
  return authenticatedRequest<LikeState>(`/api/video/auth/${videoID}/like`, { method: 'DELETE' })
}

export function getCommentList(videoID: number, options: ListOptions = {}) {
  validateID(videoID, '视频')
  return request<CommentListResponse>(`/api/video/${videoID}/comments?${listQuery(options)}`)
}

export function createComment(videoID: number, content: string) {
  validateID(videoID, '视频')
  return authenticatedRequest<{ comment: CommentItem }>(`/api/video/auth/${videoID}/comments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content: validateCommentContent(content) }),
  })
}

export function removeComment(videoID: number, commentID: number) {
  validateID(videoID, '视频')
  validateID(commentID, '评论')
  return authenticatedRequest<null>(`/api/video/auth/${videoID}/comments/${commentID}`, {
    method: 'DELETE',
  })
}

export function getFollowState(userID: number) {
  validateID(userID, '用户')
  return authenticatedRequest<FollowState>(`/api/user/auth/${userID}/follow`)
}

export function createFollow(userID: number) {
  validateID(userID, '用户')
  return authenticatedRequest<FollowState>(`/api/user/auth/${userID}/follow`, { method: 'PUT' })
}

export function removeFollow(userID: number) {
  validateID(userID, '用户')
  return authenticatedRequest<FollowState>(`/api/user/auth/${userID}/follow`, { method: 'DELETE' })
}

export function getFollowerList(userID: number, options: ListOptions = {}) {
  validateID(userID, '用户')
  return request<FollowListResponse>(`/api/user/${userID}/followers?${listQuery(options)}`)
}

export function getFollowingList(userID: number, options: ListOptions = {}) {
  validateID(userID, '用户')
  return request<FollowListResponse>(`/api/user/${userID}/following?${listQuery(options)}`)
}
