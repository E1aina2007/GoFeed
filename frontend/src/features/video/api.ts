import { withAuthenticatedSession } from '@/features/auth/session'
import { ApiError, request } from '@/lib/api'

export type VideoAuthor = {
  id: number
  username: string
  avatar_url?: string
}

export type VideoItem = {
  id: number
  title: string
  description: string
  play_url: string
  play_file_name: string
  play_original_name: string
  cover_url: string
  cover_file_name: string
  cover_original_name: string
  published_at: string
  likes_count: number
  comments_count: number
  author: VideoAuthor
}

export type VideoListResponse = {
  items: VideoItem[]
  next_cursor?: string
}

type VideoResponse = {
  video: VideoItem
}

export type ListPublishedVideosOptions = {
  cursor?: string
  limit?: number
  authorID?: number
}

export type UploadedVideo = Pick<VideoItem, 'play_url' | 'play_file_name' | 'play_original_name'>

export type UploadedCover = Pick<VideoItem, 'cover_url' | 'cover_file_name' | 'cover_original_name'>

export type PublishVideoInput = {
  title: string
  description?: string
} & UploadedVideo & UploadedCover

type PublishVideoResponse = {
  video: VideoItem
}

export function listPublishedVideos({ cursor, limit = 12, authorID }: ListPublishedVideosOptions = {}) {
  const query = new URLSearchParams({ limit: String(limit) })
  if (cursor) {
    query.set('cursor', cursor)
  }
  if (authorID) {
    query.set('author_id', String(authorID))
  }
  return request<VideoListResponse>(`/api/video?${query.toString()}`)
}

export function getPublishedVideo(id: number) {
  return request<VideoResponse>(`/api/video/${id}`)
}

export function listMyVideos({ cursor, limit = 20 }: ListPublishedVideosOptions = {}) {
  const query = new URLSearchParams({ limit: String(limit) })
  if (cursor) {
    query.set('cursor', cursor)
  }
  return withAuthenticatedSession((accessToken) => request<VideoListResponse>(`/api/video/auth/mine?${query.toString()}`, {
    headers: { Authorization: `Bearer ${accessToken}` },
  }))
}

export function deleteVideo(id: number) {
  if (!Number.isSafeInteger(id) || id <= 0) {
    return Promise.reject(new ApiError(400, '视频 ID 无效'))
  }
  return withAuthenticatedSession((accessToken) => request<null>(`/api/video/auth/${id}`, {
    method: 'DELETE',
    headers: { Authorization: `Bearer ${accessToken}` },
  }))
}

function readUploadResponse<T>(xhr: XMLHttpRequest): T {
  let body: unknown = null
  try {
    body = xhr.responseText ? JSON.parse(xhr.responseText) : null
  } catch {
    body = null
  }

  if (xhr.status < 200 || xhr.status >= 300) {
    const message = typeof body === 'object' && body !== null && 'error' in body && typeof body.error === 'string'
      ? body.error
      : '上传失败，请稍后重试'
    throw new ApiError(xhr.status, message)
  }

  return body as T
}

function uploadMedia<T>(path: string, file: File, onProgress?: (progress: number) => void) {
  return withAuthenticatedSession((accessToken) => new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    const formData = new FormData()
    formData.append('file', file)

    xhr.open('POST', path)
    xhr.setRequestHeader('Accept', 'application/json')
    xhr.setRequestHeader('Authorization', `Bearer ${accessToken}`)
    xhr.upload.addEventListener('progress', (event) => {
      if (event.lengthComputable) {
        onProgress?.(event.loaded / event.total)
      }
    })
    xhr.addEventListener('load', () => {
      try {
        resolve(readUploadResponse<T>(xhr))
      } catch (error) {
        reject(error)
      }
    })
    xhr.addEventListener('error', () => reject(new ApiError(0, '网络连接失败，请检查网络后重试')))
    xhr.send(formData)
  }))
}

export function uploadVideo(file: File, onProgress?: (progress: number) => void) {
  return uploadMedia<UploadedVideo>('/api/video/auth/upload/video', file, onProgress)
}

export function uploadCover(file: File, onProgress?: (progress: number) => void) {
  return uploadMedia<UploadedCover>('/api/video/auth/upload/cover', file, onProgress)
}

export function publishVideo(input: PublishVideoInput) {
  return withAuthenticatedSession((accessToken) => request<PublishVideoResponse>('/api/video/auth/publish', {
    method: 'POST',
    headers: {
      'Authorization': `Bearer ${accessToken}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  }))
}
