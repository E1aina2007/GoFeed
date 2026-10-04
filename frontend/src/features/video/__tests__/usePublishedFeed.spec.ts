import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api'
import { clearSession, login } from '@/features/auth/session'

import {
  listFollowingFeed,
  listTimelineFeed,
  type VideoItem,
  type VideoListResponse,
} from '../api'
import { usePublishedFeed } from '../usePublishedFeed'

vi.mock('../api', () => ({
  listTimelineFeed: vi.fn<typeof listTimelineFeed>(),
  listFollowingFeed: vi.fn<typeof listFollowingFeed>(),
}))

function video(id: number, title = `视频 ${id}`): VideoItem {
  return {
    id,
    title,
    description: '',
    play_url: `/static/videos/${id}/play.mp4`,
    play_file_name: 'play.mp4',
    play_original_name: 'play.mp4',
    cover_url: `/static/covers/${id}/cover.jpg`,
    cover_file_name: 'cover.jpg',
    cover_original_name: 'cover.jpg',
    published_at: '2026-08-23T08:00:00Z',
    likes_count: 0,
    comments_count: 0,
    author: { id, username: `user-${id}` },
  }
}

function response(items: VideoItem[], nextCursor?: string): VideoListResponse {
  return { items, next_cursor: nextCursor }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function signInAs(id: number, username: string) {
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>().mockResolvedValue(
      new Response(
        JSON.stringify({
          access_token: `token-${id}`,
          refresh_token: `refresh-${id}`,
          expires_at: '2027-01-01T00:00:00Z',
          user: { id, username },
        }),
        { headers: { 'content-type': 'application/json' } },
      ),
    ),
  )
  await login({ username, password: 'password-123' })
  vi.unstubAllGlobals()
}

describe('usePublishedFeed', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.resetAllMocks()
    vi.unstubAllGlobals()
    clearSession()
  })

  it('cancels a superseded first page and ignores its delayed response', async () => {
    const firstResponse = deferred<VideoListResponse>()
    const secondResponse = deferred<VideoListResponse>()
    const listMock = vi.mocked(listTimelineFeed)
    listMock.mockReturnValueOnce(firstResponse.promise).mockReturnValueOnce(secondResponse.promise)
    const feed = usePublishedFeed()

    const firstLoad = feed.loadFirstPage()
    const firstSignal = listMock.mock.calls[0]?.[0]?.signal
    const secondLoad = feed.loadFirstPage()

    expect(firstSignal?.aborted).toBe(true)

    secondResponse.resolve(response([video(2)], 'next-page'))
    await secondLoad
    firstResponse.resolve(response([video(1)]))
    await firstLoad

    expect(feed.videos.value).toEqual([video(2)])
    expect(feed.nextCursor.value).toBe('next-page')
    expect(feed.errorMessage.value).toBe('')
    expect(feed.isInitialLoading.value).toBe(false)
    feed.dispose()
  })

  it('allows one request per cursor and merges overlapping pages by video ID', async () => {
    const nextPage = deferred<VideoListResponse>()
    const listMock = vi.mocked(listTimelineFeed)
    listMock
      .mockResolvedValueOnce(response([video(7, '首屏标题')], 'next-page'))
      .mockReturnValueOnce(nextPage.promise)
    const feed = usePublishedFeed()

    await feed.loadFirstPage()
    const firstMore = feed.loadMore()
    const duplicateMore = feed.loadMore()

    expect(listMock).toHaveBeenCalledTimes(2)
    expect(listMock).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: 'next-page' }))

    nextPage.resolve(response([video(7, '更新后的标题'), video(8)], undefined))
    await Promise.all([firstMore, duplicateMore])

    expect(feed.videos.value).toEqual([video(7, '更新后的标题'), video(8)])
    expect(feed.nextCursor.value).toBeUndefined()
    expect(feed.isLoadingMore.value).toBe(false)
    feed.dispose()
  })

  it('retries transient first-page failures and recovers without exposing an error', async () => {
    vi.useFakeTimers()
    const listMock = vi.mocked(listTimelineFeed)
    listMock
      .mockRejectedValueOnce(new ApiError(503, 'engagement stats temporarily unavailable'))
      .mockResolvedValueOnce(response([video(3)], 'next-page'))
    const feed = usePublishedFeed()

    const loading = feed.loadFirstPage()
    await Promise.resolve()
    await vi.runAllTimersAsync()
    await loading

    expect(listMock).toHaveBeenCalledTimes(2)
    expect(feed.videos.value).toEqual([video(3)])
    expect(feed.nextCursor.value).toBe('next-page')
    expect(feed.errorMessage.value).toBe('')
    feed.dispose()
  })

  it('stops retrying after the bounded recovery attempts are exhausted', async () => {
    vi.useFakeTimers()
    const listMock = vi.mocked(listTimelineFeed)
    listMock.mockRejectedValue(new ApiError(503, 'engagement stats temporarily unavailable'))
    const feed = usePublishedFeed()

    const loading = feed.loadFirstPage()
    await Promise.resolve()
    await vi.runAllTimersAsync()
    await loading

    expect(listMock).toHaveBeenCalledTimes(3)
    expect(feed.errorMessage.value).toBe('服务暂时不可用，请稍后重试')
    expect(feed.isInitialLoading.value).toBe(false)
    feed.dispose()
  })

  it('retries a transient pagination failure with the original cursor', async () => {
    vi.useFakeTimers()
    const listMock = vi.mocked(listTimelineFeed)
    listMock
      .mockResolvedValueOnce(response([video(7, '首屏标题')], 'next-page'))
      .mockRejectedValueOnce(new ApiError(503, 'engagement stats temporarily unavailable'))
      .mockResolvedValueOnce(response([video(7, '更新后的标题'), video(8)]))
    const feed = usePublishedFeed()

    await feed.loadFirstPage()
    const loading = feed.loadMore()
    await Promise.resolve()
    await vi.runAllTimersAsync()
    await loading

    expect(listMock).toHaveBeenCalledTimes(3)
    expect(listMock).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: 'next-page' }))
    expect(feed.videos.value).toEqual([video(7, '更新后的标题'), video(8)])
    expect(feed.errorMessage.value).toBe('')
    feed.dispose()
  })

  it('cancels a scheduled retry when the feed is disposed', async () => {
    vi.useFakeTimers()
    const listMock = vi.mocked(listTimelineFeed)
    listMock.mockRejectedValueOnce(new ApiError(503, 'engagement stats temporarily unavailable'))
    const feed = usePublishedFeed()

    const loading = feed.loadFirstPage()
    await Promise.resolve()
    feed.dispose()
    await vi.runAllTimersAsync()
    await loading

    expect(listMock).toHaveBeenCalledTimes(1)
    expect(feed.errorMessage.value).toBe('')
  })

  it('keeps cancellation silent and preserves non-retryable errors for retry UI', async () => {
    const pendingResponse = deferred<VideoListResponse>()
    const listMock = vi.mocked(listTimelineFeed)
    listMock.mockReturnValueOnce(pendingResponse.promise)
    const feed = usePublishedFeed()

    const loading = feed.loadFirstPage()
    feed.dispose()
    pendingResponse.reject(Object.assign(new Error('cancelled'), { name: 'AbortError' }))
    await loading

    expect(feed.errorMessage.value).toBe('')
    expect(feed.videos.value).toEqual([])

    listMock.mockRejectedValueOnce(new ApiError(400, 'invalid cursor'))
    await feed.loadFirstPage()

    expect(feed.errorMessage.value).toBe('分页状态已失效，请重新加载')
    expect(feed.isInitialLoading.value).toBe(false)
    feed.dispose()
  })

  it('clears an invalid cursor after a 400 pagination failure so retry reloads the first page', async () => {
    const listMock = vi.mocked(listTimelineFeed)
    listMock
      .mockResolvedValueOnce(response([video(1)], 'bad-cursor'))
      .mockRejectedValueOnce(new ApiError(400, 'invalid feed cursor'))
      .mockResolvedValueOnce(response([video(2)]))
    const feed = usePublishedFeed()

    await feed.loadFirstPage()
    await feed.loadMore()

    expect(feed.nextCursor.value).toBeUndefined()
    expect(feed.errorMessage.value).toBe('分页状态已失效，请重新加载')
    expect(feed.videos.value).toEqual([video(1)])

    await feed.loadFirstPage()

    expect(listMock.mock.calls[2]?.[0]?.cursor).toBeUndefined()
    expect(feed.videos.value).toEqual([video(2)])
    expect(feed.errorMessage.value).toBe('')
    feed.dispose()
  })

  it('does not request the following feed while signed out', async () => {
    const followingMock = vi.mocked(listFollowingFeed)
    const feed = usePublishedFeed()

    feed.setScene('following')
    await expect(feed.loadFirstPage()).resolves.toBeUndefined()

    expect(followingMock).not.toHaveBeenCalled()
    expect(feed.errorMessage.value).toBe('')
    expect(feed.isInitialLoading.value).toBe(false)
    feed.dispose()
  })

  it('keeps scene lists, cursors and requests isolated between Timeline and Following', async () => {
    await signInAs(7, 'alice')
    const listMock = vi.mocked(listTimelineFeed)
    const followingMock = vi.mocked(listFollowingFeed)
    listMock.mockResolvedValue(response([video(1)], 'timeline-cursor'))
    followingMock.mockResolvedValue(response([video(2)], 'following-cursor'))
    const feed = usePublishedFeed()

    await feed.loadFirstPage()
    feed.setScene('following')
    await feed.loadFirstPage()

    expect(feed.videos.value).toEqual([video(2)])
    expect(feed.nextCursor.value).toBe('following-cursor')
    expect(followingMock).toHaveBeenCalledWith(
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )

    feed.setScene('timeline')

    expect(feed.videos.value).toEqual([video(1)])
    expect(feed.nextCursor.value).toBe('timeline-cursor')
    feed.dispose()
  })

  it('aborts the previous scene request on switch and discards its late response', async () => {
    await signInAs(7, 'alice')
    const pendingTimeline = deferred<VideoListResponse>()
    const listMock = vi.mocked(listTimelineFeed)
    const followingMock = vi.mocked(listFollowingFeed)
    listMock.mockReturnValueOnce(pendingTimeline.promise)
    followingMock.mockResolvedValue(response([video(2)]))
    const feed = usePublishedFeed()

    const timelineLoad = feed.loadFirstPage()
    const timelineSignal = listMock.mock.calls[0]?.[0]?.signal

    feed.setScene('following')
    await feed.loadFirstPage()
    pendingTimeline.resolve(response([video(9)]))
    await timelineLoad

    expect(timelineSignal?.aborted).toBe(true)
    expect(feed.videos.value).toEqual([video(2)])

    feed.setScene('timeline')
    expect(feed.videos.value).toEqual([])
    feed.dispose()
  })

  it('reloads the following list from the server on every entry to the scene', async () => {
    await signInAs(7, 'alice')
    const listMock = vi.mocked(listTimelineFeed)
    const followingMock = vi.mocked(listFollowingFeed)
    listMock.mockResolvedValue(response([video(1)], 'timeline-cursor'))
    followingMock
      .mockResolvedValueOnce(response([video(2)], 'following-cursor'))
      .mockResolvedValueOnce(response([video(3)]))
    const feed = usePublishedFeed()

    await feed.loadFirstPage()
    feed.setScene('following')
    await feed.loadFirstPage()
    feed.setScene('timeline')
    feed.setScene('following')
    await feed.loadFirstPage()

    expect(followingMock).toHaveBeenCalledTimes(2)
    expect(followingMock.mock.calls.every((call) => call[0]?.cursor === undefined)).toBe(true)
    expect(feed.videos.value).toEqual([video(3)])
    feed.dispose()
  })

  it('retries transient following failures without dropping the loaded list', async () => {
    vi.useFakeTimers()
    await signInAs(7, 'alice')
    const followingMock = vi.mocked(listFollowingFeed)
    followingMock
      .mockResolvedValueOnce(response([video(5)], 'following-cursor'))
      .mockRejectedValueOnce(new ApiError(503, 'feed temporarily unavailable'))
      .mockResolvedValueOnce(response([video(6)]))
    const feed = usePublishedFeed()

    feed.setScene('following')
    await feed.loadFirstPage()
    const loading = feed.loadMore()
    await Promise.resolve()
    await vi.runAllTimersAsync()
    await loading

    expect(followingMock).toHaveBeenCalledTimes(3)
    expect(followingMock).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'following-cursor' }),
    )
    expect(feed.videos.value).toEqual([video(5), video(6)])
    expect(feed.errorMessage.value).toBe('')
    feed.dispose()
  })

  it('surfaces the authentication message when the following feed finally fails with 401', async () => {
    await signInAs(7, 'alice')
    const followingMock = vi.mocked(listFollowingFeed)
    followingMock.mockRejectedValue(new ApiError(401, '登录状态已失效，请重新登录'))
    const feed = usePublishedFeed()

    feed.setScene('following')
    await feed.loadFirstPage()

    expect(feed.errorMessage.value).toBe('登录状态已失效，请重新登录')
    feed.dispose()
  })

  it('resets scene state when the viewer changes but survives a same-user token refresh', async () => {
    const listMock = vi.mocked(listTimelineFeed)
    listMock.mockResolvedValue(response([video(1)], 'timeline-cursor'))
    await signInAs(1, 'alice')
    const feed = usePublishedFeed()

    await feed.loadFirstPage()
    expect(feed.videos.value).toEqual([video(1)])

    // 同一用户重新登录（token 刷新）：user ID 不变，列表与游标保留
    await signInAs(1, 'alice')
    expect(feed.videos.value).toEqual([video(1)])
    expect(feed.nextCursor.value).toBe('timeline-cursor')

    // 更换用户：在途请求作废，两个场景状态清空，迟到响应被丢弃
    const pending = deferred<VideoListResponse>()
    listMock.mockReturnValueOnce(pending.promise)
    const reloading = feed.loadFirstPage()
    const reloadSignal = listMock.mock.calls[1]?.[0]?.signal

    await signInAs(2, 'bob')
    expect(reloadSignal?.aborted).toBe(true)

    pending.resolve(response([video(9)]))
    await reloading

    expect(feed.videos.value).toEqual([])
    expect(feed.nextCursor.value).toBeUndefined()
    feed.dispose()
  })
})
