import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { defineComponent, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, RouterView, type Router } from 'vue-router'

import FeedView from '../FeedView.vue'

// FeedView 的整合行为用例：这里直接在 fetch 层构造首屏/分页响应，
// 用来验证页面与 usePublishedFeed 协作后的分页、重试、失效首屏和离开路由取消
const videoItem = {
  id: 1,
  title: '首屏视频',
  description: '第一条公开视频',
  play_url: '/static/videos/1/first.mp4',
  play_file_name: 'first.mp4',
  play_original_name: 'first.mp4',
  cover_url: '/static/covers/1/first.jpg',
  cover_file_name: 'first.jpg',
  cover_original_name: 'first.jpg',
  published_at: '2026-08-18T12:00:00+08:00',
  likes_count: 0,
  comments_count: 0,
  author: { id: 1, username: 'first-author', avatar_url: '' },
}

function videoWithID(id: number, title: string) {
  return {
    ...videoItem,
    id,
    title,
    play_url: `/static/videos/${id}/play.mp4`,
    cover_url: `/static/covers/${id}/cover.jpg`,
    author: { id, username: `user-${id}`, avatar_url: '' },
  }
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

type PendingResponse = {
  signal: AbortSignal | undefined
  resolve: (body: unknown) => void
}

function createFeedFetch() {
  const calls: string[] = []
  const pending: PendingResponse[] = []

  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    const request = input instanceof Request ? input : undefined
    const url = typeof input === 'string' ? input : (request?.url ?? String(input))
    calls.push(url)
    if (request || !url.startsWith('/api/feed?scene=timeline&limit=12')) {
      return new Response(null, { status: 404 })
    }

    return new Promise<Response>((resolve) => {
      pending.push({
        signal: init?.signal ?? undefined,
        resolve: (body: unknown) => resolve(jsonResponse(body)),
      })
    })
  })

  return { calls, fetchMock, pending }
}

const cleanupCallbacks: Array<() => void> = []

// 真实路由挂载：FeedView 由 RouterView 渲染，路由切换才能真正触发组件卸载
const RouteHost = defineComponent({
  name: 'RouteHost',
  components: { RouterView },
  template: '<RouterView />',
})

async function mountFeed(path = '/'): Promise<{ router: Router; wrapper: VueWrapper }> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'feed', component: FeedView },
      { path: '/users/:id', name: 'user-profile', component: { template: '<div />' } },
      { path: '/video/:id', name: 'video-detail', component: { template: '<div />' } },
    ],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(RouteHost, { global: { plugins: [createPinia(), router] } })
  cleanupCallbacks.push(() => {
    if (wrapper.exists()) {
      wrapper.unmount()
    }
  })
  return { router, wrapper }
}

function feedStream(wrapper: VueWrapper) {
  return wrapper.get('main.short-feed')
}

function scrollToBottom(wrapper: VueWrapper) {
  const stream = feedStream(wrapper)
  const element = stream.element as HTMLElement
  Object.defineProperty(element, 'scrollHeight', { configurable: true, value: 3000 })
  Object.defineProperty(element, 'clientHeight', { configurable: true, value: 800 })
  Object.defineProperty(element, 'scrollTop', { configurable: true, value: 2900, writable: true })
  return stream.trigger('scroll')
}

describe('FeedView 整合行为', () => {
  beforeEach(() => {
    vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue(undefined)
    vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => undefined)
  })

  afterEach(() => {
    while (cleanupCallbacks.length) {
      cleanupCallbacks.pop()?.()
    }
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('滚动到底部加载第二页并按 ID 追加，不重复渲染重叠视频', async () => {
    const firstVideo = videoWithID(1, '首屏标题')
    const secondVideo = videoWithID(2, '第二页视频')
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ items: [firstVideo], next_cursor: 'page-2' }))
      .mockResolvedValueOnce(
        jsonResponse({ items: [{ ...firstVideo, title: '更新后的首屏标题' }, secondVideo] }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(1)
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/feed?scene=timeline&limit=12')

    await scrollToBottom(wrapper)
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('cursor=page-2')
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/feed?scene=timeline&limit=12&cursor=page-2')
    const cards = feedStream(wrapper).findAll('.short-video')
    expect(cards).toHaveLength(2)
    expect(cards[0]?.text()).toContain('更新后的首屏标题')
    expect(cards[1]?.text()).toContain('第二页视频')
    expect(wrapper.text()).toContain('已经到底了')
  })

  it('末页没有 next_cursor 时滚动到底部不再请求，并展示到底提示', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValue(jsonResponse({ items: [videoWithID(1, '唯一一页')] }))
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()

    await scrollToBottom(wrapper)
    await flushPromises()
    await scrollToBottom(wrapper)
    await flushPromises()

    // 缺少 next_cursor 表示没有下一页，滚动不得触发请求
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('已经到底了')
  })

  it('失效的首屏响应不能覆盖已经开始的新加载', async () => {
    let resolveStale!: (body: unknown) => void
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveStale = (body: unknown) => resolve(jsonResponse(body))
          }),
      )
      .mockResolvedValueOnce(jsonResponse({ items: [videoWithID(2, '新首屏标题')] }))
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    const staleSignal = fetchMock.mock.calls[0]?.[1]?.signal

    // 第一次加载仍在途，第二次加载已经完成并渲染
    const feedView = wrapper.findComponent(FeedView).vm as unknown as {
      loadFirstPage: () => Promise<void>
    }
    await feedView.loadFirstPage()
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('新首屏标题')

    // 迟到的旧响应此时才返回另一组视频：既不能覆盖新结果，也不能清空列表
    resolveStale({ items: [videoWithID(3, '过期首屏标题')] })
    await flushPromises()

    expect(staleSignal?.aborted).toBe(true)
    const cards = feedStream(wrapper).findAll('.short-video')
    expect(cards).toHaveLength(1)
    expect(cards[0]?.text()).toContain('新首屏标题')
    expect(wrapper.text()).not.toContain('过期首屏标题')
  })

  it('首屏错误后展示错误态与重试按钮，重试成功后渲染视频', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ error: 'invalid cursor' }, 400))
      .mockResolvedValueOnce(jsonResponse({ items: [videoWithID(1, '重试成功的视频')] }))
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()

    // 400 不可重试，界面必须立刻给出错误提示而不是一直停在骨架屏
    const errorSection = wrapper.get('section.feed-message[role="alert"]')
    expect(errorSection.text()).toContain('分页状态已失效，请重新加载')
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(0)

    await errorSection.get('button').trigger('click')
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).not.toContain('分页状态已失效，请重新加载')
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(1)
    expect(wrapper.text()).toContain('重试成功的视频')
  })

  it('分页错误时保留首屏并重试同一游标，失败不丢已加载内容', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ items: [videoWithID(1, '首屏视频')], next_cursor: 'page-2' }))
      .mockResolvedValueOnce(jsonResponse({ error: 'invalid cursor' }, 400))
      .mockResolvedValueOnce(jsonResponse({ items: [videoWithID(2, '第二页视频')] }))
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    await scrollToBottom(wrapper)
    await flushPromises()

    // 分页失败只影响追加：首屏卡片仍在，错误态带重试
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(1)
    const errorStatus = feedStream(wrapper).get('.stream-status--error')
    expect(errorStatus.text()).toContain('分页状态已失效，请重新加载')

    await errorStatus.get('button').trigger('click')
    await flushPromises()

    // 重试必须继续同一页，而不是重新拉首屏
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(String(fetchMock.mock.calls[2]?.[0])).toContain('cursor=page-2')
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(2)
    expect(wrapper.text()).toContain('第二页视频')
  })

  it('分页失败后滚动保留错误态，点击重试才请求同一游标', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ items: [videoWithID(1, '首屏视频')], next_cursor: 'page-2' }))
      .mockResolvedValueOnce(jsonResponse({ error: 'invalid cursor' }, 400))
      .mockResolvedValueOnce(jsonResponse({ items: [videoWithID(2, '第二页视频')] }))
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    await scrollToBottom(wrapper)
    await flushPromises()

    expect(feedStream(wrapper).get('.stream-status--error').text()).toContain('分页状态已失效')

    await scrollToBottom(wrapper)
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(1)
    const errorStatus = feedStream(wrapper).get('.stream-status--error')
    expect(errorStatus.text()).toContain('分页状态已失效')
    await errorStatus.get('button').trigger('click')
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(String(fetchMock.mock.calls[2]?.[0])).toContain('cursor=page-2')
    expect(wrapper.text()).not.toContain('分页状态已失效')
    expect(wrapper.text()).toContain('第二页视频')
  })

  it('空列表渲染空态且不显示重试入口', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ items: [] })),
    )

    const { wrapper } = await mountFeed()
    await flushPromises()

    const message = wrapper.get('section.feed-message')
    expect(message.text()).toContain('暂时没有公开视频')
    expect(message.find('button').exists()).toBe(false)
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(0)
  })

  it('离开路由时中断在途首屏请求并丢弃迟到结果', async () => {
    const pending = createFeedFetch()
    vi.stubGlobal('fetch', pending.fetchMock)

    const { router, wrapper } = await mountFeed()
    const inflight = pending.pending[0]
    if (!inflight) {
      throw new Error('expected an in-flight feed request')
    }
    expect(inflight.signal?.aborted).toBe(false)

    await router.push('/video/1')
    await flushPromises()
    await nextTick()
    await flushPromises()

    // 离开 Feed 路由触发组件卸载，在途请求必须被真正取消
    expect(inflight.signal?.aborted).toBe(true)

    inflight.resolve({ items: [videoWithID(9, '迟到视频')] })
    await flushPromises()

    // 卸载后不再渲染任何内容，也不会把结果写回已销毁的页面
    expect(wrapper.findAll('.short-video')).toHaveLength(0)
    expect(wrapper.text()).not.toContain('迟到视频')
  })
})
