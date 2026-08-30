<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'

import { clearSession, currentUser } from '@/features/auth/session'
import { deleteAccount, updateName, updatePassword, updateProfile, uploadAvatar } from '@/features/user/api'
import { apiUserMessage } from '@/lib/api'
import { useConfirmStore } from '@/stores/confirm'
import { useToastStore } from '@/stores/toast'

const router = useRouter()
const toast = useToastStore()
const confirmStore = useConfirmStore()
const name = ref(currentUser.value?.username ?? '')
const avatarInput = ref<HTMLInputElement>()
const avatarFile = ref<File>()
const avatarPreviewURL = ref('')
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
const avatarSource = computed(() => avatarPreviewURL.value || currentUser.value?.avatar_url || '')

const avatarInitial = computed(() => currentUser.value?.username.slice(0, 1).toUpperCase() || '?')

function releaseAvatarPreview() {
  if (avatarPreviewURL.value) {
    if (typeof URL.revokeObjectURL === 'function') {
      URL.revokeObjectURL(avatarPreviewURL.value)
    }
    avatarPreviewURL.value = ''
  }
}

function selectAvatar(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  errorMessage.value = ''
  avatarFile.value = undefined
  releaseAvatarPreview()

  if (!file) {
    return
  }
  const extension = file.name.toLowerCase().split('.').pop()
  if (!extension || !['jpg', 'jpeg', 'png', 'webp'].includes(extension)) {
    errorMessage.value = '头像只支持 JPG、PNG 或 WebP 图片'
    input.value = ''
    return
  }
  if (file.size <= 0 || file.size > 10 * 1024 * 1024) {
    errorMessage.value = '头像文件不能超过 10 MiB'
    input.value = ''
    return
  }

  avatarFile.value = file
  if (typeof URL.createObjectURL === 'function') {
    avatarPreviewURL.value = URL.createObjectURL(file)
  }
}

function messageFor(error: unknown, fallback: string) {
  return apiUserMessage(error, fallback, {
    403: '当前密码不正确',
    409: '用户名已被占用',
    413: '头像文件不能超过 10 MiB',
  })
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
    toast.success('用户名已更新')
  } catch (error) {
    errorMessage.value = messageFor(error, '用户名更新失败，请稍后重试')
    toast.error(errorMessage.value)
  } finally {
    isSavingName.value = false
  }
}

async function saveProfile() {
  profileMessage.value = ''
  errorMessage.value = ''
  if (!avatarFile.value && !bio.value.trim()) {
    errorMessage.value = '请选择头像文件或填写个人简介'
    return
  }
  isSavingProfile.value = true
  let avatarUploaded = false
  try {
    if (avatarFile.value) {
      await uploadAvatar(avatarFile.value)
      avatarUploaded = true
      avatarFile.value = undefined
      releaseAvatarPreview()
      if (avatarInput.value) {
        avatarInput.value.value = ''
      }
    }
    if (bio.value.trim()) {
      await updateProfile({ bio: bio.value.trim() })
    }
    profileMessage.value = '个人资料已更新'
    toast.success('个人资料已更新')
  } catch (error) {
    errorMessage.value = messageFor(error, avatarUploaded ? '头像已更新，但简介保存失败，请稍后重试' : '资料更新失败，请稍后重试')
    toast.error(errorMessage.value)
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
    toast.success('密码已更新，请重新登录')
    await router.replace({ name: 'login', query: { passwordUpdated: '1' } })
  } catch (error) {
    errorMessage.value = messageFor(error, '密码更新失败，请稍后重试')
    toast.error(errorMessage.value)
  } finally {
    isSavingPassword.value = false
  }
}

async function removeAccount() {
  const confirmed = await confirmStore.confirm({
    title: '注销账号',
    message: '注销后账号将立即从公开页面消失，确定继续吗？',
    confirmText: '注销',
    danger: true,
  })
  if (!confirmed) {
    return
  }
  isDeleting.value = true
  errorMessage.value = ''
  try {
    await deleteAccount()
    clearSession()
    toast.info('账号已注销')
    await router.replace({ name: 'feed' })
  } catch (error) {
    errorMessage.value = messageFor(error, '注销失败，请稍后重试')
    toast.error(errorMessage.value)
  } finally {
    isDeleting.value = false
  }
}

onBeforeUnmount(releaseAvatarPreview)
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
        <div class="avatar-editor">
          <div class="avatar-preview">
            <img v-if="avatarSource" :src="avatarSource" :alt="`${currentUser?.username || '用户'} 的头像预览`">
            <span v-else>{{ avatarInitial }}</span>
          </div>
          <label class="file-field">
            <span>头像文件</span>
            <input
              ref="avatarInput"
              accept=".jpg,.jpeg,.png,.webp,image/jpeg,image/png,image/webp"
              type="file"
              :disabled="isSavingProfile"
              @change="selectAvatar"
            >
            <small>JPG、PNG 或 WebP，最大 10 MiB；当前使用本地媒体存储</small>
          </label>
        </div>
        <label class="form-field">
          <span>个人简介</span>
          <textarea v-model="bio" maxlength="255" rows="4" :disabled="isSavingProfile"></textarea>
        </label>
        <p class="form-hint">头像通过文件上传更新；对象存储 URL 仍保留接口兼容能力。</p>
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

.avatar-editor {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: center;
  gap: 16px;
}

.avatar-preview {
  display: grid;
  width: 76px;
  height: 76px;
  place-items: center;
  overflow: hidden;
  border: 1px solid #b8c0bd;
  border-radius: 50%;
  color: #ffffff;
  background: var(--accent);
  font-size: 1.5rem;
  font-weight: 750;
}

.avatar-preview img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.file-field {
  display: grid;
  gap: 8px;
  min-width: 0;
  color: var(--ink-strong);
  font-size: 0.9rem;
  font-weight: 650;
}

.file-field input {
  width: 100%;
  min-height: 42px;
  border: 1px solid #b8c0bd;
  border-radius: 4px;
  padding: 8px;
  color: var(--ink-strong);
  background: #ffffff;
}

.file-field small {
  color: var(--ink-muted);
  font-size: 0.78rem;
  font-weight: 400;
  line-height: 1.45;
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

  .avatar-editor {
    grid-template-columns: 1fr;
  }
}
</style>
