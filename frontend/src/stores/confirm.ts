import { defineStore } from 'pinia'
import { ref } from 'vue'

export type ConfirmOptions = {
  title: string
  message: string
  confirmText?: string
  cancelText?: string
  danger?: boolean
}

// 全局确认对话框状态，配合 App 挂载的 ConfirmDialog 使用；
// confirm 返回 Promise，resolve 值表示用户是否确认
export const useConfirmStore = defineStore('confirm', () => {
  const open = ref(false)
  const title = ref('')
  const message = ref('')
  const confirmText = ref('确认')
  const cancelText = ref('取消')
  const danger = ref(false)
  let pending: ((accepted: boolean) => void) | null = null

  function settle(accepted: boolean) {
    open.value = false
    const resolve = pending
    pending = null
    resolve?.(accepted)
  }

  function confirm(options: ConfirmOptions): Promise<boolean> {
    // 打开新的确认前先拒绝未结算的旧确认，避免悬挂的 Promise
    settle(false)
    title.value = options.title
    message.value = options.message
    confirmText.value = options.confirmText ?? '确认'
    cancelText.value = options.cancelText ?? '取消'
    danger.value = options.danger ?? false
    open.value = true
    return new Promise((resolve) => {
      pending = resolve
    })
  }

  function accept() {
    settle(true)
  }

  function cancel() {
    settle(false)
  }

  return { open, title, message, confirmText, cancelText, danger, confirm, accept, cancel }
})
