import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import type { Router } from 'vue-router'

import { login } from '@/features/auth/session'
import { ApiError } from '@/lib/api'
import LoginView from '../LoginView.vue'

const route = vi.hoisted(() => ({ query: {} as Record<string, unknown> }))
const routerReplace = vi.hoisted(() => vi.fn<Router['replace']>())

vi.mock('vue-router', () => ({
  RouterLink: { template: '<a><slot /></a>' },
  useRoute: () => route,
  useRouter: () => ({ replace: routerReplace }),
}))

vi.mock('@/features/auth/session', () => ({
  login: vi.fn<typeof login>(),
}))

const sessionFixture = {
  access_token: 'access-token',
  refresh_token: 'refresh-token',
  expires_at: '2026-08-30T08:00:00Z',
  user: { id: 42, username: 'alice' },
}

function mountView() {
  return mount(LoginView, { global: { plugins: [createPinia()] } })
}

async function submitWith(wrapper: ReturnType<typeof mountView>, username: string, password: string) {
  const inputs = wrapper.findAll('input')
  await inputs[0]?.setValue(username)
  await inputs[1]?.setValue(password)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

describe('LoginView', () => {
  beforeEach(() => {
    route.query = {}
    routerReplace.mockClear()
    vi.mocked(login).mockReset()
  })

  it('shows the registration notice when redirected from the register page', () => {
    route.query = { registered: '1' }
    const wrapper = mountView()
    expect(wrapper.get('[role="status"]').text()).toBe('注册成功，请使用新账号登录')
    wrapper.unmount()
  })

  it('logs in with trimmed credentials and follows the redirect target', async () => {
    route.query = { redirect: '/video/80' }
    vi.mocked(login).mockResolvedValue(sessionFixture)
    const wrapper = mountView()
    await submitWith(wrapper, '  alice  ', 'password-123')

    expect(login).toHaveBeenCalledWith({ username: 'alice', password: 'password-123' })
    expect(routerReplace).toHaveBeenCalledWith('/video/80')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('redirects to the feed when no redirect target is present', async () => {
    vi.mocked(login).mockResolvedValue(sessionFixture)
    const wrapper = mountView()
    await submitWith(wrapper, 'alice', 'password-123')

    expect(routerReplace).toHaveBeenCalledWith('/')
    wrapper.unmount()
  })

  it('keeps the server error visible when login fails', async () => {
    vi.mocked(login).mockRejectedValue(new ApiError(401, '用户名或密码错误'))
    const wrapper = mountView()
    await submitWith(wrapper, 'alice', 'password-123')

    expect(wrapper.get('[role="alert"]').text()).toBe('用户名或密码错误')
    expect(routerReplace).not.toHaveBeenCalled()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
})
