import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { Router } from 'vue-router'

import { register } from '@/features/auth/session'
import { ApiError } from '@/lib/api'
import RegisterView from '../RegisterView.vue'

const route = vi.hoisted(() => ({ query: {} as Record<string, unknown> }))
const routerReplace = vi.hoisted(() => vi.fn<Router['replace']>())
const toastError = vi.hoisted(() => vi.fn<(message: string) => void>())
const toastSuccess = vi.hoisted(() => vi.fn<(message: string) => void>())

vi.mock('vue-router', () => ({
  RouterLink: { template: '<a><slot /></a>' },
  useRoute: () => route,
  useRouter: () => ({ replace: routerReplace }),
}))

vi.mock('@/features/auth/session', () => ({
  register: vi.fn<typeof register>(),
}))

// toast 的自动消失定时器会干扰倒计时断言，这里只断言提示内容
vi.mock('@/stores/toast', () => ({
  useToastStore: () => ({ success: toastSuccess, error: toastError }),
}))

function mountView() {
  return mount(RegisterView)
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
    toastError.mockClear()
    vi.mocked(register).mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
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

  it('counts down the server retry-after window after a 429 and keeps the retry entry', async () => {
    vi.useFakeTimers()
    vi.mocked(register).mockRejectedValue(new ApiError(429, 'rate limit exceeded', 2))
    const wrapper = mountView()
    await submitWith(wrapper, 'alice', 'password-123')

    expect(register).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[role="alert"]').text()).toBe('请求过于频繁，请 2 秒后重试')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    expect(toastError).toHaveBeenCalledWith('请求过于频繁，请 2 秒后重试')

    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()

    vi.mocked(register).mockResolvedValue({ user: { id: 7, username: 'alice' } })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(register).toHaveBeenCalledTimes(2)
    expect(routerReplace).toHaveBeenCalledWith({ name: 'login', query: { registered: '1' } })
    wrapper.unmount()
  })

  it('stops the countdown timer when the page unmounts', async () => {
    vi.useFakeTimers()
    vi.mocked(register).mockRejectedValue(new ApiError(429, 'rate limit exceeded', 60))
    const wrapper = mountView()
    await submitWith(wrapper, 'alice', 'password-123')
    expect(vi.getTimerCount()).toBe(1)

    wrapper.unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})
