<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { register } from '@/features/auth/session'
import { ApiError } from '@/lib/api'
import { useToastStore } from '@/stores/toast'

const route = useRoute()
const router = useRouter()
const toast = useToastStore()
const username = ref('')
const password = ref('')
const confirmPassword = ref('')
const isSubmitting = ref(false)
const errorMessage = ref('')

const redirectPath = computed(() => {
  const redirect = route.query.redirect
  return typeof redirect === 'string' && redirect.startsWith('/') ? redirect : ''
})

const loginLocation = computed(() => ({
  name: 'login',
  query: redirectPath.value ? { redirect: redirectPath.value } : {},
}))

async function submit() {
  errorMessage.value = ''
  if (password.value !== confirmPassword.value) {
    errorMessage.value = '两次输入的密码不一致'
    return
  }

  isSubmitting.value = true
  try {
    await register({ username: username.value.trim(), password: password.value })
    await router.replace({
      name: 'login',
      query: {
        ...(redirectPath.value ? { redirect: redirectPath.value } : {}),
        registered: '1',
      },
    })
    toast.success('注册成功，请登录')
  } catch (error) {
    errorMessage.value = error instanceof ApiError ? error.message : '注册失败，请检查网络后重试'
    toast.error(errorMessage.value)
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <main class="account-page">
    <form class="account-form" @submit.prevent="submit">
      <header class="account-form__header">
        <p>GoFeed</p>
        <h1>注册</h1>
      </header>

      <label class="form-field">
        <span>用户名</span>
        <input v-model="username" name="username" autocomplete="username" minlength="3" maxlength="32" required>
      </label>

      <label class="form-field">
        <span>密码</span>
        <input v-model="password" name="password" type="password" autocomplete="new-password" minlength="8" maxlength="72" required>
      </label>

      <label class="form-field">
        <span>确认密码</span>
        <input v-model="confirmPassword" name="confirm-password" type="password" autocomplete="new-password" minlength="8" maxlength="72" required>
      </label>

      <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>

      <button class="primary-action" type="submit" :disabled="isSubmitting">
        {{ isSubmitting ? '正在注册' : '注册' }}
      </button>

      <p class="account-switch">
        已有账号？
        <RouterLink :to="loginLocation">登录</RouterLink>
      </p>
    </form>
  </main>
</template>

<style scoped>
.account-page {
  display: grid;
  min-height: calc(100dvh - 64px);
  place-items: center;
  padding: 32px 16px;
  background: #f3f5f4;
}

.account-form {
  width: min(100%, 420px);
  border: 1px solid #d7dcda;
  border-radius: 8px;
  padding: 32px;
  background: #ffffff;
}

.account-form__header {
  margin-bottom: 28px;
}

.account-form__header p,
.account-form__header h1 {
  margin: 0;
}

.account-form__header p {
  margin-bottom: 8px;
  color: var(--accent-strong);
  font-size: 0.9rem;
  font-weight: 750;
}

.account-form__header h1 {
  color: var(--ink-strong);
  font-size: 1.55rem;
  line-height: 1.2;
}

.form-field {
  display: grid;
  gap: 8px;
  margin-top: 18px;
  color: var(--ink-strong);
  font-size: 0.9rem;
  font-weight: 650;
}

.form-field input {
  width: 100%;
  min-height: 42px;
  border: 1px solid #b8c0bd;
  border-radius: 4px;
  padding: 8px 10px;
  color: var(--ink-strong);
  background: #ffffff;
}

.form-error {
  margin: 18px 0 0;
  color: #ae2c20;
  font-size: 0.9rem;
  line-height: 1.45;
}

.primary-action {
  width: 100%;
  min-height: 44px;
  margin-top: 24px;
  border: 1px solid var(--accent-strong);
  border-radius: 4px;
  color: #ffffff;
  background: var(--accent);
  font-weight: 700;
  cursor: pointer;
}

.primary-action:hover:not(:disabled) {
  background: var(--accent-strong);
}

.primary-action:disabled {
  opacity: 0.62;
  cursor: wait;
}

.account-switch {
  margin: 18px 0 0;
  color: var(--ink-muted);
  font-size: 0.88rem;
  text-align: center;
}

.account-switch a {
  color: var(--accent-strong);
  font-weight: 700;
}

@media (max-width: 640px) {
  .account-page {
    min-height: calc(100dvh - 56px);
    padding: 20px 12px;
  }

  .account-form {
    padding: 24px 20px;
  }
}
</style>
