import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'

import PublishVideoView from '../PublishVideoView.vue'

describe('PublishVideoView', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('requires a title before starting an upload', async () => {
    const wrapper = mount(PublishVideoView, { global: { plugins: [createPinia()], stubs: { RouterLink: true } } })

    await wrapper.get('form').trigger('submit')

    expect(wrapper.get('[role="alert"]').text()).toBe('请填写视频标题')
  })

  it('requires both media files after the title is provided', async () => {
    const wrapper = mount(PublishVideoView, { global: { plugins: [createPinia()], stubs: { RouterLink: true } } })
    await wrapper.get('input').setValue('春日散步')

    await wrapper.get('form').trigger('submit')

    expect(wrapper.get('[role="alert"]').text()).toBe('请选择一个视频文件')
  })
})
