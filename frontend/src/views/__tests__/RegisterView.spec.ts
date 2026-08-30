import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import type { Router } from 'vue-router'

import { register } from '@/features/auth/session'
import { ApiError } from '@/lib/api'
import RegisterView from '../RegisterView.vue'

const route = vi.hoisted(() => ({ query: {} as Record<string, unknown> }))
const routerReplace = vi.hoisted(() => vi.fn<Router['replace']>())

vi.mock('vue-router', () => ({
  RouterLink: { template: '<a><slot /></a>' },
  useRoute: () => route,
  useRouter: () => ({ replace: routerReplace }),
}))

vi.mock('@/features/auth/session', () => ({
  register: vi.fn<typeof register>(),
}))

function mountView() {
  return mount(RegisterView, { global: { plugins: [createPinia()] } })
}

async function submitWith(
  wrapper: ReturnType<typeof mountView>,
  username: string,
  password: string,
  confirmPassword = password,
) {
  const inputs = wrapper.findAll('input')
  await inputs[0]?.setValue(username)
  await inputs[1]?.setValue(password)
  await inputs[2]?.setValue(confirmPassword)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

describe('RegisterView', () => {
  beforeEach(() => {
    route.query = {}
    routerReplace.mockClear()
    vi.mocked(register).mockReset()
  })

  it('rejects mismatched passwords without calling the API', async () => {
    const wrapper = mountView()
    await submitWith(wrapper, 'alice', 'password-123', 'different-123')

    expect(wrapper.get('[role="alert"]').text()).toBe('两次输入的密码不一致')
    expect(register).not.toHaveBeenCalled()
    expect(routerReplace).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('registers the account and redirects to login with the notice flag', async () => {
    route.query = { redirect: '/video/80' }
    vi.mocked(register).mockResolvedValue({ user: { id: 7, username: 'alice' } })
    const wrapper = mountView()
    await submitWith(wrapper, '  alice  ', 'password-123')

    expect(register).toHaveBeenCalledWith({ username: 'alice', password: 'password-123' })
    expect(routerReplace).toHaveBeenCalledWith({
      name: 'login',
      query: { redirect: '/video/80', registered: '1' },
    })
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps the server error visible when registration fails', async () => {
    vi.mocked(register).mockRejectedValue(new ApiError(409, 'username already exists'))
    const wrapper = mountView()
    await submitWith(wrapper, 'alice', 'password-123')

    expect(wrapper.get('[role="alert"]').text()).toBe('用户名已被占用')
    expect(routerReplace).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
