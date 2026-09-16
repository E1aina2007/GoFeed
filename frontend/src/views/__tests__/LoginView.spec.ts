import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { Router } from 'vue-router'

import { login } from '@/features/auth/session'
import { ApiError } from '@/lib/api'
import LoginView from '../LoginView.vue'

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
  login: vi.fn<typeof login>(),
}))

// toast 的自动消失定时器会干扰倒计时断言，这里只断言提示内容
vi.mock('@/stores/toast', () => ({
  useToastStore: () => ({ success: toastSuccess, error: toastError }),
}))

const sessionFixture = {
  access_token: 'access-token',
  refresh_token: 'refresh-token',
  expires_at: '2026-08-30T08:00:00Z',
  user: { id: 42, username: 'alice' },
}

function mountView() {
  return mount(LoginView)
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
    toastError.mockClear()
    vi.mocked(login).mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
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
    vi.mocked(login).mockRejectedValue(new ApiError(401, 'invalid username or password'))
    const wrapper = mountView()
    await submitWith(wrapper, 'alice', 'password-123')

    expect(wrapper.get('[role="alert"]').text()).toBe('用户名或密码错误')
    expect(routerReplace).not.toHaveBeenCalled()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('shows the server retry-after countdown after a 429 and blocks duplicate submits', async () => {
    vi.useFakeTimers()
    vi.mocked(login).mockRejectedValue(new ApiError(429, 'rate limit exceeded', 3))
    const wrapper = mountView()
    await submitWith(wrapper, 'alice', 'password-123')

    expect(login).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[role="alert"]').text()).toBe('请求过于频繁，请 3 秒后重试')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    expect(toastError).toHaveBeenCalledWith('请求过于频繁，请 3 秒后重试')

    // 等待期内重复提交不应再消耗服务端登录额度
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(login).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(1000)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('请求过于频繁，请 2 秒后重试')

    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()

    // 倒计时结束后沿用按钮本身作为重试入口
    vi.mocked(login).mockResolvedValue(sessionFixture)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(login).toHaveBeenCalledTimes(2)
    expect(routerReplace).toHaveBeenCalledWith('/')
    wrapper.unmount()
  })

  it('stops the countdown timer when the page unmounts', async () => {
    vi.useFakeTimers()
    vi.mocked(login).mockRejectedValue(new ApiError(429, 'rate limit exceeded', 30))
    const wrapper = mountView()
    await submitWith(wrapper, 'alice', 'password-123')
    expect(vi.getTimerCount()).toBe(1)

    wrapper.unmount()
    expect(vi.getTimerCount()).toBe(0)

    vi.advanceTimersByTime(5000)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps the 429 message without a countdown when the header is unusable', async () => {
    vi.mocked(login).mockRejectedValue(new ApiError(429, 'rate limit exceeded'))
    const wrapper = mountView()
    await submitWith(wrapper, 'alice', 'password-123')

    expect(wrapper.get('[role="alert"]').text()).toBe('请求过于频繁，请稍后重试')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
})
