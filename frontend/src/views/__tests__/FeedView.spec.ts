import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
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
  return {
    router,
    wrapper: mount(FeedView, { global: { plugins: [router] } }),
  }
}

describe('FeedView', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders a full-viewport short video from the public feed', async () => {
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      items: [videoItem],
    }), {
      headers: { 'content-type': 'application/json' },
    })))

    const { wrapper } = await mountFeed()
    await flushPromises()

    expect(wrapper.get('video').attributes('src')).toBe('/static/videos/7/night-run.mp4')
    expect(wrapper.get('.short-video').classes()).toContain('short-video')
    expect(wrapper.text()).toContain('@runfast')
    expect(wrapper.text()).toContain('城市夜跑')
  })

  it('confirms the newly published video and clears the one-time query parameter', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ items: [videoItem] }), {
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    const { router, wrapper } = await mountFeed('/?published=7')
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[role="status"]').text()).toBe('视频已发布')
    expect(router.currentRoute.value.query.published).toBeUndefined()
  })

  it('keeps the feed usable when the published video is absent from the first page', async () => {
    const otherVideo = { ...videoItem, id: 8, title: '清晨骑行' }
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ items: [otherVideo] }), {
      headers: { 'content-type': 'application/json' },
    })))

    const { router, wrapper } = await mountFeed('/?published=7')
    await flushPromises()

    expect(wrapper.text()).toContain('清晨骑行')
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    expect(router.currentRoute.value.query.published).toBeUndefined()
  })
})
