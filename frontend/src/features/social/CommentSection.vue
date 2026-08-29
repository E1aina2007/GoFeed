<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { currentUser, isAuthenticated } from '@/features/auth/session'
import { ApiError } from '@/lib/api'
import { useConfirmStore } from '@/stores/confirm'
import { useToastStore } from '@/stores/toast'

import { createComment, getCommentList, removeComment, type CommentItem } from './api'
import { changeVideoCommentCount, getVideoEngagement, type VideoEngagement } from './engagement'

const props = defineProps<{
  videoId: number
  commentsCount: number
}>()

const route = useRoute()
const router = useRouter()
const toast = useToastStore()
const confirmStore = useConfirmStore()
const comments = ref<CommentItem[]>([])
const nextCursor = ref<string>()
const isLoading = ref(false)
const isLoadingMore = ref(false)
const isSubmitting = ref(false)
const deletingCommentID = ref<number>()
const listError = ref('')
const formError = ref('')
const content = ref('')
const engagement = ref<VideoEngagement>()
let listRequestID = 0

const hasMore = computed(() => Boolean(nextCursor.value))
const displayedCount = computed(() => engagement.value?.commentsCount ?? props.commentsCount)

function syncEngagement() {
  engagement.value = getVideoEngagement(props.videoId, 0, props.commentsCount)
}

function errorMessage(error: unknown, fallback: string) {
  return error instanceof ApiError ? error.message : fallback
}

function ownsComment(comment: CommentItem) {
  return currentUser.value?.id === comment.author.id
}

function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN')
}

async function loadComments(cursor?: string) {
  const requestID = ++listRequestID
  if (cursor) {
    isLoadingMore.value = true
  } else {
    isLoading.value = true
    listError.value = ''
  }

  try {
    const response = await getCommentList(props.videoId, { cursor })
    if (requestID !== listRequestID) {
      return
    }
    comments.value = cursor ? [...comments.value, ...response.items] : response.items
    nextCursor.value = response.next_cursor
  } catch (error) {
    if (requestID === listRequestID) {
      listError.value = errorMessage(error, '评论加载失败，请稍后重试')
    }
  } finally {
    if (requestID === listRequestID) {
      isLoading.value = false
      isLoadingMore.value = false
    }
  }
}

function retryComments() {
  void loadComments()
}

function loadMore() {
  if (!nextCursor.value || isLoadingMore.value) {
    return
  }
  void loadComments(nextCursor.value)
}

async function redirectToLogin() {
  toast.info('请先登录后再评论')
  await router.push({ name: 'login', query: { redirect: route.fullPath } })
}

async function submitComment() {
  if (!isAuthenticated.value) {
    await redirectToLogin()
    return
  }

  const normalized = content.value.trim()
  if (!normalized) {
    formError.value = '请输入评论内容'
    return
  }
  if (normalized.length > 1000) {
    formError.value = '评论不能超过 1000 个字符'
    return
  }

  isSubmitting.value = true
  formError.value = ''
  try {
    const response = await createComment(props.videoId, normalized)
    comments.value = [response.comment, ...comments.value]
    content.value = ''
    changeVideoCommentCount(props.videoId, 1)
    toast.success('评论已发布')
  } catch (error) {
    formError.value = errorMessage(error, '发表评论失败，请稍后重试')
    toast.error(formError.value)
  } finally {
    isSubmitting.value = false
  }
}

async function deleteComment(comment: CommentItem) {
  if (!ownsComment(comment) || deletingCommentID.value) {
    return
  }
  const confirmed = await confirmStore.confirm({
    title: '删除评论',
    message: '确定删除这条评论吗？',
    confirmText: '删除',
    danger: true,
  })
  if (!confirmed) {
    return
  }

  deletingCommentID.value = comment.id
  listError.value = ''
  try {
    await removeComment(props.videoId, comment.id)
    comments.value = comments.value.filter((item) => item.id !== comment.id)
    changeVideoCommentCount(props.videoId, -1)
    toast.info('评论已删除')
  } catch (error) {
    listError.value = errorMessage(error, '删除评论失败，请稍后重试')
    toast.error(listError.value)
  } finally {
    deletingCommentID.value = undefined
  }
}

watch(
  () => [props.videoId, props.commentsCount] as const,
  () => {
    syncEngagement()
  },
  { immediate: true },
)

watch(
  () => props.videoId,
  () => {
    comments.value = []
    nextCursor.value = undefined
    content.value = ''
    formError.value = ''
    void loadComments()
  },
  { immediate: true },
)
</script>

<template>
  <section class="comment-section" aria-labelledby="comment-heading">
    <header class="comment-section__header">
      <h2 id="comment-heading">
        评论 <span>{{ displayedCount }}</span>
      </h2>
    </header>

    <form v-if="isAuthenticated" class="comment-form" @submit.prevent="submitComment">
      <label class="sr-only" for="comment-content">发表评论</label>
      <textarea
        id="comment-content"
        v-model="content"
        maxlength="1000"
        rows="3"
        placeholder="写下你的看法"
        :disabled="isSubmitting"
      />
      <div class="comment-form__footer">
        <span class="comment-form__count">{{ content.length }}/1000</span>
        <button class="primary-action" type="submit" :disabled="isSubmitting || !content.trim()">
          {{ isSubmitting ? '正在发布' : '发表评论' }}
        </button>
      </div>
      <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
    </form>
    <RouterLink
      v-else
      class="comment-login"
      :to="{ name: 'login', query: { redirect: route.fullPath } }"
    >
      登录后发表评论
    </RouterLink>

    <div v-if="isLoading" class="comment-state" role="status">正在加载评论</div>
    <div
      v-else-if="listError && !comments.length"
      class="comment-state comment-state--error"
      role="alert"
    >
      <p>{{ listError }}</p>
      <button class="secondary-action" type="button" @click="retryComments">重试</button>
    </div>
    <div v-else-if="!comments.length" class="comment-state">暂时没有评论</div>
    <ol v-else class="comment-list">
      <li v-for="comment in comments" :key="comment.id" class="comment-item">
        <div class="comment-avatar" aria-hidden="true">
          <img v-if="comment.author.avatar_url" :src="comment.author.avatar_url" alt="" />
          <span v-else>{{ comment.author.username.slice(0, 1).toUpperCase() }}</span>
        </div>
        <div class="comment-copy">
          <div class="comment-meta">
            <RouterLink :to="{ name: 'user-profile', params: { id: comment.author.id } }"
              >@{{ comment.author.username }}</RouterLink
            >
            <time :datetime="comment.created_at">{{ formatDate(comment.created_at) }}</time>
          </div>
          <p>{{ comment.content }}</p>
          <button
            v-if="ownsComment(comment)"
            class="delete-comment"
            type="button"
            :disabled="deletingCommentID === comment.id"
            @click="deleteComment(comment)"
          >
            {{ deletingCommentID === comment.id ? '正在删除' : '删除' }}
          </button>
        </div>
      </li>
    </ol>

    <p v-if="listError && comments.length" class="inline-error" role="alert">{{ listError }}</p>
    <button
      v-if="hasMore"
      class="secondary-action load-more"
      type="button"
      :disabled="isLoadingMore"
      @click="loadMore"
    >
      {{ isLoadingMore ? '正在加载' : '加载更多评论' }}
    </button>
  </section>
</template>

<style scoped>
.comment-section {
  border-top: 1px solid var(--content-border);
  padding: 24px;
}

.comment-section__header h2 {
  margin: 0;
  color: var(--content-ink);
  font-size: 1.12rem;
}

.comment-section__header span,
.comment-form__count,
.comment-meta time {
  color: var(--content-subtle);
  font-variant-numeric: tabular-nums;
}

.comment-form {
  margin-top: 16px;
}

.comment-form textarea {
  display: block;
  width: 100%;
  resize: vertical;
  border: 1px solid var(--content-border-strong);
  border-radius: 6px;
  padding: 10px;
  color: var(--content-ink);
  background: #121918;
  line-height: 1.5;
}

.comment-form textarea:focus {
  border-color: var(--content-accent-strong);
  outline: 0;
}

.comment-form__footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 8px;
}

.comment-form__count {
  font-size: 0.78rem;
}

.primary-action,
.secondary-action,
.comment-login {
  min-height: 36px;
  border: 1px solid var(--content-border-strong);
  border-radius: 6px;
  padding: 8px 13px;
  font-size: 0.86rem;
  font-weight: 700;
}

.primary-action {
  border-color: var(--content-accent-strong);
  color: #052720;
  background: var(--content-accent);
  cursor: pointer;
}

.primary-action:disabled,
.secondary-action:disabled {
  cursor: wait;
  opacity: 0.6;
}

.secondary-action {
  color: var(--content-accent);
  background: var(--content-surface-raised);
  cursor: pointer;
}

.comment-login {
  display: inline-flex;
  align-items: center;
  margin-top: 16px;
  color: var(--content-accent);
  text-decoration: none;
  background: var(--content-surface-raised);
}

.comment-state {
  padding: 30px 8px;
  color: var(--content-muted);
  text-align: center;
}

.comment-state--error,
.form-error,
.inline-error {
  color: var(--content-danger);
}

.comment-state p {
  margin-top: 0;
}

.comment-list {
  display: grid;
  gap: 0;
  margin: 16px 0 0;
  padding: 0;
  list-style: none;
}

.comment-item {
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr);
  gap: 10px;
  border-top: 1px solid var(--content-border);
  padding: 14px 0;
}

.comment-item:first-child {
  border-top: 0;
}

.comment-avatar {
  display: grid;
  width: 34px;
  height: 34px;
  place-items: center;
  overflow: hidden;
  border-radius: 50%;
  color: #ffffff;
  background: var(--content-accent-strong);
  font-size: 0.8rem;
  font-weight: 700;
}

.comment-avatar img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.comment-copy {
  min-width: 0;
}

.comment-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: baseline;
}

.comment-meta a {
  color: var(--content-ink);
  font-size: 0.88rem;
  font-weight: 700;
  text-decoration: none;
}

.comment-meta a:hover {
  color: var(--content-accent);
}

.comment-meta time {
  font-size: 0.75rem;
}

.comment-copy p {
  margin: 7px 0 0;
  color: var(--content-muted);
  line-height: 1.55;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.delete-comment {
  margin-top: 8px;
  border: 0;
  padding: 0;
  color: var(--content-danger);
  background: transparent;
  font-size: 0.8rem;
  font-weight: 700;
  cursor: pointer;
}

.delete-comment:disabled {
  cursor: wait;
  opacity: 0.65;
}

.load-more {
  display: block;
  margin: 16px auto 0;
}

@media (max-width: 640px) {
  .comment-section {
    padding: 18px 16px;
  }
}
</style>
