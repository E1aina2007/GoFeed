import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import { clearSession, login } from '@/features/auth/session'

import FeedView from '../FeedView.vue'

const videoItem = {
  id: 7,
  title: '城市夜跑',
  description: '沿着江边跑完这一段。',
  play_url: '/static/videos/7/night-run.mp4',
  play_file_name: 'night-run.mp4',
  play_original_name: 'night-run.mp4',
  cover_url: '/static/covers/7/night-run.jpg',
  cover_file_name: 'night-run.jpg',
  cover_original_name: 'night-run.jpg',
  published_at: '2026-08-18T12:00:00+08:00',
  likes_count: 0,
  comments_count: 0,
  author: { id: 7, username: 'runfast', avatar_url: '' },
}

const authSession = {
  access_token: 'unit-access-token',
  refresh_token: 'unit-refresh-token',
  expires_at: '2027-01-01T00:00:00Z',
  user: { id: 42, username: 'unit-user' },
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

// 登录接口走真实 session 模块；调用后由用例自行替换 fetch 以分流 Feed 请求
async function signIn() {
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : String(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      return new Response(null, { status: 404 })
    }),
  )
  await login({ username: 'unit-user', password: 'password-123' })
}

type ObserverEntry = Pick<
  IntersectionObserverEntry,
  'target' | 'isIntersecting' | 'intersectionRatio'
>

class MockIntersectionObserver {
  static instances: MockIntersectionObserver[] = []

  readonly disconnect = vi.fn<() => void>()
  readonly observe = vi.fn<(target: Element) => void>()

  constructor(private readonly callback: IntersectionObserverCallback) {
    MockIntersectionObserver.instances.push(this)
  }

  trigger(entries: ObserverEntry[]) {
    this.callback(entries as IntersectionObserverEntry[], this as unknown as IntersectionObserver)
  }
}

const cleanupCallbacks: Array<() => void> = []
const originalVisibilityState = Object.getOwnPropertyDescriptor(document, 'visibilityState')

function setVisibilityState(value: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', { configurable: true, value })
}

function restoreVisibilityState() {
  if (originalVisibilityState) {
    Object.defineProperty(document, 'visibilityState', originalVisibilityState)
    return
  }
  Reflect.deleteProperty(document, 'visibilityState')
}

function currentObserver() {
  const observer = MockIntersectionObserver.instances[MockIntersectionObserver.instances.length - 1]
  if (!observer) {
    throw new Error('expected an IntersectionObserver instance')
  }
  return observer
}

async function mountFeed(path = '/') {
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
  const wrapper = mount(FeedView, { global: { plugins: [createPinia(), router] } })
  cleanupCallbacks.push(() => {
    if (wrapper.exists()) {
      wrapper.unmount()
    }
  })
  return { router, wrapper }
}

describe('FeedView', () => {
  beforeEach(() => {
    vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue(undefined)
    vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => undefined)
  })

  afterEach(() => {
    while (cleanupCallbacks.length) {
      cleanupCallbacks.pop()?.()
    }
    MockIntersectionObserver.instances = []
    restoreVisibilityState()
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    clearSession()
  })

  it('renders a full-viewport short video from the public feed', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(
          JSON.stringify({
            items: [videoItem],
          }),
          {
            headers: { 'content-type': 'application/json' },
          },
        ),
      ),
    )

    const { wrapper } = await mountFeed()
    await flushPromises()

    expect(wrapper.get('video').attributes('src')).toBe('/static/videos/7/night-run.mp4')
    expect(wrapper.get('.short-video').classes()).toContain('short-video')
    expect(wrapper.text()).toContain('@runfast')
    expect(wrapper.text()).toContain('城市夜跑')
  })

  it('confirms the newly published video and clears the one-time query parameter', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ items: [videoItem] }), {
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const { router, wrapper } = await mountFeed('/?published=7')
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[role="status"]').text()).toBe('视频已发布')
    expect(router.currentRoute.value.query.published).toBeUndefined()
  })

  it('keeps the feed usable when the published video is absent from the first page', async () => {
    const otherVideo = { ...videoItem, id: 8, title: '清晨骑行' }
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(JSON.stringify({ items: [otherVideo] }), {
          headers: { 'content-type': 'application/json' },
        }),
      ),
    )

    const { router, wrapper } = await mountFeed('/?published=7')
    await flushPromises()

    expect(wrapper.text()).toContain('清晨骑行')
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    expect(router.currentRoute.value.query.published).toBeUndefined()
  })

  it('automatically restores a failed published return before clearing its one-time query parameter', async () => {
    vi.useFakeTimers()
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: '服务暂不可用' }), {
          status: 503,
          headers: { 'content-type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ items: [videoItem] }), {
          headers: { 'content-type': 'application/json' },
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const { router, wrapper } = await mountFeed('/?published=7')
    await flushPromises()
    await vi.runAllTimersAsync()
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[role="status"]').text()).toBe('视频已发布')
    expect(wrapper.get('video').attributes('src')).toBe('/static/videos/7/night-run.mp4')
    expect(router.currentRoute.value.query.published).toBeUndefined()
  })

  it('pauses hidden players and resumes only the active visible player', async () => {
    const otherVideo = { ...videoItem, id: 8, title: '清晨骑行' }
    const playMock = vi.mocked(HTMLMediaElement.prototype.play)
    const pauseMock = vi.mocked(HTMLMediaElement.prototype.pause)
    setVisibilityState('visible')
    vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(
          JSON.stringify({
            items: [videoItem, otherVideo],
          }),
          {
            headers: { 'content-type': 'application/json' },
          },
        ),
      ),
    )

    const { wrapper } = await mountFeed()
    await flushPromises()
    const players = wrapper.findAll('video').map((player) => player.element as HTMLVideoElement)
    const firstPlayer = players[0]
    const secondPlayer = players[1]
    if (!firstPlayer || !secondPlayer) {
      throw new Error('expected two video players')
    }
    const observer = currentObserver()
    observer.trigger([
      { target: firstPlayer, isIntersecting: true, intersectionRatio: 0.8 },
      { target: secondPlayer, isIntersecting: false, intersectionRatio: 0 },
    ])
    await flushPromises()
    playMock.mockClear()
    pauseMock.mockClear()

    observer.trigger([{ target: secondPlayer, isIntersecting: false, intersectionRatio: 0 }])
    await flushPromises()
    expect(playMock).toHaveBeenCalledTimes(1)
    expect(playMock.mock.contexts[0]).toBe(firstPlayer)
    expect(pauseMock).toHaveBeenCalledTimes(1)
    expect(pauseMock.mock.contexts[0]).toBe(secondPlayer)
    playMock.mockClear()
    pauseMock.mockClear()

    setVisibilityState('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(pauseMock).toHaveBeenCalledTimes(2)

    playMock.mockClear()
    pauseMock.mockClear()
    setVisibilityState('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()

    expect(playMock).toHaveBeenCalledTimes(1)
    expect(playMock.mock.contexts[0]).toBe(firstPlayer)
    expect(pauseMock).toHaveBeenCalledTimes(1)
    expect(pauseMock.mock.contexts[0]).toBe(secondPlayer)
  })

  it('pauses when no player is visible and releases player resources on unmount', async () => {
    const pauseMock = vi.mocked(HTMLMediaElement.prototype.pause)
    vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(
          JSON.stringify({
            items: [videoItem],
          }),
          {
            headers: { 'content-type': 'application/json' },
          },
        ),
      ),
    )

    const { wrapper } = await mountFeed()
    await flushPromises()
    const observer = currentObserver()
    const player = wrapper.get('video').element as HTMLVideoElement
    pauseMock.mockClear()

    observer.trigger([{ target: player, isIntersecting: false, intersectionRatio: 0 }])
    expect(pauseMock).toHaveBeenCalledTimes(1)

    pauseMock.mockClear()
    wrapper.unmount()
    expect(observer.disconnect).toHaveBeenCalledTimes(1)
    expect(pauseMock).toHaveBeenCalledTimes(1)
  })

  it('shows the following sign-in entry without requesting the following scene while signed out', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ items: [videoItem] }))
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    expect(wrapper.text()).toContain('城市夜跑')

    const tabs = wrapper.findAll('.feed-tab')
    expect(tabs[0]?.attributes('aria-pressed')).toBe('true')
    expect(tabs[1]?.attributes('aria-pressed')).toBe('false')

    await tabs[1]!.trigger('click')
    await flushPromises()

    const signin = wrapper.get('section.feed-message[role="status"]')
    expect(signin.text()).toContain('登录后查看关注作者的最新视频')
    expect(signin.get('a').attributes('href')).toBe('/login?redirect=/?scene=following')
    // 只有 Timeline 首屏请求，未登录不发 Following 请求
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe('/api/feed?scene=timeline&limit=12')
  })

  it('enters the following scene directly from the scene query parameter when signed in', async () => {
    const followingVideo = {
      ...videoItem,
      id: 9,
      title: '直达关注流',
      author: { id: 9, username: 'followed-author', avatar_url: '' },
    }
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : String(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.startsWith('/api/feed?scene=following')) {
        return jsonResponse({ items: [followingVideo] })
      }
      return jsonResponse({ items: [videoItem] })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed('/?scene=following')
    await flushPromises()

    // 场景来自 URL：只发 Following 认证首屏，不发 Timeline 请求
    //（LikeButton 的点赞状态请求与 Feed 场景无关，不参与断言）
    const feedCalls = fetchMock.mock.calls.filter(([input]) =>
      String(input).startsWith('/api/feed'),
    )
    expect(feedCalls.map(([input]) => String(input))).toEqual(['/api/feed?scene=following&limit=12'])
    expect(new Headers(feedCalls[0]?.[1]?.headers).get('Authorization')).toBe(
      'Bearer unit-access-token',
    )
    expect(wrapper.text()).toContain('直达关注流')
    expect(wrapper.findAll('.feed-tab')[1]?.attributes('aria-pressed')).toBe('true')
  })

  it('falls back to the timeline for single-value timeline, missing or invalid scene values', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(async () => jsonResponse({ items: [videoItem] }))
    vi.stubGlobal('fetch', fetchMock)

    for (const path of ['/', '/?scene=timeline', '/?scene=nonsense', '/?scene=following&scene=timeline']) {
      const { wrapper } = await mountFeed(path)
      await flushPromises()

      // 单值 following 才进入 Following；timeline、缺失、非法值与多值都回退 Timeline
      expect(wrapper.findAll('.feed-tab')[0]?.attributes('aria-pressed')).toBe('true')
      expect(wrapper.text()).toContain('城市夜跑')
      wrapper.unmount()
    }
    const timelineCalls = fetchMock.mock.calls.map(([input]) => String(input))
    expect(timelineCalls).toHaveLength(4)
    for (const url of timelineCalls) {
      expect(url).toBe('/api/feed?scene=timeline&limit=12')
    }
  })

  it('shows the sign-in entry and no feed request when visiting the scene URL signed out', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ items: [videoItem] }))
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed('/?scene=following')
    await flushPromises()

    const signin = wrapper.get('section.feed-message[role="status"]')
    expect(signin.text()).toContain('登录后查看关注作者的最新视频')
    // redirect 携带 scene=following 的完整路径
    expect(signin.get('a').attributes('href')).toBe('/login?redirect=/?scene=following')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('keeps other query parameters in the following login redirect', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ items: [videoItem] }))
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed('/?foo=bar&scene=following')
    await flushPromises()

    const signin = wrapper.get('section.feed-message[role="status"]')
    expect(signin.get('a').attributes('href')).toBe('/login?redirect=/?foo=bar%26scene=following')
  })

  it('updates the scene query parameter on switch while preserving other parameters', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ items: [videoItem] }))
    vi.stubGlobal('fetch', fetchMock)

    const { router, wrapper } = await mountFeed('/?foo=bar')
    await flushPromises()

    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ foo: 'bar', scene: 'following' })
    expect(wrapper.findAll('.feed-tab')[1]?.attributes('aria-pressed')).toBe('true')

    await wrapper.findAll('.feed-tab')[0]!.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ foo: 'bar' })
    expect(wrapper.findAll('.feed-tab')[0]?.attributes('aria-pressed')).toBe('true')
  })

  it('enters the following scene automatically after signing in on the scene URL', async () => {
    const followingVideo = {
      ...videoItem,
      id: 9,
      title: '登录后的关注视频',
      author: { id: 9, username: 'followed-author', avatar_url: '' },
    }
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : String(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.startsWith('/api/feed?scene=following')) {
        return jsonResponse({ items: [followingVideo] })
      }
      return jsonResponse({ items: [videoItem] })
    })
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed('/?scene=following')
    await flushPromises()
    // 未登录展示登录入口，不发请求
    expect(fetchMock).not.toHaveBeenCalled()

    await login({ username: 'unit-user', password: 'password-123' })
    await flushPromises()

    // 登录成功回到同一 URL 后自动发起 Following 认证首屏请求
    const followingCalls = fetchMock.mock.calls.filter(([input]) =>
      String(input).startsWith('/api/feed?scene=following'),
    )
    expect(followingCalls).toHaveLength(1)
    expect(new Headers(followingCalls[0]?.[1]?.headers).get('Authorization')).toBe(
      'Bearer unit-access-token',
    )
    expect(wrapper.text()).toContain('登录后的关注视频')
    expect(wrapper.find('section.feed-message[role="status"]').exists()).toBe(false)
  })

  it('loads the following scene with the session token for a signed-in visitor', async () => {
    const followingVideo = {
      ...videoItem,
      id: 9,
      title: '关注作者的视频',
      author: { id: 9, username: 'followed-author', avatar_url: '' },
    }
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : String(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.startsWith('/api/feed?scene=following')) {
        return jsonResponse({ items: [followingVideo] })
      }
      return jsonResponse({ items: [videoItem] })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()

    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()

    const followingCalls = fetchMock.mock.calls.filter(([input]) =>
      String(input).startsWith('/api/feed?scene=following'),
    )
    expect(followingCalls).toHaveLength(1)
    expect(String(followingCalls[0]?.[0])).toBe('/api/feed?scene=following&limit=12')
    expect(new Headers(followingCalls[0]?.[1]?.headers).get('Authorization')).toBe(
      'Bearer unit-access-token',
    )
    expect(wrapper.text()).toContain('关注作者的视频')
    expect(wrapper.findAll('.feed-tab')[1]?.attributes('aria-pressed')).toBe('true')
  })

  it('renders the following empty state without a retry entry', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : String(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.startsWith('/api/feed?scene=following')) {
        return jsonResponse({ items: [] })
      }
      return jsonResponse({ items: [videoItem] })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()

    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()

    const message = wrapper.get('section.feed-message[role="alert"]')
    expect(message.text()).toContain('还没有可看的关注视频')
    expect(message.find('button').exists()).toBe(false)
    expect(wrapper.findAll('.short-video')).toHaveLength(0)
  })

  it('pauses the timeline players when switching to the following scene', async () => {
    const otherVideo = { ...videoItem, id: 8, title: '清晨骑行' }
    const pauseMock = vi.mocked(HTMLMediaElement.prototype.pause)
    setVisibilityState('visible')
    vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ items: [videoItem, otherVideo] })),
    )

    const { wrapper } = await mountFeed()
    await flushPromises()
    const players = wrapper.findAll('video').map((player) => player.element as HTMLVideoElement)
    const firstPlayer = players[0]
    const secondPlayer = players[1]
    if (!firstPlayer || !secondPlayer) {
      throw new Error('expected two video players')
    }
    currentObserver().trigger([
      { target: firstPlayer, isIntersecting: true, intersectionRatio: 0.8 },
      { target: secondPlayer, isIntersecting: false, intersectionRatio: 0 },
    ])
    await flushPromises()
    pauseMock.mockClear()

    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()

    // 切换场景时旧场景的播放器必须先暂停
    expect(pauseMock.mock.contexts).toContain(firstPlayer)
    expect(pauseMock.mock.contexts).toContain(secondPlayer)
  })

  it('invalidates the old player observer on switch and drops its late callbacks', async () => {
    // 两个场景返回相同视频 ID：迟到的旧观察回调不得操作新场景的同 ID 播放器
    const sharedVideo = { ...videoItem, id: 7, title: '同 ID 时间线视频' }
    const followingShared = { ...videoItem, id: 7, title: '同 ID 关注视频' }
    const playMock = vi.mocked(HTMLMediaElement.prototype.play)
    const pauseMock = vi.mocked(HTMLMediaElement.prototype.pause)
    setVisibilityState('visible')
    vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : String(input)
      if (url === '/api/user/login') {
        return jsonResponse(authSession)
      }
      if (url.startsWith('/api/feed?scene=following')) {
        return jsonResponse({ items: [followingShared] })
      }
      return jsonResponse({ items: [sharedVideo] })
    })
    await signIn()
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountFeed()
    await flushPromises()
    const timelineObserver = currentObserver()
    timelineObserver.trigger([
      { target: wrapper.get('video').element, isIntersecting: true, intersectionRatio: 0.8 },
    ])
    await flushPromises()
    expect(playMock).toHaveBeenCalledTimes(1)

    await wrapper.findAll('.feed-tab')[1]!.trigger('click')
    await flushPromises()

    // 切换开始即断开旧观察并重建新观察
    expect(timelineObserver.disconnect).toHaveBeenCalled()
    const newObserver = currentObserver()
    expect(newObserver).not.toBe(timelineObserver)
    expect(wrapper.text()).toContain('同 ID 关注视频')
    const followingPlayer = wrapper.get('video').element as HTMLVideoElement

    // 旧 observer 的迟到回调（同 ID 可见）必须整体丢弃：不播放也不误暂停新场景播放器
    playMock.mockClear()
    pauseMock.mockClear()
    timelineObserver.trigger([
      { target: followingPlayer, isIntersecting: true, intersectionRatio: 0.8 },
    ])
    await flushPromises()
    expect(playMock).not.toHaveBeenCalled()
    expect(pauseMock).not.toHaveBeenCalled()

    // 新 observer 正常接管当前场景
    newObserver.trigger([
      { target: followingPlayer, isIntersecting: true, intersectionRatio: 0.8 },
    ])
    await flushPromises()
    expect(playMock).toHaveBeenCalledTimes(1)
    expect(playMock.mock.contexts[0]).toBe(followingPlayer)
  })
})
