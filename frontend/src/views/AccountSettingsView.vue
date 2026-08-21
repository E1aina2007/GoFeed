<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'

import { clearSession, currentUser } from '@/features/auth/session'
import { deleteAccount, updateName, updatePassword, updateProfile } from '@/features/user/api'
import { ApiError } from '@/lib/api'

const router = useRouter()
const name = ref(currentUser.value?.username ?? '')
const avatarURL = ref(currentUser.value?.avatar_url ?? '')
const bio = ref(currentUser.value?.bio ?? '')
const oldPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const nameMessage = ref('')
const profileMessage = ref('')
const passwordMessage = ref('')
const errorMessage = ref('')
const isSavingName = ref(false)
const isSavingProfile = ref(false)
const isSavingPassword = ref(false)
const isDeleting = ref(false)

function messageFor(error: unknown, fallback: string) {
  return error instanceof ApiError ? error.message : fallback
}

async function saveName() {
  nameMessage.value = ''
  errorMessage.value = ''
  if (!name.value.trim()) {
    errorMessage.value = '请输入用户名'
    return
  }
  isSavingName.value = true
  try {
    await updateName(name.value)
    nameMessage.value = '用户名已更新'
  } catch (error) {
    errorMessage.value = messageFor(error, '用户名更新失败，请稍后重试')
  } finally {
    isSavingName.value = false
  }
}

async function saveProfile() {
  profileMessage.value = ''
  errorMessage.value = ''
  if (!avatarURL.value.trim() && !bio.value.trim()) {
    errorMessage.value = '请至少填写头像地址或个人简介'
    return
  }
  isSavingProfile.value = true
  try {
    await updateProfile({
      ...(avatarURL.value.trim() ? { avatar_url: avatarURL.value.trim() } : {}),
      ...(bio.value.trim() ? { bio: bio.value.trim() } : {}),
    })
    profileMessage.value = '个人资料已更新'
  } catch (error) {
    errorMessage.value = messageFor(error, '资料更新失败，请稍后重试')
  } finally {
    isSavingProfile.value = false
  }
}

async function savePassword() {
  passwordMessage.value = ''
  errorMessage.value = ''
  if (newPassword.value !== confirmPassword.value) {
    errorMessage.value = '两次输入的新密码不一致'
    return
  }
  isSavingPassword.value = true
  try {
    await updatePassword(oldPassword.value, newPassword.value)
    clearSession()
    await router.replace({ name: 'login', query: { passwordUpdated: '1' } })
  } catch (error) {
    errorMessage.value = messageFor(error, '密码更新失败，请稍后重试')
  } finally {
    isSavingPassword.value = false
  }
}

async function removeAccount() {
  if (!window.confirm('注销后账号将立即从公开页面消失，确定继续吗？')) {
    return
  }
  isDeleting.value = true
  errorMessage.value = ''
  try {
    await deleteAccount()
    clearSession()
    await router.replace({ name: 'feed' })
  } catch (error) {
    errorMessage.value = messageFor(error, '注销失败，请稍后重试')
  } finally {
    isDeleting.value = false
  }
}
</script>

<template>
  <main class="settings-page">
    <header class="settings-header">
      <p class="eyebrow">账户</p>
      <h1>账户设置</h1>
    </header>

    <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>

    <section class="settings-section">
      <h2>用户名</h2>
      <form class="settings-form" @submit.prevent="saveName">
        <label class="form-field">
          <span>新的用户名</span>
          <input v-model="name" autocomplete="username" minlength="3" maxlength="32" required>
        </label>
        <button class="primary-action" type="submit" :disabled="isSavingName">{{ isSavingName ? '保存中' : '保存用户名' }}</button>
        <p v-if="nameMessage" class="success-message" role="status">{{ nameMessage }}</p>
      </form>
    </section>

    <section class="settings-section">
      <h2>公开资料</h2>
      <form class="settings-form" @submit.prevent="saveProfile">
        <label class="form-field">
          <span>头像地址</span>
          <input v-model="avatarURL" type="url" maxlength="512" placeholder="https://...">
        </label>
        <label class="form-field">
          <span>个人简介</span>
          <textarea v-model="bio" maxlength="255" rows="4"></textarea>
        </label>
        <p class="form-hint">当前接口只更新非空字段，暂不支持清空已有资料。</p>
        <button class="primary-action" type="submit" :disabled="isSavingProfile">{{ isSavingProfile ? '保存中' : '保存资料' }}</button>
        <p v-if="profileMessage" class="success-message" role="status">{{ profileMessage }}</p>
      </form>
    </section>

    <section class="settings-section">
      <h2>修改密码</h2>
      <form class="settings-form" @submit.prevent="savePassword">
        <label class="form-field">
          <span>当前密码</span>
          <input v-model="oldPassword" type="password" autocomplete="current-password" minlength="8" maxlength="72" required>
        </label>
        <label class="form-field">
          <span>新密码</span>
          <input v-model="newPassword" type="password" autocomplete="new-password" minlength="8" maxlength="72" required>
        </label>
        <label class="form-field">
          <span>确认新密码</span>
          <input v-model="confirmPassword" type="password" autocomplete="new-password" minlength="8" maxlength="72" required>
        </label>
        <p class="form-hint">修改成功后所有设备都会退出登录，需要重新登录。</p>
        <button class="primary-action" type="submit" :disabled="isSavingPassword">{{ isSavingPassword ? '保存中' : '修改密码' }}</button>
        <p v-if="passwordMessage" class="success-message" role="status">{{ passwordMessage }}</p>
      </form>
    </section>

    <section class="danger-section">
      <h2>注销账号</h2>
      <p>注销后账号会立即软删除，公开主页和视频将不可见。</p>
      <button class="danger-action" type="button" :disabled="isDeleting" @click="removeAccount">
        {{ isDeleting ? '处理中' : '注销账号' }}
      </button>
    </section>
  </main>
</template>

<style scoped>
.settings-page {
  min-height: calc(100dvh - 64px);
  padding: 36px 16px 64px;
  background: #f3f5f4;
}

.settings-header,
.settings-section,
.danger-section,
.form-error {
  width: min(100%, 760px);
  margin-right: auto;
  margin-left: auto;
}

.settings-header {
  margin-bottom: 20px;
}

.eyebrow {
  margin: 0 0 6px;
  color: var(--accent-strong);
  font-size: 0.86rem;
  font-weight: 750;
}

.settings-header h1,
.settings-section h2,
.danger-section h2 {
  margin: 0;
  color: var(--ink-strong);
}

.settings-header h1 {
  font-size: 1.7rem;
}

.settings-section,
.danger-section {
  border-top: 1px solid var(--border-subtle);
  padding: 24px 0;
}

.settings-section h2,
.danger-section h2 {
  font-size: 1.1rem;
}

.settings-form {
  width: min(100%, 520px);
  margin-top: 18px;
}

.form-field {
  display: grid;
  gap: 8px;
  margin-top: 16px;
  color: var(--ink-strong);
  font-size: 0.9rem;
  font-weight: 650;
}

.form-field input,
.form-field textarea {
  width: 100%;
  border: 1px solid #b8c0bd;
  border-radius: 4px;
  padding: 10px;
  color: var(--ink-strong);
  background: #ffffff;
}

.form-field input {
  min-height: 42px;
}

.primary-action,
.danger-action {
  min-height: 42px;
  margin-top: 18px;
  border-radius: 4px;
  padding: 0 16px;
  font-weight: 700;
  cursor: pointer;
}

.primary-action {
  border: 1px solid var(--accent-strong);
  color: #ffffff;
  background: var(--accent);
}

.danger-action {
  border: 1px solid #ae2c20;
  color: #ae2c20;
  background: transparent;
}

.primary-action:disabled,
.danger-action:disabled {
  cursor: wait;
  opacity: 0.6;
}

.form-error {
  color: #ae2c20;
  line-height: 1.5;
}

.form-hint,
.danger-section p {
  color: var(--ink-muted);
  font-size: 0.86rem;
  line-height: 1.5;
}

.success-message {
  color: var(--accent-strong);
  font-size: 0.88rem;
}

@media (max-width: 640px) {
  .settings-page {
    min-height: calc(100dvh - 56px);
    padding: 24px 12px 40px;
  }
}
</style>
