import { request } from '@/lib/api'

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

export type ListPublishedVideosOptions = {
  cursor?: string
  limit?: number
}

export function listPublishedVideos({ cursor, limit = 12 }: ListPublishedVideosOptions = {}) {
  const query = new URLSearchParams({ limit: String(limit) })
  if (cursor) {
    query.set('cursor', cursor)
  }
  return request<VideoListResponse>(`/api/video?${query.toString()}`)
}
