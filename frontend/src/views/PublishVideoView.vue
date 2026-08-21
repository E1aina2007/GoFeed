<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'

import {
  publishVideo,
  uploadCover,
  uploadVideo,
  type UploadedCover,
  type UploadedVideo,
} from '@/features/video/api'
import { ApiError } from '@/lib/api'
import { useToastStore } from '@/stores/toast'

const maxVideoSize = 200 * 1024 * 1024
const maxCoverSize = 10 * 1024 * 1024
const videoExtension = /\.(mp4|webm|mov)$/i
const coverExtension = /\.(jpg|jpeg|png|webp)$/i

const router = useRouter()
const toast = useToastStore()
const title = ref('')
const description = ref('')
const videoFile = ref<File>()
const coverFile = ref<File>()
const uploadedVideo = ref<UploadedVideo>()
const uploadedCover = ref<UploadedCover>()
const currentStage = ref('')
const uploadProgress = ref(0)
const errorMessage = ref('')
const isSubmitting = ref(false)

const progressLabel = computed(() => `${Math.round(uploadProgress.value * 100)}%`)

function selectVideo(event: Event) {
  const input = event.target as HTMLInputElement
  videoFile.value = input.files?.[0]
  uploadedVideo.value = undefined
  errorMessage.value = ''
}

function selectCover(event: Event) {
  const input = event.target as HTMLInputElement
  coverFile.value = input.files?.[0]
  uploadedCover.value = undefined
  errorMessage.value = ''
}

function formatFileSize(size: number) {
  return `${(size / 1024 / 1024).toFixed(size >= 10 * 1024 * 1024 ? 0 : 1)} MiB`
}

function validationError() {
  if (!title.value.trim()) {
    return '请填写视频标题'
  }
  if (!videoFile.value) {
    return '请选择一个视频文件'
  }
  if (!videoExtension.test(videoFile.value.name) || videoFile.value.size === 0 || videoFile.value.size > maxVideoSize) {
    return '视频仅支持不超过 200 MiB 的 MP4、WebM 或 MOV 文件'
  }
  if (!coverFile.value) {
    return '请选择一张封面图片'
  }
  if (!coverExtension.test(coverFile.value.name) || coverFile.value.size === 0 || coverFile.value.size > maxCoverSize) {
    return '封面仅支持不超过 10 MiB 的 JPG、PNG 或 WebP 文件'
  }
  return ''
}

async function submit() {
  errorMessage.value = validationError()
  if (errorMessage.value || !videoFile.value || !coverFile.value) {
    return
  }

  isSubmitting.value = true
  try {
    if (!uploadedVideo.value) {
      currentStage.value = '正在上传视频'
      uploadProgress.value = 0
      uploadedVideo.value = await uploadVideo(videoFile.value, (progress) => {
        uploadProgress.value = progress
      })
    }

    if (!uploadedCover.value) {
      currentStage.value = '正在上传封面'
      uploadProgress.value = 0
      uploadedCover.value = await uploadCover(coverFile.value, (progress) => {
        uploadProgress.value = progress
      })
    }

    currentStage.value = '正在发布视频'
    uploadProgress.value = 1
    const response = await publishVideo({
      title: title.value.trim(),
      description: description.value.trim(),
      ...uploadedVideo.value,
      ...uploadedCover.value,
    })
    toast.success('视频已发布，正在返回 Feed')
    await router.replace({ name: 'feed', query: { published: String(response.video.id) } })
  } catch (error) {
    errorMessage.value = error instanceof ApiError ? error.message : '发布失败，请检查网络后重试'
    toast.error(errorMessage.value)
  } finally {
    isSubmitting.value = false
    currentStage.value = ''
  }
}
</script>

<template>
  <main class="publish-page">
    <form class="publish-form" @submit.prevent="submit">
      <header class="publish-form__header">
        <div>
          <p>发布</p>
          <h1>上传视频</h1>
        </div>
        <RouterLink class="cancel-link" :to="{ name: 'feed' }">取消</RouterLink>
      </header>

      <p class="publish-hint" role="note">视频不超过 200 MiB，封面不超过 10 MiB；上传完成后才会创建公开视频。</p>

      <label class="form-field">
        <span>标题</span>
        <input v-model="title" maxlength="255" required>
      </label>

      <label class="form-field">
        <span>简介</span>
        <textarea v-model="description" maxlength="1000" rows="4"></textarea>
      </label>

      <div class="file-fields">
        <label class="file-field">
          <span>视频文件</span>
          <input accept=".mp4,.webm,.mov,video/mp4,video/webm,video/quicktime" type="file" @change="selectVideo">
          <small v-if="videoFile">{{ videoFile.name }} · {{ formatFileSize(videoFile.size) }}</small>
          <small v-else>MP4、WebM 或 MOV，最大 200 MiB</small>
        </label>

        <label class="file-field">
          <span>封面图片</span>
          <input accept=".jpg,.jpeg,.png,.webp,image/jpeg,image/png,image/webp" type="file" @change="selectCover">
          <small v-if="coverFile">{{ coverFile.name }} · {{ formatFileSize(coverFile.size) }}</small>
          <small v-else>JPG、PNG 或 WebP，最大 10 MiB</small>
        </label>
      </div>

      <div v-if="isSubmitting" class="upload-status" role="status">
        <div>
          <span>{{ currentStage }}</span>
          <strong>{{ progressLabel }}</strong>
        </div>
        <progress :value="uploadProgress" max="1"></progress>
      </div>

      <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>

      <button class="primary-action" type="submit" :disabled="isSubmitting">
        {{ isSubmitting ? '正在处理' : '上传并发布' }}
      </button>
    </form>
  </main>
</template>

<style scoped>
.publish-page {
  min-height: calc(100dvh - 64px);
  padding: 40px 16px 64px;
  background: #f3f5f4;
}

.publish-form {
  width: min(100%, 760px);
  margin: 0 auto;
  border: 1px solid #d7dcda;
  border-radius: 8px;
  padding: 32px;
  background: #ffffff;
}

.publish-form__header {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 30px;
}

.publish-form__header p,
.publish-form__header h1 {
  margin: 0;
}

.publish-form__header p {
  margin-bottom: 8px;
  color: var(--accent-strong);
  font-size: 0.9rem;
  font-weight: 750;
}

.publish-form__header h1 {
  color: var(--ink-strong);
  font-size: 1.55rem;
  line-height: 1.2;
}

.cancel-link {
  color: var(--ink-muted);
  font-size: 0.92rem;
}

.publish-hint {
  margin: -10px 0 24px;
  color: var(--ink-muted);
  font-size: 0.86rem;
  line-height: 1.5;
}

.form-field,
.file-field {
  display: grid;
  gap: 8px;
  color: var(--ink-strong);
  font-size: 0.9rem;
  font-weight: 650;
}

.form-field + .form-field {
  margin-top: 22px;
}

.form-field input,
.form-field textarea {
  width: 100%;
  border: 1px solid #b8c0bd;
  border-radius: 4px;
  padding: 10px;
  color: var(--ink-strong);
  background: #ffffff;
  resize: vertical;
}

.form-field input {
  min-height: 42px;
}

.file-fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  margin-top: 24px;
}

.file-field {
  min-height: 136px;
  border: 1px dashed #aab7b2;
  border-radius: 6px;
  padding: 16px;
  background: #f8faf9;
}

.file-field input {
  width: 100%;
  color: var(--ink-muted);
  font-size: 0.85rem;
  font-weight: 400;
}

.file-field small {
  color: var(--ink-muted);
  font-size: 0.78rem;
  font-weight: 400;
  line-height: 1.45;
  overflow-wrap: anywhere;
}

.upload-status {
  margin-top: 24px;
  color: var(--ink-muted);
  font-size: 0.9rem;
}

.upload-status > div {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 8px;
}

.upload-status strong {
  color: var(--ink-strong);
}

.upload-status progress {
  display: block;
  width: 100%;
  height: 8px;
  accent-color: var(--accent);
}

.form-error {
  margin: 20px 0 0;
  color: #ae2c20;
  font-size: 0.9rem;
  line-height: 1.45;
}

.primary-action {
  min-height: 44px;
  margin-top: 26px;
  border: 1px solid var(--accent-strong);
  border-radius: 4px;
  padding: 0 20px;
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

@media (max-width: 640px) {
  .publish-page {
    min-height: calc(100dvh - 56px);
    padding: 20px 12px 40px;
  }

  .publish-form {
    padding: 24px 20px;
  }

  .file-fields {
    grid-template-columns: 1fr;
  }
}
</style>
