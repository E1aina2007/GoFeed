import { afterEach, describe, expect, it } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'

import { useConfirmStore } from '@/stores/confirm'
import ConfirmDialog from '../ConfirmDialog.vue'

function mountHost() {
  const pinia = createPinia()
  const store = useConfirmStore(pinia)
  const wrapper = mount(ConfirmDialog, {
    global: { plugins: [pinia] },
    attachTo: document.body,
  })
  return { store, wrapper }
}

describe('ConfirmDialog', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('rejects a still-pending confirmation when a new one opens', async () => {
    const { store, wrapper } = mountHost()
    const first = store.confirm({ title: '第一个确认', message: 'a' })
    const second = store.confirm({ title: '第二个确认', message: 'b' })
    await flushPromises()

    await expect(first).resolves.toBe(false)
    expect(document.body.textContent).toContain('第二个确认')

    store.accept()
    await expect(second).resolves.toBe(true)
    wrapper.unmount()
  })
})
