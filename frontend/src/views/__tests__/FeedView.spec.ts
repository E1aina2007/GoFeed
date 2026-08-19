import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import FeedView from '../FeedView.vue'

describe('FeedView', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders a full-viewport short video from the public feed', async () => {
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      items: [{
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
      }],
    }), {
      headers: { 'content-type': 'application/json' },
    })))

    const wrapper = mount(FeedView)
    await flushPromises()

    expect(wrapper.get('video').attributes('src')).toBe('/static/videos/7/night-run.mp4')
    expect(wrapper.get('.short-video').classes()).toContain('short-video')
    expect(wrapper.text()).toContain('@runfast')
    expect(wrapper.text()).toContain('城市夜跑')
  })
})
