<script setup lang="ts">
import { ref } from 'vue'

import { useDialogA11y } from '@/lib/dialog'
import { useConfirmStore } from '@/stores/confirm'

// 全局确认对话框的展示层，确认语义由 stores/confirm 驱动；
// 危险操作传入 danger 时确认按钮显示为警示色
const store = useConfirmStore()
const dialogRef = ref<HTMLElement | null>(null)

useDialogA11y(
  () => store.open,
  dialogRef,
  store.cancel,
)
</script>

<template>
  <Teleport to="body">
    <section v-if="store.open" class="confirm-backdrop" role="presentation" @click.self="store.cancel">
      <div
        ref="dialogRef"
        class="confirm-dialog"
        role="alertdialog"
        aria-modal="true"
        tabindex="-1"
        :aria-label="store.title"
      >
        <h2 class="confirm-dialog__title">{{ store.title }}</h2>
        <p class="confirm-dialog__message">{{ store.message }}</p>
        <div class="confirm-dialog__actions">
          <button class="confirm-dialog__button" type="button" @click="store.cancel">
            {{ store.cancelText }}
          </button>
          <button
            class="confirm-dialog__button confirm-dialog__button--confirm"
            :class="{ 'confirm-dialog__button--danger': store.danger }"
            type="button"
            @click="store.accept"
          >
            {{ store.confirmText }}
          </button>
        </div>
      </div>
    </section>
  </Teleport>
</template>

<style scoped>
.confirm-backdrop {
  --confirm-surface: #182020;
  --confirm-surface-raised: #202b2a;
  --confirm-border: #2b3a38;
  --confirm-border-strong: #3a504c;
  --confirm-ink: #edf2f0;
  --confirm-muted: #a4b5b1;
  --confirm-accent: #55d2b4;
  --confirm-danger: #e8847a;
  --confirm-danger-strong: #d4695f;
  position: fixed;
  z-index: 60;
  inset: 0;
  display: grid;
  place-items: center;
  padding: 16px;
  background: #060b0ba8;
}

.confirm-dialog {
  width: min(100%, 400px);
  border: 1px solid var(--confirm-border-strong);
  border-radius: 8px;
  padding: 18px 16px 16px;
  color: var(--confirm-ink);
  background: var(--confirm-surface);
}

.confirm-dialog__title {
  margin: 0 0 8px;
  font-size: 1.02rem;
}

.confirm-dialog__message {
  margin: 0 0 18px;
  overflow-wrap: anywhere;
  color: var(--confirm-muted);
  font-size: 0.9rem;
}

.confirm-dialog__actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}

.confirm-dialog__button {
  min-height: 36px;
  border: 1px solid var(--confirm-border-strong);
  border-radius: 6px;
  padding: 7px 14px;
  color: var(--confirm-ink);
  background: var(--confirm-surface-raised);
  font-size: 0.86rem;
  cursor: pointer;
}

.confirm-dialog__button:hover {
  border-color: var(--confirm-accent);
}

.confirm-dialog__button--confirm {
  border-color: var(--confirm-border-strong);
  color: #062922;
  background: var(--confirm-accent);
  font-weight: 700;
}

.confirm-dialog__button--confirm:hover {
  background: #63ddc0;
}

.confirm-dialog__button--danger {
  color: #fff5f3;
  background: var(--confirm-danger);
}

.confirm-dialog__button--danger:hover {
  border-color: var(--confirm-danger);
  background: var(--confirm-danger-strong);
}

@media (max-width: 640px) {
  .confirm-backdrop {
    align-items: end;
    padding: 0;
  }

  .confirm-dialog {
    width: 100%;
    border-radius: 8px 8px 0 0;
  }
}
</style>
