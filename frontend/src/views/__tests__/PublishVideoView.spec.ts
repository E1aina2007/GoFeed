import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import type { Router } from 'vue-router'

import {
  createDraft,
  publishDraft,
  uploadCover,
  uploadVideo,
} from '@/features/video/api'
import PublishVideoView from '../PublishVideoView.vue'

const routerReplace = vi.hoisted(() => vi.fn<Router['replace']>())

vi.mock('vue-router', () => ({
  useRouter: () => ({ replace: routerReplace }),
}))

vi.mock('@/features/video/api', () => ({
  createDraft: vi.fn<typeof createDraft>(),
  publishDraft: vi.fn<typeof publishDraft>(),
  uploadCover: vi.fn<typeof uploadCover>(),
  uploadVideo: vi.fn<typeof uploadVideo>(),
}))

describe('PublishVideoView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('requires a title before starting an upload', async () => {
    const wrapper = mount(PublishVideoView, { global: { plugins: [createPinia()], stubs: { RouterLink: true } } })

    await wrapper.get('form').trigger('submit')

    expect(wrapper.get('[role="alert"]').text()).toBe('请填写视频标题')
  })

  it('requires both media files after the title is provided', async () => {
    const wrapper = mount(PublishVideoView, { global: { plugins: [createPinia()], stubs: { RouterLink: true } } })
    await wrapper.get('input').setValue('春日散步')

    await wrapper.get('form').trigger('submit')

    expect(wrapper.get('[role="alert"]').text()).toBe('请选择一个视频文件')
  })

  it('creates a draft, binds both media files, then publishes it', async () => {
    vi.mocked(createDraft).mockResolvedValue({
      draft: {
        id: 7,
        title: '春日散步',
        description: '',
        status: 'draft',
        created_at: '2026-08-22T08:00:00Z',
        updated_at: '2026-08-22T08:00:00Z',
      },
    })
    vi.mocked(uploadVideo).mockResolvedValue({
      draft_id: 7,
      play_url: '/static/videos/42/clip.mp4',
      play_file_name: 'clip.mp4',
      play_original_name: '服务端视频.mp4',
    })
    vi.mocked(uploadCover).mockResolvedValue({
      draft_id: 7,
      cover_url: '/static/covers/42/cover.png',
      cover_file_name: 'cover.png',
      cover_original_name: '服务端封面.png',
    })
    vi.mocked(publishDraft).mockResolvedValue({
      video: {
        id: 100,
        title: '春日散步',
        description: '',
        play_url: '/static/videos/42/clip.mp4',
        play_file_name: 'clip.mp4',
        play_original_name: '服务端视频.mp4',
        cover_url: '/static/covers/42/cover.png',
        cover_file_name: 'cover.png',
        cover_original_name: '服务端封面.png',
        published_at: '2026-08-22T08:01:00Z',
        likes_count: 0,
        comments_count: 0,
        author: { id: 42, username: 'alice', avatar_url: '' },
      },
    })

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'local.png', { type: 'image/png' })
    await fields[0]?.setValue('春日散步')
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createDraft).toHaveBeenCalledWith({ title: '春日散步', description: '' })
    expect(uploadVideo).toHaveBeenCalledWith(7, video, expect.any(Function))
    expect(uploadCover).toHaveBeenCalledWith(7, cover, expect.any(Function))
    expect(publishDraft).toHaveBeenCalledWith(7)
    expect(wrapper.text()).toContain('服务端视频.mp4')
    expect(routerReplace).toHaveBeenCalledWith({ name: 'feed', query: { published: '100' } })
  })
})
