import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import type { Router } from 'vue-router'

import { clearSession, login } from '@/features/auth/session'
import AccountSettingsView from '../AccountSettingsView.vue'

const routerReplace = vi.hoisted(() => vi.fn<Router['replace']>())

vi.mock('vue-router', () => ({
  useRouter: () => ({ replace: routerReplace }),
}))

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

describe('AccountSettingsView', () => {
  beforeEach(() => {
    clearSession()
    vi.clearAllMocks()
  })

  afterEach(() => {
    clearSession()
    vi.unstubAllGlobals()
  })

  it('uses file upload for avatars and keeps the URL field out of the form', async () => {
    const session = {
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_at: '2026-08-26T08:00:00Z',
      user: { id: 42, username: 'alice' },
    }
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(session))
      .mockResolvedValueOnce(jsonResponse({ avatar_url: '/static/avatars/42/20260824/avatar.png' }))
    vi.stubGlobal('fetch', fetchMock)

    await login({ username: 'alice', password: 'password-123' })
    const wrapper = mount(AccountSettingsView, { global: { plugins: [createPinia()] } })

    expect(wrapper.find('input[type="url"]').exists()).toBe(false)
    const fileInput = wrapper.get('input[type="file"]')
    const file = new File(['png'], 'avatar.png', { type: 'image/png' })
    Object.defineProperty(fileInput.element, 'files', { configurable: true, value: [file] })
    await fileInput.trigger('change')

    const profileForm = wrapper.findAll('form')[1]
    if (!profileForm) {
      throw new Error('profile form was not rendered')
    }
    await profileForm.trigger('submit')
    await flushPromises()

    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/user/auth/avatar',
      expect.objectContaining({ method: 'POST' }),
    )
    expect(wrapper.text()).toContain('个人资料已更新')
  })
})
