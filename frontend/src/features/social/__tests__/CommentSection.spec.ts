import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'

import { clearSession, login } from '@/features/auth/session'
import CommentSection from '../CommentSection.vue'

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

async function mountSection(videoId = 80, commentsCount = 0) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/video/:id', name: 'video-detail', component: { template: '<div />' } },
      { path: '/login', name: 'login', component: { template: '<div />' } },
      { path: '/users/:id', name: 'user-profile', component: { template: '<div />' } },
    ],
  })
  await router.push({ name: 'video-detail', params: { id: videoId } })
  await router.isReady()

  return {
    router,
    wrapper: mount(CommentSection, {
      props: { videoId, commentsCount },
      global: { plugins: [createPinia(), router] },
    }),
  }
}

describe('CommentSection', () => {
  afterEach(() => {
    clearSession()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('shows a login route for anonymous visitors', async () => {
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ items: [] })))
    const { router, wrapper } = await mountSection()
    await flushPromises()

    await wrapper.get('.comment-login').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.name).toBe('login')
    expect(router.currentRoute.value.query.redirect).toBe('/video/80')
    wrapper.unmount()
  })

  it('adds and removes the current user comment while keeping the count in sync', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(session))
      .mockResolvedValueOnce(
        jsonResponse({
          items: [
            {
              id: 1,
              video_id: 81,
              author: { id: 9, username: 'bob' },
              content: '第一条评论',
              created_at: '2026-08-26T08:00:00Z',
            },
          ],
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse(
          {
            comment: {
              id: 2,
              video_id: 81,
              author: { id: 42, username: 'alice' },
              content: '我的评论',
              created_at: '2026-08-26T08:01:00Z',
            },
          },
          201,
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    await login({ username: 'alice', password: 'password-123' })

    const { wrapper } = await mountSection(81, 1)
    await flushPromises()
    await wrapper.get('textarea').setValue('我的评论')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('评论 2')
    expect(wrapper.text()).toContain('我的评论')
    await wrapper.get('.delete-comment').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('评论 1')
    expect(wrapper.text()).not.toContain('我的评论')
    expect(fetchMock).toHaveBeenLastCalledWith(
      '/api/video/auth/81/comments/2',
      expect.objectContaining({ method: 'DELETE' }),
    )
    wrapper.unmount()
  })

  it('shows the list error and retries the failed request', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ error: '评论服务暂不可用' }, 500))
      .mockResolvedValueOnce(
        jsonResponse({
          items: [
            {
              id: 3,
              video_id: 82,
              author: { id: 9, username: 'bob' },
              content: '重试成功',
              created_at: '2026-08-26T08:00:00Z',
            },
          ],
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const { wrapper } = await mountSection(82, 1)
    await flushPromises()
    expect(wrapper.text()).toContain('评论服务暂不可用')

    await wrapper.get('.secondary-action').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('重试成功')
    expect(fetchMock).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
