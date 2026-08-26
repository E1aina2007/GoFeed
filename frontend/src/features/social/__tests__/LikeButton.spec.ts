import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'

import { clearSession, login } from '@/features/auth/session'
import LikeButton from '../LikeButton.vue'

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

async function mountButton(videoId: number, likesCount = 0) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/video/:id', name: 'video-detail', component: { template: '<div />' } },
      { path: '/login', name: 'login', component: { template: '<div />' } },
    ],
  })
  await router.push({ name: 'video-detail', params: { id: videoId } })
  await router.isReady()

  return {
    router,
    wrapper: mount(LikeButton, {
      props: { videoId, likesCount },
      global: { plugins: [createPinia(), router] },
    }),
  }
}

describe('LikeButton', () => {
  afterEach(() => {
    clearSession()
    vi.unstubAllGlobals()
  })

  it('redirects an anonymous visitor to login with the current page as redirect target', async () => {
    const { router, wrapper } = await mountButton(70, 3)

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.name).toBe('login')
    expect(router.currentRoute.value.query.redirect).toBe('/video/70')
    wrapper.unmount()
  })

  it('loads current state then uses the server count after a like', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(session))
      .mockResolvedValueOnce(jsonResponse({ liked: false, likes_count: 3 }))
      .mockResolvedValueOnce(jsonResponse({ liked: true, likes_count: 4 }))
    vi.stubGlobal('fetch', fetchMock)
    await login({ username: 'alice', password: 'password-123' })

    const { wrapper } = await mountButton(71, 1)
    await flushPromises()
    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(wrapper.get('button').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('button').text()).toContain('已赞')
    expect(wrapper.get('button').text()).toContain('4')
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/video/auth/71/like', expect.any(Object))
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/video/auth/71/like',
      expect.objectContaining({ method: 'PUT' }),
    )
    wrapper.unmount()
  })

  it('keeps the previous state when a change fails and permits retry', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(session))
      .mockResolvedValueOnce(jsonResponse({ liked: false, likes_count: 1 }))
      .mockResolvedValueOnce(jsonResponse({ error: '点赞服务暂不可用' }, 500))
      .mockResolvedValueOnce(jsonResponse({ liked: true, likes_count: 2 }))
    vi.stubGlobal('fetch', fetchMock)
    await login({ username: 'alice', password: 'password-123' })

    const { wrapper } = await mountButton(72, 1)
    await flushPromises()
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.get('button').attributes('aria-pressed')).toBe('false')

    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.get('button').attributes('aria-pressed')).toBe('true')
    expect(fetchMock).toHaveBeenCalledTimes(4)
    wrapper.unmount()
  })
})
