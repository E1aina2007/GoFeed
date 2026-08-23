import { computed, ref } from 'vue'

import { ApiError } from '@/lib/api'

import { listPublishedVideos, type VideoItem, type VideoListResponse } from './api'

function isAbortError(error: unknown) {
  return typeof error === 'object' && error !== null && 'name' in error && error.name === 'AbortError'
}

function requestErrorMessage(error: unknown) {
  return error instanceof ApiError ? error.message : '视频加载失败，请检查网络后重试'
}

function mergeVideos(current: VideoItem[], incoming: VideoItem[]) {
  const positions = new Map(current.map((video, index) => [video.id, index]))
  const merged = [...current]

  for (const video of incoming) {
    const position = positions.get(video.id)
    if (position === undefined) {
      positions.set(video.id, merged.length)
      merged.push(video)
      continue
    }
    merged[position] = video
  }

  return merged
}

export function usePublishedFeed() {
  const videos = ref<VideoItem[]>([])
  const nextCursor = ref<string>()
  const isInitialLoading = ref(true)
  const isLoadingMore = ref(false)
  const errorMessage = ref('')
  const hasMore = computed(() => Boolean(nextCursor.value))

  let generation = 0
  let initialController: AbortController | undefined
  let moreController: AbortController | undefined

  function ownsInitialRequest(controller: AbortController, requestGeneration: number) {
    return (
      initialController === controller
      && generation === requestGeneration
      && !controller.signal.aborted
    )
  }

  function ownsMoreRequest(controller: AbortController, requestGeneration: number, cursor: string) {
    return (
      moreController === controller
      && generation === requestGeneration
      && nextCursor.value === cursor
      && !controller.signal.aborted
    )
  }

  async function loadFirstPage(): Promise<VideoListResponse | undefined> {
    generation += 1
    const requestGeneration = generation
    initialController?.abort()
    moreController?.abort()

    const controller = new AbortController()
    initialController = controller
    moreController = undefined
    isInitialLoading.value = true
    isLoadingMore.value = false
    errorMessage.value = ''

    try {
      const response = await listPublishedVideos({ signal: controller.signal })
      if (!ownsInitialRequest(controller, requestGeneration)) {
        return undefined
      }

      videos.value = mergeVideos([], response.items)
      nextCursor.value = response.next_cursor
      return response
    } catch (error) {
      if (!ownsInitialRequest(controller, requestGeneration) || isAbortError(error)) {
        return undefined
      }

      videos.value = []
      nextCursor.value = undefined
      errorMessage.value = requestErrorMessage(error)
      return undefined
    } finally {
      if (initialController === controller) {
        initialController = undefined
        isInitialLoading.value = false
      }
    }
  }

  async function loadMore(): Promise<VideoListResponse | undefined> {
    const cursor = nextCursor.value
    if (!cursor || isInitialLoading.value || isLoadingMore.value) {
      return undefined
    }

    const requestGeneration = generation
    const controller = new AbortController()
    moreController = controller
    isLoadingMore.value = true
    errorMessage.value = ''

    try {
      const response = await listPublishedVideos({ cursor, signal: controller.signal })
      if (!ownsMoreRequest(controller, requestGeneration, cursor)) {
        return undefined
      }

      videos.value = mergeVideos(videos.value, response.items)
      nextCursor.value = response.next_cursor
      return response
    } catch (error) {
      if (!ownsMoreRequest(controller, requestGeneration, cursor) || isAbortError(error)) {
        return undefined
      }

      errorMessage.value = requestErrorMessage(error)
      return undefined
    } finally {
      if (moreController === controller) {
        moreController = undefined
        isLoadingMore.value = false
      }
    }
  }

  function dispose() {
    generation += 1
    initialController?.abort()
    moreController?.abort()
    initialController = undefined
    moreController = undefined
    isInitialLoading.value = false
    isLoadingMore.value = false
  }

  return {
    videos,
    nextCursor,
    isInitialLoading,
    isLoadingMore,
    errorMessage,
    hasMore,
    loadFirstPage,
    loadMore,
    dispose,
  }
}
