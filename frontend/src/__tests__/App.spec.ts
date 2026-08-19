import { describe, it, expect } from 'vitest'

import { mount } from '@vue/test-utils'
import App from '../App.vue'

describe('App', () => {
  it('renders the application shell', () => {
    const wrapper = mount(App, {
      global: {
        stubs: {
          RouterLink: { template: '<a><slot /></a>' },
          RouterView: { template: '<div />' },
        },
      },
    })
    expect(wrapper.text()).toContain('GoFeed')
    expect(wrapper.text()).toContain('发现')
    expect(wrapper.text()).toContain('注册')
  })
})
