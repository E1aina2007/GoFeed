import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'

import type { VideoItem } from '@/features/video/api'
import { listPublishedVideos } from '@/features/video/api'
import { getUserProfile } from '@/features/user/api'
import { ApiError } from '@/lib/api'
import UserProfileView from '../UserProfileView.vue'

const route = vi.hoisted(() => ({ params: { id: '42' } as Record<string, string> }))

vi.mock('vue-router', () => ({
  RouterLink: { template: '<a><slot /></a>' },
  useRoute: () => route,
}))

vi.mock('@/features/user/api', () => ({
  getUserProfile: vi.fn<typeof getUserProfile>(),
}))

vi.mock('@/features/video/api', () => ({
  listPublishedVideos: vi.fn<typeof listPublishedVideos>(),
}))

function profileResponse() {
  return {
    account: { id: 42, username: 'alice', bio: '创作者简介' },
    video_count: 2,
    total_likes: 9,
    follower_count: 3,
    vlogger_count: 5,
  }
}

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
  return mount(UserProfileView, {
    global: {
      plugins: [createPinia()],
      stubs: {
        FollowButton: true,
        FollowListDialog: {
          template: '<div class="follow-dialog-stub" />',
        },
      },
    },
  })
}

describe('UserProfileView', () => {
  beforeEach(() => {
    route.params = { id: '42' }
    vi.mocked(getUserProfile).mockReset()
    vi.mocked(listPublishedVideos).mockReset()
  })

  it('renders the profile stats and published videos', async () => {
    vi.mocked(getUserProfile).mockResolvedValue(profileResponse())
    vi.mocked(listPublishedVideos).mockResolvedValue({
      items: [videoItem(1, '第一条')],
      next_cursor: 'cursor-2',
    })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('@alice')
    expect(wrapper.text()).toContain('创作者简介')
    expect(wrapper.text()).toContain('第一条')
    expect(getUserProfile).toHaveBeenCalledWith(42)
    expect(listPublishedVideos).toHaveBeenCalledWith({ authorID: 42 })
    wrapper.unmount()
  })

  it('appends the next page of videos on load more', async () => {
    vi.mocked(getUserProfile).mockResolvedValue(profileResponse())
    vi.mocked(listPublishedVideos)
      .mockResolvedValueOnce({ items: [videoItem(1, '第一条')], next_cursor: 'cursor-2' })
      .mockResolvedValueOnce({ items: [videoItem(2, '第二条')] })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('.load-more').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('第二条')
    expect(listPublishedVideos).toHaveBeenNthCalledWith(2, { authorID: 42, cursor: 'cursor-2' })
    wrapper.unmount()
  })

  it('shows the error state and recovers through the retry button', async () => {
    vi.mocked(getUserProfile).mockRejectedValue(new ApiError(404, 'user not found'))
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('内容不存在或已被删除')
    vi.mocked(getUserProfile).mockResolvedValue(profileResponse())
    vi.mocked(listPublishedVideos).mockResolvedValue({ items: [videoItem(1, '第一条')] })
    await wrapper.get('.secondary-action').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('@alice')
    wrapper.unmount()
  })

  it('opens the follow list dialog for followers and following', async () => {
    vi.mocked(getUserProfile).mockResolvedValue(profileResponse())
    vi.mocked(listPublishedVideos).mockResolvedValue({ items: [] })
    const wrapper = mountView()
    await flushPromises()

    const statButtons = wrapper.findAll('.profile-stat--action')
    await statButtons[0]?.trigger('click')
    let dialog = wrapper.find('.follow-dialog-stub')
    expect(dialog.attributes('open')).toBe('true')
    expect(dialog.attributes('mode')).toBe('followers')

    await statButtons[1]?.trigger('click')
    dialog = wrapper.find('.follow-dialog-stub')
    expect(dialog.attributes('open')).toBe('true')
    expect(dialog.attributes('mode')).toBe('following')
    wrapper.unmount()
  })
})
