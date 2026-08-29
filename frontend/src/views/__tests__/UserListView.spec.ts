import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'

import { listUsers } from '@/features/user/api'
import { ApiError } from '@/lib/api'
import UserListView from '../UserListView.vue'

vi.mock('vue-router', () => ({
  RouterLink: { template: '<a><slot /></a>' },
}))

vi.mock('@/features/user/api', () => ({
  listUsers: vi.fn<typeof listUsers>(),
}))

function mountView() {
  return mount(UserListView, { global: { plugins: [createPinia()] } })
}

describe('UserListView', () => {
  beforeEach(() => {
    vi.mocked(listUsers).mockReset()
  })

  it('renders the user entries with the bio fallback', async () => {
    vi.mocked(listUsers).mockResolvedValue({
      users: [
        { id: 1, username: 'alice', bio: '简介内容' },
        { id: 2, username: 'bob' },
      ],
    })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('@alice')
    expect(wrapper.text()).toContain('简介内容')
    expect(wrapper.text()).toContain('@bob')
    expect(wrapper.text()).toContain('暂无简介')
    wrapper.unmount()
  })

  it('shows the empty state when no users exist', async () => {
    vi.mocked(listUsers).mockResolvedValue({ users: [] })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('暂时没有用户')
    wrapper.unmount()
  })

  it('shows the error message and recovers through the retry button', async () => {
    vi.mocked(listUsers)
      .mockRejectedValueOnce(new ApiError(500, '用户服务暂不可用'))
      .mockResolvedValueOnce({ users: [{ id: 3, username: 'cora' }] })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('用户服务暂不可用')
    await wrapper.get('.secondary-action').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('@cora')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
