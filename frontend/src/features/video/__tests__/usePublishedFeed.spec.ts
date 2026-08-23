import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api'

import { listPublishedVideos, type VideoItem, type VideoListResponse } from '../api'
import { usePublishedFeed } from '../usePublishedFeed'

vi.mock('../api', () => ({
  listPublishedVideos: vi.fn<typeof listPublishedVideos>(),
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

describe('usePublishedFeed', () => {
  afterEach(() => {
    vi.resetAllMocks()
  })

  it('cancels a superseded first page and ignores its delayed response', async () => {
    const firstResponse = deferred<VideoListResponse>()
    const secondResponse = deferred<VideoListResponse>()
    const listMock = vi.mocked(listPublishedVideos)
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
    const listMock = vi.mocked(listPublishedVideos)
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

  it('keeps cancellation silent and preserves server errors for retry UI', async () => {
    const pendingResponse = deferred<VideoListResponse>()
    const listMock = vi.mocked(listPublishedVideos)
    listMock.mockReturnValueOnce(pendingResponse.promise)
    const feed = usePublishedFeed()

    const loading = feed.loadFirstPage()
    feed.dispose()
    pendingResponse.reject(Object.assign(new Error('cancelled'), { name: 'AbortError' }))
    await loading

    expect(feed.errorMessage.value).toBe('')
    expect(feed.videos.value).toEqual([])

    listMock.mockRejectedValueOnce(new ApiError(503, '服务暂不可用'))
    await feed.loadFirstPage()

    expect(feed.errorMessage.value).toBe('服务暂不可用')
    expect(feed.isInitialLoading.value).toBe(false)
    feed.dispose()
  })
})
