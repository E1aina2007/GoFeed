import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

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
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
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

  it('retries a failed published return before clearing its one-time query parameter', async () => {
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

    expect(wrapper.get('[role="alert"]').text()).toContain('服务暂不可用')
    expect(router.currentRoute.value.query.published).toBe('7')

    await wrapper.get('button').trigger('click')
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
})
