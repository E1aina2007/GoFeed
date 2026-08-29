import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'

import { clearSession, login } from '@/features/auth/session'
import FollowButton from '../FollowButton.vue'
import FollowListDialog from '../FollowListDialog.vue'

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

async function createTestRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/users/:id', name: 'user-profile', component: { template: '<div />' } },
      { path: '/login', name: 'login', component: { template: '<div />' } },
    ],
  })
  await router.push({ name: 'user-profile', params: { id: 90 } })
  await router.isReady()
  return router
}

describe('follow controls', () => {
  afterEach(() => {
    clearSession()
    document.body.replaceChildren()
    vi.unstubAllGlobals()
  })

  it('redirects anonymous visitors and applies the server follow state after login', async () => {
    const anonymousRouter = await createTestRouter()
    const anonymous = mount(FollowButton, {
      props: { userId: 91, followerCount: 3 },
      global: { plugins: [createPinia(), anonymousRouter] },
    })
    await anonymous.get('button').trigger('click')
    await flushPromises()
    expect(anonymousRouter.currentRoute.value.name).toBe('login')
    anonymous.unmount()

    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(session))
      .mockResolvedValueOnce(jsonResponse({ following: false, follower_count: 3 }))
      .mockResolvedValueOnce(jsonResponse({ following: true, follower_count: 4 }))
    vi.stubGlobal('fetch', fetchMock)
    await login({ username: 'alice', password: 'password-123' })

    const authenticatedRouter = await createTestRouter()
    const authenticated = mount(FollowButton, {
      props: { userId: 92, followerCount: 3 },
      global: { plugins: [createPinia(), authenticatedRouter] },
    })
    await flushPromises()
    await authenticated.get('button').trigger('click')
    await flushPromises()

    expect(authenticated.get('button').text()).toContain('已关注')
    const stateChanges = authenticated.emitted('stateChange') ?? []
    expect(stateChanges[stateChanges.length - 1]).toEqual([
      { following: true, follower_count: 4 },
    ])
    expect(fetchMock).toHaveBeenLastCalledWith(
      '/api/user/auth/92/follow',
      expect.objectContaining({ method: 'PUT' }),
    )
    authenticated.unmount()
  })

  it('renders public follower and following lists and switches tabs', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        jsonResponse({
          items: [
            {
              user: { id: 3, username: 'bob' },
              followed_at: '2026-08-26T08:00:00Z',
            },
          ],
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          items: [
            {
              user: { id: 4, username: 'cora' },
              followed_at: '2026-08-26T08:01:00Z',
            },
          ],
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    const router = await createTestRouter()
    const wrapper = mount(FollowListDialog, {
      props: { open: true, userId: 90, mode: 'followers' },
      global: { plugins: [router] },
      attachTo: document.body,
    })
    await flushPromises()

    expect(document.body.textContent).toContain('@bob')
    await wrapper.setProps({ mode: 'following' })
    await flushPromises()

    expect(document.body.textContent).toContain('@cora')
    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/user/90/followers?limit=20',
      expect.any(Object),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/user/90/following?limit=20',
      expect.any(Object),
    )
    wrapper.unmount()
  })

  it('traps keyboard focus inside the dialog and restores it on close', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      jsonResponse({
        items: [
          {
            user: { id: 3, username: 'bob' },
            followed_at: '2026-08-26T08:00:00Z',
          },
        ],
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    const router = await createTestRouter()
    const wrapper = mount(FollowListDialog, {
      props: { open: false, userId: 90, mode: 'followers' },
      global: { plugins: [router] },
      attachTo: document.body,
    })

    const trigger = document.createElement('button')
    trigger.textContent = '打开列表'
    document.body.append(trigger)
    trigger.focus()
    expect(document.activeElement).toBe(trigger)

    await wrapper.setProps({ open: true })
    await flushPromises()

    const tabButtons = Array.from(document.body.querySelectorAll<HTMLButtonElement>('.follow-tabs__item'))
    const [firstTab] = tabButtons
    const followLink = document.body.querySelector<HTMLAnchorElement>('.follow-user')
    if (!firstTab || !followLink) {
      throw new Error('对话框应渲染可聚焦的标签页与列表项')
    }
    expect(document.activeElement).toBe(firstTab)

    // 焦点在首个元素上按 Shift+Tab 应回绕到对话框末尾的关注链接
    firstTab.focus()
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true }))
    expect(document.activeElement).toBe(followLink)

    // 焦点在末尾元素上按 Tab 应回到对话框开头
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab' }))
    expect(document.activeElement).toBe(firstTab)

    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    const closeEvents = wrapper.emitted('update:open') ?? []
    expect(closeEvents[closeEvents.length - 1]).toEqual([false])

    await wrapper.setProps({ open: false })
    await flushPromises()
    expect(document.activeElement).toBe(trigger)

    trigger.remove()
    wrapper.unmount()
  })
})
