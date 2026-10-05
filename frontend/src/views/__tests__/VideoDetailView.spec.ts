import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'

import { getPublishedVideo } from '@/features/video/api'
import { ApiError } from '@/lib/api'
import VideoDetailView from '../VideoDetailView.vue'

const route = vi.hoisted(() => ({ params: { id: '80' } as Record<string, string> }))

vi.mock('vue-router', () => ({
  RouterLink: { template: '<a><slot /></a>' },
  useRoute: () => route,
}))

vi.mock('@/features/video/api', () => ({
  getPublishedVideo: vi.fn<typeof getPublishedVideo>(),
}))

function videoResponse() {
  return {
    video: {
      id: 80,
      title: '春日散步',
      description: '记录一下',
      play_url: '/static/videos/42/clip.mp4',
      play_file_name: 'clip.mp4',
      play_original_name: '本地视频.mp4',
      cover_url: '/static/covers/42/cover.png',
      cover_file_name: 'cover.png',
      cover_original_name: '本地封面.png',
      published_at: '2026-08-22T08:00:00Z',
      likes_count: 3,
      comments_count: 5,
      author: { id: 42, username: 'alice' },
    },
  }
}

function mountView() {
  return mount(VideoDetailView, {
    global: {
      plugins: [createPinia()],
      stubs: { CommentSection: true, LikeButton: true },
    },
  })
}

describe('VideoDetailView', () => {
  beforeEach(() => {
    route.params = { id: '80' }
    vi.mocked(getPublishedVideo).mockReset()
  })

  it('shows the server error and recovers through the retry button', async () => {
    vi.mocked(getPublishedVideo)
      .mockRejectedValueOnce(new ApiError(404, 'video not found'))
      .mockResolvedValueOnce(videoResponse())
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('内容不存在或已被删除')
    await wrapper.get('.secondary-action').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('春日散步')
    wrapper.unmount()
  })
})
