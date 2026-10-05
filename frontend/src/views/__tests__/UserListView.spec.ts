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

  it('shows the error message and recovers through the retry button', async () => {
    vi.mocked(listUsers)
      .mockRejectedValueOnce(new ApiError(500, 'user operation failed'))
      .mockResolvedValueOnce({ users: [{ id: 3, username: 'cora' }] })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('服务暂时不可用，请稍后重试')
    await wrapper.get('.secondary-action').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('@cora')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('loads the next user page without duplicating entries and retries a page failure', async () => {
    vi.mocked(listUsers)
      .mockResolvedValueOnce({
        users: [
          { id: 1, username: 'alice' },
          { id: 2, username: 'bob' },
        ],
        next_cursor: 'users-page-2',
      })
      .mockRejectedValueOnce(new ApiError(500, 'user operation failed'))
      .mockResolvedValueOnce({
        users: [
          { id: 2, username: 'bob' },
          { id: 3, username: 'cora' },
        ],
      })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('.more-button').trigger('click')
    await flushPromises()
    expect(wrapper.get('.inline-error').text()).toContain('服务暂时不可用，请稍后重试')

    await wrapper.get('.more-button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('@cora')
    expect(wrapper.find('.inline-error').exists()).toBe(false)
    expect(wrapper.findAll('.user-item')).toHaveLength(3)
    wrapper.unmount()
  })
})
