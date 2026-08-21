import { defineStore } from 'pinia'
import { ref } from 'vue'

export type ToastType = 'success' | 'error' | 'info'

export type Toast = {
  id: number
  type: ToastType
  message: string
}

let nextID = 1

export const useToastStore = defineStore('toast', () => {
  const toasts = ref<Toast[]>([])

  function remove(id: number) {
    toasts.value = toasts.value.filter((toast) => toast.id !== id)
  }

  function push(type: ToastType, message: string, ttlMS = 2600) {
    const id = nextID++
    toasts.value.push({ id, type, message })
    window.setTimeout(() => remove(id), ttlMS)
  }

  function success(message: string) {
    push('success', message)
  }

  function error(message: string) {
    push('error', message, 3600)
  }

  function info(message: string) {
    push('info', message)
  }

  return { toasts, push, remove, success, error, info }
})
