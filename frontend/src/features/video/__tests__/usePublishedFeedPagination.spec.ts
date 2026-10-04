import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api'

import { listFollowingFeed, listTimelineFeed, type VideoItem, type VideoListResponse } from '../api'
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
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

describe('usePublishedFeed 分页与取消', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.resetAllMocks()
  })

  it('第二页请求携带游标并追加到首屏之后，同时推进游标', async () => {
    const listMock = vi.mocked(listTimelineFeed)
    listMock
      .mockResolvedValueOnce(response([video(1), video(2)], 'page-2'))
      .mockResolvedValueOnce(response([video(3)], 'page-3'))
    const feed = usePublishedFeed()

    const firstPage = await feed.loadFirstPage()
    expect(firstPage?.next_cursor).toBe('page-2')
    expect(listMock.mock.calls[0]?.[0]).toEqual({ signal: expect.any(AbortSignal) })

    // 第二页必须带上首屏游标，否则会重复拿到第一页
    const secondPage = await feed.loadMore()

    expect(listMock).toHaveBeenCalledTimes(2)
    expect(listMock.mock.calls[1]?.[0]).toMatchObject({ cursor: 'page-2' })
    expect(secondPage?.items).toEqual([video(3)])
    expect(feed.videos.value).toEqual([video(1), video(2), video(3)])
    expect(feed.nextCursor.value).toBe('page-3')
    expect(feed.hasMore.value).toBe(true)
    feed.dispose()
  })

  it('重新加载首屏不携带上一轮游标，空页结束分页', async () => {
    const listMock = vi.mocked(listTimelineFeed)
    listMock
      .mockResolvedValueOnce(response([video(1)], 'feed-only+/= &'))
      .mockResolvedValueOnce(response([], 'next'))
      .mockResolvedValueOnce(response([]))
    const feed = usePublishedFeed()
    await feed.loadFirstPage()
    await feed.loadMore()
    expect(listMock.mock.calls[1]?.[0]?.cursor).toBe('feed-only+/= &')
    await feed.loadFirstPage()
    expect(listMock.mock.calls[2]?.[0]).toEqual({ signal: expect.any(AbortSignal) })
    expect(feed.videos.value).toEqual([])
    expect(feed.hasMore.value).toBe(false)
    await feed.loadMore()
    expect(listMock).toHaveBeenCalledTimes(3)
    feed.dispose()
  })

  it('末页缺少 next_cursor 时结束分页并拒绝再次请求', async () => {
    const listMock = vi.mocked(listTimelineFeed)
    listMock
      .mockResolvedValueOnce(response([video(1)], 'page-2'))
      .mockResolvedValueOnce(response([video(2)]))
    const feed = usePublishedFeed()

    await feed.loadFirstPage()
    const lastPage = await feed.loadMore()

    // 末页响应可以省略 next_cursor 字段，界面因此没有下一页
    expect(lastPage).toEqual({ items: [video(2)] })
    expect(feed.nextCursor.value).toBeUndefined()
    expect(feed.hasMore.value).toBe(false)
    expect(feed.videos.value).toEqual([video(1), video(2)])

    // 没有游标时不得发出第三次请求
    const exhausted = await feed.loadMore()
    expect(exhausted).toBeUndefined()
    expect(listMock).toHaveBeenCalledTimes(2)
    feed.dispose()
  })

  it('分页遇到 400 时保留已加载视频并清空失效游标', async () => {
    const listMock = vi.mocked(listTimelineFeed)
    listMock
      .mockResolvedValueOnce(response([video(1), video(2)], 'page-2'))
      .mockRejectedValueOnce(new ApiError(400, 'invalid cursor'))
    const feed = usePublishedFeed()

    await feed.loadFirstPage()
    await feed.loadMore()

    expect(listMock).toHaveBeenCalledTimes(2)
    expect(feed.errorMessage.value).toBe('分页状态已失效，请重新加载')
    // 已渲染的内容不能被清空，但游标已被服务端判定失效，必须清空不再复用
    expect(feed.videos.value).toEqual([video(1), video(2)])
    expect(feed.nextCursor.value).toBeUndefined()
    expect(feed.hasMore.value).toBe(false)
    expect(feed.isLoadingMore.value).toBe(false)

    // 游标已清空：滚动触发的 loadMore 不得再发请求，重试只能重新加载首屏
    await feed.loadMore()
    expect(listMock).toHaveBeenCalledTimes(2)

    listMock.mockResolvedValueOnce(response([video(3)], 'page-3'))
    await feed.loadFirstPage()

    expect(listMock.mock.calls[2]?.[0]).toEqual({ signal: expect.any(AbortSignal) })
    expect(feed.errorMessage.value).toBe('')
    expect(feed.videos.value).toEqual([video(3)])
    expect(feed.nextCursor.value).toBe('page-3')
    feed.dispose()
  })

  it('并发分页共用同一游标，按视频 ID 去重后保持首屏顺序', async () => {
    const secondPage = deferred<VideoListResponse>()
    const listMock = vi.mocked(listTimelineFeed)
    listMock
      .mockResolvedValueOnce(response([video(1), video(2), video(3)], 'page-2'))
      .mockReturnValueOnce(secondPage.promise)
    const feed = usePublishedFeed()

    await feed.loadFirstPage()
    const firstMore = feed.loadMore()
    const concurrentMore = feed.loadMore()

    // 进行中的分页必须吸收同一游标的重复触发
    expect(listMock).toHaveBeenCalledTimes(2)
    expect(listMock.mock.calls[1]?.[0]).toMatchObject({ cursor: 'page-2' })

    // 第二页与首屏存在重叠 ID，去重后不能出现重复卡片
    secondPage.resolve(response([video(2), video(4)], 'page-3'))
    await Promise.all([firstMore, concurrentMore])

    expect(feed.videos.value.map((item) => item.id)).toEqual([1, 2, 3, 4])
    expect(new Set(feed.videos.value.map((item) => item.id)).size).toBe(4)
    expect(feed.nextCursor.value).toBe('page-3')
    expect(feed.isLoadingMore.value).toBe(false)
    feed.dispose()
  })

  it('销毁时真实中断在途请求，迟到的响应不会写回状态', async () => {
    const pending = deferred<VideoListResponse>()
    const listMock = vi.mocked(listTimelineFeed)
    listMock.mockReturnValueOnce(pending.promise)
    const feed = usePublishedFeed()

    const loading = feed.loadFirstPage()
    const signal = listMock.mock.calls[0]?.[0]?.signal
    expect(signal?.aborted).toBe(false)

    feed.dispose()

    // 离开路由必须真正 abort 掉在途请求，而不是只靠回调里的标记
    expect(signal?.aborted).toBe(true)

    // 请求已经返回时也不能再写入已销毁的页面状态
    pending.resolve(response([video(1)], 'page-2'))
    await loading

    expect(feed.videos.value).toEqual([])
    expect(feed.nextCursor.value).toBeUndefined()
    expect(feed.isInitialLoading.value).toBe(false)
  })

  it('分页被销毁后不写回视频，也不留下加载态', async () => {
    const pending = deferred<VideoListResponse>()
    const listMock = vi.mocked(listTimelineFeed)
    listMock
      .mockResolvedValueOnce(response([video(1)], 'page-2'))
      .mockReturnValueOnce(pending.promise)
    const feed = usePublishedFeed()

    await feed.loadFirstPage()
    const loading = feed.loadMore()
    const signal = listMock.mock.calls[1]?.[0]?.signal
    feed.dispose()
    expect(signal?.aborted).toBe(true)

    pending.resolve(response([video(2)], 'page-3'))
    await loading

    expect(feed.videos.value).toEqual([video(1)])
    expect(feed.nextCursor.value).toBe('page-2')
    expect(feed.isLoadingMore.value).toBe(false)
  })
})
