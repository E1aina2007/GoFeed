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

  it('resolves true when the user accepts a dangerous action', async () => {
    const { store, wrapper } = mountHost()
    const pending = store.confirm({
      title: '删除视频',
      message: '确定删除“春日散步”吗？',
      confirmText: '删除',
      danger: true,
    })
    await flushPromises()

    expect(document.body.textContent).toContain('删除视频')
    expect(document.body.textContent).toContain('确定删除“春日散步”吗？')
    const dangerButton = document.body.querySelector<HTMLButtonElement>(
      '.confirm-dialog__button--danger',
    )
    expect(dangerButton?.textContent).toContain('删除')

    dangerButton?.click()
    await expect(pending).resolves.toBe(true)
    expect(store.open).toBe(false)
    wrapper.unmount()
  })

  it('resolves false when cancelled by Escape or the cancel button', async () => {
    const { store, wrapper } = mountHost()
    const first = store.confirm({ title: '删除评论', message: '确定删除这条评论吗？' })
    await flushPromises()

    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await expect(first).resolves.toBe(false)
    expect(store.open).toBe(false)

    const second = store.confirm({ title: '放弃草稿', message: '确定放弃当前草稿吗？' })
    await flushPromises()
    const buttons = document.body.querySelectorAll<HTMLButtonElement>('.confirm-dialog__button')
    expect(buttons[0]?.textContent).toContain('取消')
    buttons[0]?.click()
    await expect(second).resolves.toBe(false)
    wrapper.unmount()
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
