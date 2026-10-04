import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { defineComponent, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, RouterView, type Router } from 'vue-router'

import { clearSession, login } from '@/features/auth/session'

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

const authSession = {
  access_token: 'integration-access-token',
  refresh_token: 'integration-refresh-token',
  expires_at: '2027-01-01T00:00:00Z',
  user: { id: 42, username: 'integration-user' },
}

function requestURL(input: Parameters<typeof fetch>[0]) {
  return typeof input === 'string' ? input : (input instanceof Request ? input.url : String(input))
}

// 登录接口走真实 session 模块；调用后由用例自行替换 fetch 以分流 Feed 请求
async function signIn() {
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      return new Response(null, { status: 404 })
    }),
  )
  await login({ username: 'integration-user', password: 'password-123' })
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
      { path: '/login', name: 'login', component: { template: '<div />' } },
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

// 用可记录赋值的 scrollTop 属性跟踪视图对滚动容器的程序化写入
function trackScrollTop(
  wrapper: VueWrapper,
  dimensions: { scrollHeight: number; clientHeight: number },
) {
  const element = feedStream(wrapper).element as HTMLElement
  const assignments: number[] = []
  let current = 0
  Object.defineProperty(element, 'scrollHeight', { configurable: true, value: dimensions.scrollHeight })
  Object.defineProperty(element, 'clientHeight', { configurable: true, value: dimensions.clientHeight })
  Object.defineProperty(element, 'scrollTop', {
    configurable: true,
    get: () => current,
    set: (value: number) => {
      assignments.push(value)
      current = value
    },
  })
  return {
    assignments,
    current: () => current,
    scrollTo(value: number) {
      current = value
      element.dispatchEvent(new Event('scroll'))
    },
  }
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
    clearSession()
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

  it('分页 400 清空失效游标后，重试重新加载首屏且不再复用旧游标', async () => {
    let firstPageLoads = 0
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url.includes('cursor=')) {
        return jsonResponse({ error: 'invalid cursor' }, 400)
      }
      firstPageLoads += 1
      if (firstPageLoads === 1) {
        return jsonResponse({
          items: [videoWithID(1, '首屏视频')],
          next_cursor: 'page-2',
        })
      }
      return jsonResponse({ items: [videoWithID(2, '重新加载的首屏')] })
    })
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    await scrollToBottom(wrapper)
    await flushPromises()

    // 400 只影响追加：首屏卡片仍在，错误态带重试
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(1)
    const errorStatus = feedStream(wrapper).get('.stream-status--error')
    expect(errorStatus.text()).toContain('分页状态已失效，请重新加载')

    await errorStatus.get('button').trigger('click')
    await flushPromises()

    // 重试重新加载首屏，而不是复用已被服务端判定的失效游标
    const pagedCalls = fetchMock.mock.calls.map(([input]) => requestURL(input)).filter((url) =>
      url.includes('cursor='),
    )
    expect(pagedCalls).toHaveLength(1)
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(1)
    expect(wrapper.text()).toContain('重新加载的首屏')
    expect(wrapper.text()).not.toContain('分页状态已失效')
  })

  it('分页 400 清空游标后，滚动不再触发分页请求，重试前保留错误态', async () => {
    let firstPageLoads = 0
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url.includes('cursor=')) {
        return jsonResponse({ error: 'invalid cursor' }, 400)
      }
      firstPageLoads += 1
      if (firstPageLoads === 1) {
        return jsonResponse({
          items: [videoWithID(1, '首屏视频')],
          next_cursor: 'page-2',
        })
      }
      return jsonResponse({ items: [videoWithID(2, '重新加载的首屏')] })
    })
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    await scrollToBottom(wrapper)
    await flushPromises()

    expect(feedStream(wrapper).get('.stream-status--error').text()).toContain('分页状态已失效')
    const pagedCalls = () =>
      fetchMock.mock.calls.map(([input]) => requestURL(input)).filter((url) => url.includes('cursor='))
    expect(pagedCalls()).toHaveLength(1)

    // 游标已被清空：继续滚动不得再产生分页请求
    await scrollToBottom(wrapper)
    await flushPromises()
    expect(pagedCalls()).toHaveLength(1)
    expect(feedStream(wrapper).get('.stream-status--error')).toBeTruthy()

    await feedStream(wrapper).get('.stream-status--error').get('button').trigger('click')
    await flushPromises()

    expect(pagedCalls()).toHaveLength(1)
    expect(wrapper.text()).not.toContain('分页状态已失效')
    expect(wrapper.text()).toContain('重新加载的首屏')
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

  it('切换到关注场景时中断在途的 Timeline 首屏请求', async () => {
    let resolveTimeline!: (response: Response) => void
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.includes('scene=timeline')) {
        return new Promise<Response>((resolve) => {
          resolveTimeline = resolve
        })
      }
      if (url.includes('scene=following')) {
        return jsonResponse({ items: [videoWithID(20, '关注首屏')] })
      }
      return new Response(null, { status: 404 })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    const timelineSignal = fetchMock.mock.calls
      .map(([, init]) => init?.signal)
      .find((signal) => signal instanceof AbortSignal)

    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()

    expect(timelineSignal?.aborted).toBe(true)
    expect(wrapper.text()).toContain('关注首屏')

    // 迟到的 Timeline 响应不应再影响页面
    resolveTimeline(jsonResponse({ items: [videoWithID(9, '迟到视频')] }))
    await flushPromises()
    expect(wrapper.text()).toContain('关注首屏')
    expect(wrapper.text()).not.toContain('迟到视频')
  })

  it('关注场景用独立游标分页，切回 Timeline 复用缓存不重新请求', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.includes('scene=following')) {
        if (url.includes('cursor=')) {
          return jsonResponse({ items: [videoWithID(21, '关注第二页')] })
        }
        return jsonResponse({ items: [videoWithID(20, '关注首屏')], next_cursor: 'following-page-2' })
      }
      if (url.includes('scene=timeline')) {
        return jsonResponse({ items: [videoWithID(1, '首屏视频')] })
      }
      return new Response(null, { status: 404 })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(1)

    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('关注首屏')
    await scrollToBottom(wrapper)
    await flushPromises()
    expect(wrapper.text()).toContain('关注第二页')

    const followingCalls = fetchMock.mock.calls
      .map(([input]) => requestURL(input))
      .filter((url) => url.includes('scene=following'))
    expect(followingCalls).toEqual([
      '/api/feed?scene=following&limit=12',
      '/api/feed?scene=following&limit=12&cursor=following-page-2',
    ])
    for (const [input, init] of fetchMock.mock.calls.filter(([callInput]) =>
      requestURL(callInput).includes('scene=following'),
    )) {
      expect(String(input)).toBe(followingCalls.shift())
      expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer integration-access-token')
    }

    await wrapper.findAll('.feed-tab')[0]!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('首屏视频')
    const timelineCalls = fetchMock.mock.calls
      .map(([input]) => requestURL(input))
      .filter((url) => url.includes('scene=timeline'))
    expect(timelineCalls).toHaveLength(1)
  })

  it('关注场景 401 后通过会话恢复重试，重试携带新令牌与同一游标', async () => {
    let followingAttempts = 0
    const refreshedSession = { ...authSession, access_token: 'integration-access-token-2' }
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url === '/api/user/refresh') {
        return jsonResponse(refreshedSession)
      }
      if (url.includes('scene=following')) {
        followingAttempts += 1
        if (followingAttempts === 1) {
          return jsonResponse({ error: 'token expired' }, 401)
        }
        return jsonResponse({ items: [videoWithID(20, '关注首屏')] })
      }
      if (url.includes('scene=timeline')) {
        return jsonResponse({ items: [videoWithID(1, '首屏视频')] })
      }
      return new Response(null, { status: 404 })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('关注首屏')
    const followingCalls = fetchMock.mock.calls.filter(([input]) =>
      requestURL(input).includes('scene=following'),
    )
    expect(followingCalls).toHaveLength(2)
    expect(new Headers(followingCalls[0]?.[1]?.headers).get('Authorization')).toBe(
      'Bearer integration-access-token',
    )
    expect(new Headers(followingCalls[1]?.[1]?.headers).get('Authorization')).toBe(
      'Bearer integration-access-token-2',
    )
  })

  it('切换场景与切回缓存场景时滚动容器都回到顶部', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.includes('scene=following')) {
        return jsonResponse({
          items: [videoWithID(20, '关注首屏'), videoWithID(21, '关注第二条')],
        })
      }
      if (url.includes('scene=timeline')) {
        return jsonResponse({
          items: [videoWithID(1, '首屏视频'), videoWithID(2, '第二条'), videoWithID(3, '第三条')],
        })
      }
      return new Response(null, { status: 404 })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    expect(feedStream(wrapper).findAll('.short-video')).toHaveLength(3)

    const tracker = trackScrollTop(wrapper, { scrollHeight: 3600, clientHeight: 800 })
    tracker.scrollTo(2200)
    expect(tracker.current()).toBe(2200)

    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()

    // 切换到 Following：滚动容器被显式重置为顶部，第一张关注卡片渲染
    expect(tracker.assignments).toContain(0)
    expect(tracker.current()).toBe(0)
    expect(wrapper.text()).toContain('关注首屏')

    tracker.scrollTo(1800)
    await wrapper.findAll('.feed-tab')[0]!.trigger('click')
    await flushPromises()

    expect(tracker.current()).toBe(0)
    expect(wrapper.text()).toContain('首屏视频')
    // Timeline 切回使用缓存：除两个场景的首屏外没有新的 Feed 请求
    const feedCalls = fetchMock.mock.calls
      .map(([input]) => requestURL(input))
      .filter((url) => url.includes('scene='))
    expect(feedCalls).toEqual([
      '/api/feed?scene=timeline&limit=12',
      '/api/feed?scene=following&limit=12',
    ])
  })

  it('更换观看者后重新加载当前场景并回到顶部', async () => {
    const secondSession = { ...authSession, user: { id: 43, username: 'another-user' } }
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(secondSession)
      }
      if (url.includes('scene=timeline')) {
        return jsonResponse({ items: [videoWithID(1, '首屏视频')] })
      }
      return new Response(null, { status: 404 })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    expect(wrapper.text()).toContain('首屏视频')
    const tracker = trackScrollTop(wrapper, { scrollHeight: 3000, clientHeight: 800 })
    tracker.scrollTo(1500)

    // 更换用户：hook 作废两个场景，页面重新加载并回到顶部
    await login({ username: 'another-user', password: 'password-123' })
    await flushPromises()

    expect(tracker.assignments).toContain(0)
    expect(tracker.current()).toBe(0)
    const timelineCalls = fetchMock.mock.calls
      .map(([input]) => requestURL(input))
      .filter((url) => url.includes('scene=timeline'))
    expect(timelineCalls).toHaveLength(2)
    expect(wrapper.text()).toContain('首屏视频')
  })

  it('分页 400 后重试回到顶部并重新加载首屏', async () => {
    let firstPageLoads = 0
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url.includes('cursor=')) {
        return jsonResponse({ error: 'invalid cursor' }, 400)
      }
      firstPageLoads += 1
      if (firstPageLoads === 1) {
        return jsonResponse({ items: [videoWithID(1, '首屏视频')], next_cursor: 'page-2' })
      }
      return jsonResponse({ items: [videoWithID(2, '重新加载的首屏')] })
    })
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    const tracker = trackScrollTop(wrapper, { scrollHeight: 3000, clientHeight: 800 })
    tracker.scrollTo(2200)
    await flushPromises()

    const errorStatus = feedStream(wrapper).get('.stream-status--error')
    expect(errorStatus.text()).toContain('分页状态已失效')
    const assignmentsBeforeRetry = tracker.assignments.length

    await errorStatus.get('button').trigger('click')
    await flushPromises()

    // 重试清空旧分页位置：重新加载首屏且滚动容器回到顶部
    expect(tracker.assignments.slice(assignmentsBeforeRetry)).toContain(0)
    expect(tracker.current()).toBe(0)
    expect(wrapper.text()).toContain('重新加载的首屏')
    const pagedCalls = fetchMock.mock.calls
      .map(([input]) => requestURL(input))
      .filter((url) => url.includes('cursor='))
    expect(pagedCalls).toHaveLength(1)
  })

  it('正常追加分页保持滚动位置，不回到顶部', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        jsonResponse({ items: [videoWithID(1, '首屏视频')], next_cursor: 'page-2' }),
      )
      .mockResolvedValueOnce(
        jsonResponse({ items: [videoWithID(2, '第二页视频')], next_cursor: 'page-3' }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    const tracker = trackScrollTop(wrapper, { scrollHeight: 3000, clientHeight: 800 })
    tracker.scrollTo(2200)
    await flushPromises()

    expect(wrapper.text()).toContain('第二页视频')
    // 追加分页不触发任何回到顶部的滚动赋值
    expect(tracker.assignments).not.toContain(0)
    expect(tracker.current()).toBe(2200)
  })

  it('同一观看者的会话刷新不重置滚动与列表', async () => {
    // 从既有 mock 会话派生新令牌：仅替换会话对象，观看者不变，等价于 token 刷新
    const refreshedSession = { ...authSession, access_token: `${authSession.access_token}-2` }
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(refreshedSession)
      }
      if (url.includes('scene=timeline')) {
        return jsonResponse({ items: [videoWithID(1, '首屏视频')] })
      }
      return new Response(null, { status: 404 })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    const tracker = trackScrollTop(wrapper, { scrollHeight: 3000, clientHeight: 800 })
    tracker.scrollTo(1500)

    await login({ username: 'integration-user', password: 'password-123' })
    await flushPromises()

    expect(tracker.assignments).toEqual([])
    expect(tracker.current()).toBe(1500)
    expect(wrapper.text()).toContain('首屏视频')
    const timelineCalls = fetchMock.mock.calls
      .map(([input]) => requestURL(input))
      .filter((url) => url.includes('scene=timeline'))
    expect(timelineCalls).toHaveLength(1)
  })

  it('快速连续切换场景时迟到的响应与回调不影响当前场景', async () => {
    let resolveFollowing!: (response: Response) => void
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.includes('scene=following')) {
        return new Promise<Response>((resolve) => {
          resolveFollowing = resolve
        })
      }
      if (url.includes('scene=timeline')) {
        return jsonResponse({ items: [videoWithID(1, '首屏视频')] })
      }
      return new Response(null, { status: 404 })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    const tracker = trackScrollTop(wrapper, { scrollHeight: 3000, clientHeight: 800 })
    tracker.scrollTo(1500)

    // 切到 Following 且首屏在途，随即切回 Timeline
    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()
    expect(resolveFollowing).toBeTypeOf('function')
    await wrapper.findAll('.feed-tab')[0]!.trigger('click')
    await flushPromises()

    const followingSignal = fetchMock.mock.calls
      .map(([, init]) => init?.signal)
      .find((signal) => signal instanceof AbortSignal && signal.aborted)
    expect(followingSignal).toBeTruthy()
    expect(wrapper.text()).toContain('首屏视频')

    // 迟到的 Following 响应不能改写当前场景内容或滚动位置
    resolveFollowing(jsonResponse({ items: [videoWithID(20, '迟到的关注视频')] }))
    await flushPromises()

    expect(wrapper.text()).not.toContain('迟到的关注视频')
    expect(wrapper.text()).toContain('首屏视频')
    expect(tracker.current()).toBe(0)
  })

  it('路由提交前的反向点击最终服从最后一次选择', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      // Following 首屏挂起即可：无论是否发出，最终场景都不得是 Following
      if (url.includes('scene=following')) {
        return new Promise<Response>(() => {})
      }
      if (url.includes('scene=timeline')) {
        return jsonResponse({ items: [videoWithID(1, '首屏视频')] })
      }
      return new Response(null, { status: 404 })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { router, wrapper } = await mountFeed()
    await flushPromises()
    expect(wrapper.text()).toContain('首屏视频')

    // 挂起后续导航，制造「replace 已发起但尚未提交」的窗口
    let releaseGate!: () => void
    const gate = new Promise<void>((resolve) => {
      releaseGate = resolve
    })
    const removeGate = router.beforeEach(() => gate)

    // 点击关注（导航挂起）后立刻反向点击最新
    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await wrapper.findAll('.feed-tab')[0]!.trigger('click')

    releaseGate()
    await flushPromises()
    await flushPromises()
    removeGate()

    // 最终场景必须服从最后一次点击：留在 Timeline，不得落到 Following
    expect(router.currentRoute.value.query.scene).toBeUndefined()
    expect(wrapper.findAll('.feed-tab')[0]?.attributes('aria-pressed')).toBe('true')
    expect(wrapper.text()).toContain('首屏视频')
    expect(wrapper.text()).not.toContain('关注首屏')
  })

  it('外部路由变化与前进后退时页面场景与 URL 保持一致', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.includes('scene=following')) {
        return jsonResponse({ items: [videoWithID(20, '关注首屏')] })
      }
      if (url.includes('scene=timeline')) {
        return jsonResponse({ items: [videoWithID(1, '首屏视频')] })
      }
      return new Response(null, { status: 404 })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { router, wrapper } = await mountFeed()
    await flushPromises()

    // 外部路由变化（push 产生新历史条目）进入 Following
    await router.push('/?scene=following')
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe('/?scene=following')
    expect(wrapper.text()).toContain('关注首屏')

    await router.back()
    await flushPromises()
    expect(wrapper.findAll('.feed-tab')[0]?.attributes('aria-pressed')).toBe('true')
    expect(wrapper.text()).toContain('首屏视频')

    await router.forward()
    await flushPromises()
    expect(wrapper.findAll('.feed-tab')[1]?.attributes('aria-pressed')).toBe('true')
    expect(wrapper.text()).toContain('关注首屏')
  })

  it('关注场景 400 清空失效游标，重试重新加载首屏', async () => {
    let firstPageLoads = 0
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = requestURL(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.includes('scene=following')) {
        if (url.includes('cursor=')) {
          return jsonResponse({ error: 'invalid feed cursor' }, 400)
        }
        firstPageLoads += 1
        if (firstPageLoads === 1) {
          return jsonResponse({
            items: [videoWithID(20, '关注首屏')],
            next_cursor: 'following-page-2',
          })
        }
        return jsonResponse({ items: [videoWithID(23, '重新加载的关注首屏')] })
      }
      if (url.includes('scene=timeline')) {
        return jsonResponse({ items: [videoWithID(1, '首屏视频')] })
      }
      return new Response(null, { status: 404 })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()
    await scrollToBottom(wrapper)
    await flushPromises()

    const errorStatus = feedStream(wrapper).get('.stream-status--error')
    expect(errorStatus.text()).toContain('分页状态已失效')

    await errorStatus.get('button').trigger('click')
    await flushPromises()

    const pagedCalls = fetchMock.mock.calls
      .map(([input]) => requestURL(input))
      .filter((url) => url.includes('scene=following') && url.includes('cursor='))
    expect(pagedCalls).toHaveLength(1)
    expect(wrapper.text()).toContain('重新加载的关注首屏')
    expect(wrapper.text()).not.toContain('分页状态已失效')
  })
})
