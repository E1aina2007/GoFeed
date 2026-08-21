<script setup lang="ts">
import { useToastStore } from '@/stores/toast'

const toast = useToastStore()
</script>

<template>
  <div class="toast-host" aria-live="polite" aria-relevant="additions removals">
    <div v-for="item in toast.toasts" :key="item.id" class="toast" :class="`toast--${item.type}`" role="status">
      <span class="toast__message">{{ item.message }}</span>
      <button class="toast__close" type="button" aria-label="关闭提示" title="关闭提示" @click="toast.remove(item.id)">
        ×
      </button>
    </div>
  </div>
</template>

<style scoped>
.toast-host {
  position: fixed;
  z-index: 40;
  top: 76px;
  left: 50%;
  display: grid;
  width: min(520px, calc(100vw - 24px));
  gap: 8px;
  pointer-events: none;
  transform: translateX(-50%);
}

.toast {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  padding: 10px 12px;
  color: #ffffff;
  background: #1a2023f2;
  box-shadow: 0 8px 24px #00000038;
  pointer-events: auto;
}

.toast--success {
  border-color: #4c8e83;
}

.toast--error {
  border-color: #ae2c20;
}

.toast--info {
  border-color: #5d83a2;
}

.toast__message {
  min-width: 0;
  font-size: 0.88rem;
  line-height: 1.4;
  overflow-wrap: anywhere;
}

.toast__close {
  width: 28px;
  height: 28px;
  border: 0;
  border-radius: 4px;
  color: #ffffff;
  background: #ffffff1a;
  font-size: 1.2rem;
  line-height: 1;
  cursor: pointer;
}

@media (max-width: 640px) {
  .toast-host {
    top: 66px;
  }
}
</style>
