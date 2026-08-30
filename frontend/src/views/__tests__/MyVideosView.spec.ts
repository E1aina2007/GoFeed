import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'

import { deleteVideo, listMyVideos, type VideoItem } from '@/features/video/api'
import { ApiError } from '@/lib/api'
import { useConfirmStore } from '@/stores/confirm'
import MyVideosView from '../MyVideosView.vue'

vi.mock('vue-router', () => ({
  RouterLink: { template: '<a><slot /></a>' },
}))

vi.mock('@/features/video/api', () => ({
  listMyVideos: vi.fn<typeof listMyVideos>(),
  deleteVideo: vi.fn<typeof deleteVideo>(),
}))

function videoItem(id: number, title: string): VideoItem {
  return {
    id,
    title,
    description: '',
    play_url: `/static/videos/42/clip-${id}.mp4`,
    play_file_name: `clip-${id}.mp4`,
    play_original_name: `本地视频-${id}.mp4`,
    cover_url: `/static/covers/42/cover-${id}.png`,
    cover_file_name: `cover-${id}.png`,
    cover_original_name: `本地封面-${id}.png`,
    published_at: '2026-08-22T08:00:00Z',
    likes_count: 0,
    comments_count: 0,
    author: { id: 42, username: 'alice' },
  }
}

function mountView() {
  const pinia = createPinia()
  return {
    confirmStore: useConfirmStore(pinia),
    wrapper: mount(MyVideosView, { global: { plugins: [pinia] } }),
  }
}

describe('MyVideosView', () => {
  beforeEach(() => {
    vi.mocked(listMyVideos).mockReset()
    vi.mocked(deleteVideo).mockReset()
  })

  it('renders the first page and appends the next page on load more', async () => {
    vi.mocked(listMyVideos)
      .mockResolvedValueOnce({ items: [videoItem(1, '第一条')], next_cursor: 'cursor-2' })
      .mockResolvedValueOnce({ items: [videoItem(2, '第二条')] })
    const { wrapper } = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('第一条')
    expect(wrapper.text()).not.toContain('已经到底了')
    expect(listMyVideos).toHaveBeenCalledTimes(1)

    await wrapper.get('.load-more').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('第二条')
    expect(wrapper.text()).toContain('已经到底了')
    expect(listMyVideos).toHaveBeenNthCalledWith(2, { cursor: 'cursor-2' })
    wrapper.unmount()
  })

  it('shows the error state and recovers through the retry button', async () => {
    vi.mocked(listMyVideos)
      .mockRejectedValueOnce(new ApiError(500, 'video operation failed'))
      .mockResolvedValueOnce({ items: [videoItem(1, '第一条')] })
    const { wrapper } = mountView()
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('服务暂时不可用，请稍后重试')
    await wrapper.get('.secondary-action').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('第一条')
    wrapper.unmount()
  })

  it('keeps the video when the confirmation is cancelled', async () => {
    vi.mocked(listMyVideos).mockResolvedValue({ items: [videoItem(1, '第一条')] })
    const { confirmStore, wrapper } = mountView()
    await flushPromises()

    await wrapper.get('.text-action--danger').trigger('click')
    await flushPromises()
    expect(confirmStore.open).toBe(true)

    confirmStore.cancel()
    await flushPromises()

    expect(deleteVideo).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('第一条')
    wrapper.unmount()
  })

  it('removes the video after the confirmation is accepted', async () => {
    vi.mocked(listMyVideos).mockResolvedValue({ items: [videoItem(1, '第一条')] })
    vi.mocked(deleteVideo).mockResolvedValue(null)
    const { confirmStore, wrapper } = mountView()
    await flushPromises()

    await wrapper.get('.text-action--danger').trigger('click')
    await flushPromises()
    expect(confirmStore.open).toBe(true)

    confirmStore.accept()
    await flushPromises()

    expect(deleteVideo).toHaveBeenCalledWith(1)
    expect(wrapper.text()).not.toContain('第一条')
    expect(wrapper.text()).toContain('视频已删除')
    wrapper.unmount()
  })

  it('keeps the video and shows the error when deletion fails', async () => {
    vi.mocked(listMyVideos).mockResolvedValue({ items: [videoItem(1, '第一条')] })
    vi.mocked(deleteVideo).mockRejectedValue(new ApiError(500, 'video operation failed'))
    const { confirmStore, wrapper } = mountView()
    await flushPromises()

    await wrapper.get('.text-action--danger').trigger('click')
    await flushPromises()
    confirmStore.accept()
    await flushPromises()

    expect(wrapper.text()).toContain('第一条')
    expect(wrapper.get('.inline-error').text()).toBe('服务暂时不可用，请稍后重试')
    wrapper.unmount()
  })
})
