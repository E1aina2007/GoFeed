import { computed, reactive, ref, watch } from 'vue'

import { currentSession, isAuthenticated } from '@/features/auth/session'
import { ApiError, apiUserMessage } from '@/lib/api'

import {
  listFollowingFeed,
  listTimelineFeed,
  type VideoItem,
  type VideoListResponse,
} from './api'

export type FeedScene = 'timeline' | 'following'

const feedRetryDelays = [300, 900] as const
const feedScenes = ['timeline', 'following'] as const

function isAbortError(error: unknown) {
  return (
    typeof error === 'object' && error !== null && 'name' in error && error.name === 'AbortError'
  )
}

function isRetryableFeedError(error: unknown) {
  if (!(error instanceof ApiError)) {
    return true
  }
  return error.status === 0 || error.status === 408 || error.status === 429 || error.status >= 500
}

function waitForRetry(signal: AbortSignal, delay: number) {
  return new Promise<void>((resolve) => {
    let settled = false

    function finish() {
      if (settled) {
        return
      }
      settled = true
      clearTimeout(timer)
      signal.removeEventListener('abort', finish)
      resolve()
    }

    const timer = setTimeout(finish, delay)
    signal.addEventListener('abort', finish, { once: true })
    if (signal.aborted) {
      finish()
    }
  })
}

function requestErrorMessage(error: unknown) {
  return apiUserMessage(error, '视频加载失败，请检查网络后重试', {
    400: '分页状态已失效，请重新加载',
  })
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

type SceneFeed = {
  videos: VideoItem[]
  nextCursor: string | undefined
  isInitialLoading: boolean
  isLoadingMore: boolean
  errorMessage: string
  loaded: boolean
}

function createSceneFeed(): SceneFeed {
  return {
    videos: [],
    nextCursor: undefined,
    isInitialLoading: false,
    isLoadingMore: false,
    errorMessage: '',
    loaded: false,
  }
}

type SceneRuntime = {
  generation: number
  initialController?: AbortController
  moreController?: AbortController
}

function createSceneRuntime(): SceneRuntime {
  return { generation: 0 }
}

// 管理最新与关注视频流的独立分页状态，避免旧请求覆盖当前列表
export function usePublishedFeed() {
  const scene = ref<FeedScene>('timeline')
  const scenes = {
    timeline: reactive(createSceneFeed()),
    following: reactive(createSceneFeed()),
  }
  const runtimes = {
    timeline: createSceneRuntime(),
    following: createSceneRuntime(),
  }

  const activeScene = computed(() => scenes[scene.value])
  const videos = computed(() => activeScene.value.videos)
  const nextCursor = computed(() => activeScene.value.nextCursor)
  const isInitialLoading = computed(() => activeScene.value.isInitialLoading)
  const isLoadingMore = computed(() => activeScene.value.isLoadingMore)
  const errorMessage = computed(() => activeScene.value.errorMessage)
  const hasMore = computed(() => Boolean(activeScene.value.nextCursor))
  const isSceneLoaded = computed(() => activeScene.value.loaded)

  // 观看者以 user ID 判定：token 刷新会替换会话对象但不改变 ID，不触发重置
  const viewerID = computed(() => currentSession.value?.user.id ?? null)

  // 取消指定场景的请求并使其迟到响应失效
  function abortScene(sceneName: FeedScene) {
    const runtime = runtimes[sceneName]
    const feed = scenes[sceneName]
    runtime.generation += 1
    runtime.initialController?.abort()
    runtime.moreController?.abort()
    runtime.initialController = undefined
    runtime.moreController = undefined
    feed.isInitialLoading = false
    feed.isLoadingMore = false
  }

  function resetScene(sceneName: FeedScene) {
    abortScene(sceneName)
    const feed = scenes[sceneName]
    feed.videos = []
    feed.nextCursor = undefined
    feed.errorMessage = ''
    feed.loaded = false
  }

  function ownsInitialRequest(
    sceneName: FeedScene,
    controller: AbortController,
    requestGeneration: number,
  ) {
    const runtime = runtimes[sceneName]
    return (
      runtime.initialController === controller
      && runtime.generation === requestGeneration
      && !controller.signal.aborted
    )
  }

  function ownsMoreRequest(
    sceneName: FeedScene,
    controller: AbortController,
    requestGeneration: number,
    cursor: string,
  ) {
    const runtime = runtimes[sceneName]
    return (
      runtime.moreController === controller
      && runtime.generation === requestGeneration
      && scenes[sceneName].nextCursor === cursor
      && !controller.signal.aborted
    )
  }

  function loadSceneFeed(sceneName: FeedScene, options: { cursor?: string; signal: AbortSignal }) {
    if (sceneName === 'following') {
      return listFollowingFeed(options)
    }
    return listTimelineFeed(options)
  }

  // 重新加载指定场景首屏，有限重试并丢弃失效请求的结果
  async function loadSceneFirstPage(sceneName: FeedScene): Promise<VideoListResponse | undefined> {
    // 未登录时 Following 不发请求，由页面展示登录入口
    if (sceneName === 'following' && !isAuthenticated.value) {
      return undefined
    }

    const feed = scenes[sceneName]
    abortScene(sceneName)
    const requestGeneration = runtimes[sceneName].generation

    const controller = new AbortController()
    runtimes[sceneName].initialController = controller
    feed.isInitialLoading = true
    feed.errorMessage = ''

    try {
      for (let retry = 0; ; retry += 1) {
        try {
          const response = await loadSceneFeed(sceneName, { signal: controller.signal })
          if (!ownsInitialRequest(sceneName, controller, requestGeneration)) {
            return undefined
          }

          feed.videos = mergeVideos([], response.items)
          feed.nextCursor = response.next_cursor
          feed.loaded = true
          return response
        } catch (error) {
          if (!ownsInitialRequest(sceneName, controller, requestGeneration) || isAbortError(error)) {
            return undefined
          }

          const retryDelay = feedRetryDelays[retry]
          if (retryDelay !== undefined && isRetryableFeedError(error)) {
            await waitForRetry(controller.signal, retryDelay)
            if (!ownsInitialRequest(sceneName, controller, requestGeneration)) {
              return undefined
            }
            continue
          }

          feed.videos = []
          feed.nextCursor = undefined
          feed.loaded = false
          feed.errorMessage = requestErrorMessage(error)
          return undefined
        }
      }
    } finally {
      if (runtimes[sceneName].initialController === controller) {
        runtimes[sceneName].initialController = undefined
        feed.isInitialLoading = false
      }
    }
  }

  // 去重追加指定场景的下一页，游标失效时允许从首屏重试
  async function loadSceneMore(sceneName: FeedScene): Promise<VideoListResponse | undefined> {
    const feed = scenes[sceneName]
    const cursor = feed.nextCursor
    if (!cursor || feed.isInitialLoading || feed.isLoadingMore) {
      return undefined
    }

    const requestGeneration = runtimes[sceneName].generation
    const controller = new AbortController()
    runtimes[sceneName].moreController = controller
    feed.isLoadingMore = true
    feed.errorMessage = ''

    try {
      for (let retry = 0; ; retry += 1) {
        try {
          const response = await loadSceneFeed(sceneName, { cursor, signal: controller.signal })
          if (!ownsMoreRequest(sceneName, controller, requestGeneration, cursor)) {
            return undefined
          }

          feed.videos = mergeVideos(feed.videos, response.items)
          feed.nextCursor = response.next_cursor
          return response
        } catch (error) {
          if (
            !ownsMoreRequest(sceneName, controller, requestGeneration, cursor)
            || isAbortError(error)
          ) {
            return undefined
          }

          const retryDelay = feedRetryDelays[retry]
          if (retryDelay !== undefined && isRetryableFeedError(error)) {
            await waitForRetry(controller.signal, retryDelay)
            if (!ownsMoreRequest(sceneName, controller, requestGeneration, cursor)) {
              return undefined
            }
            continue
          }

          if (error instanceof ApiError && error.status === 400) {
            // 游标已被服务端判定失效：清空后重试只能重新加载首屏，不再复用旧游标
            feed.nextCursor = undefined
          }
          feed.errorMessage = requestErrorMessage(error)
          return undefined
        }
      }
    } finally {
      if (runtimes[sceneName].moreController === controller) {
        runtimes[sceneName].moreController = undefined
        feed.isLoadingMore = false
      }
    }
  }

  // 切换视频流并取消旧请求，重新进入关注流时刷新列表
  function setScene(next: FeedScene) {
    if (scene.value === next) {
      return
    }
    const previous = scene.value
    abortScene(previous)
    if (previous === 'following') {
      // 关注关系可能在离开期间变化：每次进入 Following 都重新读取服务端列表
      scenes.following.loaded = false
    }
    scene.value = next
  }

  function loadFirstPage() {
    return loadSceneFirstPage(scene.value)
  }

  function loadMore() {
    return loadSceneMore(scene.value)
  }

  const stopViewerWatch = watch(viewerID, () => {
    // 退出或更换观看者：作废两个场景的在途请求与缓存，由页面重新加载当前场景
    resetScene('timeline')
    resetScene('following')
  })

  function dispose() {
    stopViewerWatch()
    for (const sceneName of feedScenes) {
      abortScene(sceneName)
    }
  }

  return {
    scene,
    videos,
    nextCursor,
    isInitialLoading,
    isLoadingMore,
    errorMessage,
    hasMore,
    isSceneLoaded,
    setScene,
    loadFirstPage,
    loadMore,
    dispose,
  }
}
