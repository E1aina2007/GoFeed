import { nextTick, onBeforeUnmount, toValue, watch } from 'vue'
import type { MaybeRefOrGetter, Ref } from 'vue'

const focusableSelector = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

// 模态对话框共享行为：打开时聚焦首个可交互元素并把 Tab 圈定在对话框内，
// Escape 触发关闭回调，关闭或卸载后把焦点还原到打开前的元素
export function useDialogA11y(
  open: MaybeRefOrGetter<boolean>,
  dialogRef: Ref<HTMLElement | null>,
  onEscape: () => void,
) {
  let previousFocus: HTMLElement | null = null

  function focusableElements(): HTMLElement[] {
    const dialog = dialogRef.value
    return dialog ? Array.from(dialog.querySelectorAll<HTMLElement>(focusableSelector)) : []
  }

  function containsFocus(): boolean {
    const dialog = dialogRef.value
    return Boolean(dialog && dialog.contains(document.activeElement))
  }

  function focusFirst() {
    const [first] = focusableElements()
    if (first) {
      first.focus()
      return
    }
    dialogRef.value?.focus()
  }

  function restoreFocus() {
    if (previousFocus && previousFocus.isConnected) {
      previousFocus.focus()
    }
    previousFocus = null
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      onEscape()
      return
    }
    if (event.key !== 'Tab') {
      return
    }

    const elements = focusableElements()
    const first = elements[0]
    const last = elements[elements.length - 1]
    if (!first || !last) {
      event.preventDefault()
      return
    }
    if (event.shiftKey) {
      if (!containsFocus() || document.activeElement === first) {
        event.preventDefault()
        last.focus()
      }
      return
    }
    if (!containsFocus() || document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }

  const stopWatch = watch(
    () => toValue(open),
    (isOpen) => {
      if (isOpen) {
        previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
        document.addEventListener('keydown', handleKeydown, true)
        void nextTick(focusFirst)
      } else {
        document.removeEventListener('keydown', handleKeydown, true)
        restoreFocus()
      }
    },
    { immediate: true },
  )

  onBeforeUnmount(() => {
    stopWatch()
    document.removeEventListener('keydown', handleKeydown, true)
    restoreFocus()
  })
}
