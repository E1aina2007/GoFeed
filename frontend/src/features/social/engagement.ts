import { reactive } from 'vue'

import type { LikeState } from './api'

export type VideoEngagement = {
  liked: boolean
  likesCount: number
  commentsCount: number
}

const videoEngagement = reactive<Record<number, VideoEngagement>>({})

function normalizedCount(value: number) {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function validVideoID(videoID: number) {
  return Number.isSafeInteger(videoID) && videoID > 0
}

export function getVideoEngagement(videoID: number, likesCount = 0, commentsCount = 0) {
  if (!validVideoID(videoID)) {
    return undefined
  }

  const current = videoEngagement[videoID]
  if (current) {
    return current
  }

  const next = {
    liked: false,
    likesCount: normalizedCount(likesCount),
    commentsCount: normalizedCount(commentsCount),
  }
  videoEngagement[videoID] = next
  return next
}

export function updateVideoLikeState(videoID: number, state: LikeState) {
  const engagement = getVideoEngagement(videoID, state.likes_count)
  if (!engagement) {
    return
  }
  engagement.liked = state.liked
  engagement.likesCount = normalizedCount(state.likes_count)
}

export function updateVideoCommentCount(videoID: number, commentsCount: number) {
  const engagement = getVideoEngagement(videoID, 0, commentsCount)
  if (!engagement) {
    return
  }
  engagement.commentsCount = normalizedCount(commentsCount)
}

export function changeVideoCommentCount(videoID: number, delta: number) {
  const engagement = getVideoEngagement(videoID)
  if (!engagement) {
    return
  }
  engagement.commentsCount = normalizedCount(engagement.commentsCount + delta)
}
