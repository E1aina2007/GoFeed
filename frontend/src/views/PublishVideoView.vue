<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'

import {
  createDraft,
  discardDraft,
  getDraft,
  publishDraft,
  uploadCover,
  uploadVideo,
  type DraftItem,
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
const activeDraft = ref<DraftItem>()
const boundVideoFile = ref<File>()
const boundCoverFile = ref<File>()
const currentStage = ref('')
const uploadProgress = ref(0)
const errorMessage = ref('')
const isSubmitting = ref(false)
const isDiscarding = ref(false)
const activeUploadController = ref<AbortController>()
const isCancellingUpload = ref(false)
const cancellationRequested = ref(false)

const progressLabel = computed(() => `${Math.round(uploadProgress.value * 100)}%`)
const isBusy = computed(() => isSubmitting.value || isDiscarding.value)
const canCancelUpload = computed(
  () =>
    isSubmitting.value &&
    activeUploadController.value !== undefined &&
    !isCancellingUpload.value &&
    (currentStage.value === '正在上传视频' || currentStage.value === '正在上传封面'),
)
const videoDisplayName = computed(
  () => activeDraft.value?.play_original_name || videoFile.value?.name,
)
const coverDisplayName = computed(
  () => activeDraft.value?.cover_original_name || coverFile.value?.name,
)
const activeDraftNeedsDiscard = computed(() => {
  const draft = activeDraft.value
  if (!draft) {
    return false
  }
  return (
    draft.status !== 'draft' ||
    draft.title !== title.value.trim() ||
    draft.description !== description.value.trim() ||
    (draft.has_video && boundVideoFile.value !== videoFile.value) ||
    (draft.has_cover && boundCoverFile.value !== coverFile.value)
  )
})

function clearActiveDraft() {
  activeDraft.value = undefined
  boundVideoFile.value = undefined
  boundCoverFile.value = undefined
}

function setActiveDraft(draft: DraftItem) {
  const previousID = activeDraft.value?.id
  activeDraft.value = draft
  if (!draft.has_video) {
    boundVideoFile.value = undefined
  } else if (previousID !== draft.id || !boundVideoFile.value) {
    boundVideoFile.value = videoFile.value
  }
  if (!draft.has_cover) {
    boundCoverFile.value = undefined
  } else if (previousID !== draft.id || !boundCoverFile.value) {
    boundCoverFile.value = coverFile.value
  }
}

function markDraftMediaBound(kind: 'video' | 'cover', originalName: string, file: File) {
  const draft = activeDraft.value
  if (!draft) {
    throw new ApiError(500, '草稿状态丢失，请重试')
  }
  if (kind === 'video') {
    activeDraft.value = {
      ...draft,
      has_video: true,
      play_original_name: originalName,
    }
    boundVideoFile.value = file
    return
  }
  activeDraft.value = {
    ...draft,
    has_cover: true,
    cover_original_name: originalName,
  }
  boundCoverFile.value = file
}

function selectVideo(event: Event) {
  const input = event.target as HTMLInputElement
  videoFile.value = input.files?.[0]
  errorMessage.value = ''
}

function selectCover(event: Event) {
  const input = event.target as HTMLInputElement
  coverFile.value = input.files?.[0]
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
  if (
    !videoExtension.test(videoFile.value.name) ||
    videoFile.value.size === 0 ||
    videoFile.value.size > maxVideoSize
  ) {
    return '视频仅支持不超过 200 MiB 的 MP4、WebM 或 MOV 文件'
  }
  if (!coverFile.value) {
    return '请选择一张封面图片'
  }
  if (
    !coverExtension.test(coverFile.value.name) ||
    coverFile.value.size === 0 ||
    coverFile.value.size > maxCoverSize
  ) {
    return '封面仅支持不超过 10 MiB 的 JPG、PNG 或 WebP 文件'
  }
  if (activeDraft.value?.status === 'purging') {
    return '草稿已进入清扫，无法继续上传'
  }
  if (activeDraftNeedsDiscard.value) {
    return '当前草稿内容已变更，请先放弃当前草稿'
  }
  return ''
}

function canReconcileMediaUpload(error: unknown) {
  return error instanceof ApiError && (error.status === 0 || error.status === 409)
}

function isAbortError(error: unknown) {
  return (
    typeof error === 'object' && error !== null && 'name' in error && error.name === 'AbortError'
  )
}

async function reconcileMediaUpload(
  draftID: number,
  kind: 'video' | 'cover',
  originalError: unknown,
) {
  if (!canReconcileMediaUpload(originalError)) {
    throw originalError
  }

  currentStage.value = '正在确认草稿状态'
  let draft: DraftItem
  try {
    draft = (await getDraft(draftID)).draft
  } catch {
    throw originalError
  }
  setActiveDraft(draft)
  if (draft.status !== 'draft') {
    throw new ApiError(409, '草稿已进入清扫，无法继续上传')
  }

  const mediaBound = kind === 'video' ? draft.has_video : draft.has_cover
  if (!mediaBound) {
    throw originalError
  }
  toast.info(kind === 'video' ? '已确认视频上传成功，继续处理' : '已确认封面上传成功，继续处理')
}

async function reconcileCancelledMediaUpload(draftID: number, kind: 'video' | 'cover') {
  currentStage.value = '正在确认取消结果'
  const draft = (await getDraft(draftID)).draft
  setActiveDraft(draft)
  if (draft.status === 'purging') {
    throw new ApiError(409, '草稿已进入清扫，无法继续上传')
  }

  const mediaBound = kind === 'video' ? draft.has_video : draft.has_cover
  toast.info(
    mediaBound
      ? kind === 'video'
        ? '上传已取消，已保留服务端视频'
        : '上传已取消，已保留服务端封面'
      : '上传已取消，可稍后重试',
  )
}

async function uploadMissingVideo(draftID: number, file: File) {
  currentStage.value = '正在上传视频'
  uploadProgress.value = 0
  const controller = new AbortController()
  activeUploadController.value = controller
  try {
    let uploaded: Awaited<ReturnType<typeof uploadVideo>>
    try {
      uploaded = await uploadVideo(
        draftID,
        file,
        (progress) => {
          uploadProgress.value = progress
        },
        controller.signal,
      )
    } catch (error) {
      if (controller.signal.aborted || cancellationRequested.value || isAbortError(error)) {
        await reconcileCancelledMediaUpload(draftID, 'video')
        return false
      }
      await reconcileMediaUpload(draftID, 'video', error)
      return true
    }
    if (controller.signal.aborted || cancellationRequested.value) {
      await reconcileCancelledMediaUpload(draftID, 'video')
      return false
    }
    markDraftMediaBound('video', uploaded.play_original_name, file)
    return true
  } finally {
    if (activeUploadController.value === controller) {
      activeUploadController.value = undefined
    }
  }
}

async function uploadMissingCover(draftID: number, file: File) {
  currentStage.value = '正在上传封面'
  uploadProgress.value = 0
  const controller = new AbortController()
  activeUploadController.value = controller
  try {
    let uploaded: Awaited<ReturnType<typeof uploadCover>>
    try {
      uploaded = await uploadCover(
        draftID,
        file,
        (progress) => {
          uploadProgress.value = progress
        },
        controller.signal,
      )
    } catch (error) {
      if (controller.signal.aborted || cancellationRequested.value || isAbortError(error)) {
        await reconcileCancelledMediaUpload(draftID, 'cover')
        return false
      }
      await reconcileMediaUpload(draftID, 'cover', error)
      return true
    }
    if (controller.signal.aborted || cancellationRequested.value) {
      await reconcileCancelledMediaUpload(draftID, 'cover')
      return false
    }
    markDraftMediaBound('cover', uploaded.cover_original_name, file)
    return true
  } finally {
    if (activeUploadController.value === controller) {
      activeUploadController.value = undefined
    }
  }
}

function cancelUpload() {
  const controller = activeUploadController.value
  if (!controller || !canCancelUpload.value) {
    return
  }
  cancellationRequested.value = true
  isCancellingUpload.value = true
  errorMessage.value = ''
  currentStage.value = '正在取消上传'
  controller.abort()
}

async function discardCurrentDraft() {
  const draft = activeDraft.value
  if (!draft) {
    return true
  }
  if (!window.confirm('放弃草稿后将无法继续上传或发布，确定继续吗？')) {
    return false
  }

  isDiscarding.value = true
  errorMessage.value = ''
  try {
    await discardDraft(draft.id)
    clearActiveDraft()
    toast.info('草稿已放弃，媒体将在后台清理')
    return true
  } catch (error) {
    errorMessage.value = error instanceof ApiError ? error.message : '放弃草稿失败，请稍后重试'
    toast.error(errorMessage.value)
    return false
  } finally {
    isDiscarding.value = false
  }
}

async function cancelPublishing() {
  if (isBusy.value) {
    return
  }
  if (!(await discardCurrentDraft())) {
    return
  }
  await router.replace({ name: 'feed' })
}

async function submit() {
  errorMessage.value = validationError()
  if (errorMessage.value || !videoFile.value || !coverFile.value) {
    return
  }

  isSubmitting.value = true
  cancellationRequested.value = false
  isCancellingUpload.value = false
  try {
    const normalizedTitle = title.value.trim()
    const normalizedDescription = description.value.trim()
    if (!activeDraft.value) {
      currentStage.value = '正在创建草稿'
      const response = await createDraft({
        title: normalizedTitle,
        description: normalizedDescription,
      })
      setActiveDraft(response.draft)
    }
    const currentDraftID = activeDraft.value?.id
    if (!currentDraftID) {
      throw new ApiError(500, '草稿创建失败，请重试')
    }

    if (!activeDraft.value?.has_video) {
      if (!(await uploadMissingVideo(currentDraftID, videoFile.value))) {
        return
      }
    }

    if (!activeDraft.value?.has_cover) {
      if (!(await uploadMissingCover(currentDraftID, coverFile.value))) {
        return
      }
    }

    currentStage.value = '正在发布视频'
    uploadProgress.value = 1
    const response = await publishDraft(currentDraftID)
    clearActiveDraft()
    toast.success('视频已发布，正在返回 Feed')
    await router.replace({ name: 'feed', query: { published: String(response.video.id) } })
  } catch (error) {
    errorMessage.value = error instanceof ApiError ? error.message : '发布失败，请检查网络后重试'
    toast.error(errorMessage.value)
  } finally {
    isSubmitting.value = false
    isCancellingUpload.value = false
    activeUploadController.value = undefined
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
        <button class="cancel-link" type="button" :disabled="isBusy" @click="cancelPublishing">
          取消
        </button>
      </header>

      <p class="publish-hint" role="note">
        视频不超过 200 MiB，封面不超过 10 MiB；上传完成后才会创建公开视频。
      </p>

      <label class="form-field">
        <span>标题</span>
        <input v-model="title" maxlength="255" required :disabled="isBusy" />
      </label>

      <label class="form-field">
        <span>简介</span>
        <textarea v-model="description" maxlength="1000" rows="4" :disabled="isBusy"></textarea>
      </label>

      <div class="file-fields">
        <label class="file-field">
          <span>视频文件</span>
          <input
            accept=".mp4,.webm,.mov,video/mp4,video/webm,video/quicktime"
            type="file"
            :disabled="isBusy"
            @change="selectVideo"
          />
          <small v-if="videoFile"
            >{{ videoDisplayName }} · {{ formatFileSize(videoFile.size) }}</small
          >
          <small v-else>MP4、WebM 或 MOV，最大 200 MiB</small>
        </label>

        <label class="file-field">
          <span>封面图片</span>
          <input
            accept=".jpg,.jpeg,.png,.webp,image/jpeg,image/png,image/webp"
            type="file"
            :disabled="isBusy"
            @change="selectCover"
          />
          <small v-if="coverFile"
            >{{ coverDisplayName }} · {{ formatFileSize(coverFile.size) }}</small
          >
          <small v-else>JPG、PNG 或 WebP，最大 10 MiB</small>
        </label>
      </div>

      <div v-if="isSubmitting" class="upload-status" role="status">
        <div class="upload-status__row">
          <div class="upload-status__summary">
            <span>{{ currentStage }}</span>
            <strong>{{ progressLabel }}</strong>
          </div>
          <button
            v-if="canCancelUpload || isCancellingUpload"
            class="cancel-upload-action"
            type="button"
            :disabled="!canCancelUpload"
            @click="cancelUpload"
          >
            {{ isCancellingUpload ? '正在取消上传' : '取消上传' }}
          </button>
        </div>
        <progress :value="uploadProgress" max="1"></progress>
      </div>

      <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>

      <div v-if="activeDraft" class="draft-actions">
        <button
          class="discard-action"
          type="button"
          :disabled="isBusy"
          @click="discardCurrentDraft"
        >
          {{ isDiscarding ? '正在放弃草稿' : '放弃当前草稿' }}
        </button>
      </div>

      <button class="primary-action" type="submit" :disabled="isBusy">
        {{ isSubmitting ? '正在处理' : isDiscarding ? '正在放弃草稿' : '上传并发布' }}
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
  border: 0;
  padding: 0;
  color: var(--ink-muted);
  background: transparent;
  font-size: 0.92rem;
  cursor: pointer;
}

.cancel-link:disabled {
  cursor: wait;
  opacity: 0.62;
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

.upload-status__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 8px;
}

.upload-status__summary {
  display: flex;
  min-width: 0;
  flex: 1;
  justify-content: space-between;
  gap: 16px;
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

.cancel-upload-action {
  min-height: 32px;
  flex: 0 0 auto;
  border: 1px solid #b8c0bd;
  border-radius: 4px;
  padding: 0 10px;
  color: var(--ink-strong);
  background: #ffffff;
  font-size: 0.82rem;
  font-weight: 650;
  cursor: pointer;
}

.cancel-upload-action:hover:not(:disabled) {
  border-color: var(--accent-strong);
  background: #f1f8f5;
}

.cancel-upload-action:disabled {
  cursor: wait;
  opacity: 0.62;
}

.form-error {
  margin: 20px 0 0;
  color: #ae2c20;
  font-size: 0.9rem;
  line-height: 1.45;
}

.draft-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 20px;
}

.discard-action {
  min-height: 36px;
  border: 1px solid #c56359;
  border-radius: 4px;
  padding: 0 14px;
  color: #9a2b21;
  background: #ffffff;
  font-size: 0.86rem;
  font-weight: 650;
  cursor: pointer;
}

.discard-action:hover:not(:disabled) {
  border-color: #9a2b21;
  background: #fff4f2;
}

.discard-action:disabled {
  opacity: 0.62;
  cursor: wait;
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

  .upload-status__row {
    align-items: stretch;
    flex-direction: column;
  }

  .cancel-upload-action {
    align-self: flex-start;
  }
}
</style>
