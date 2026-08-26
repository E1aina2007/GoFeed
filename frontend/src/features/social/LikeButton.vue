<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { isAuthenticated } from '@/features/auth/session'
import { ApiError } from '@/lib/api'
import { useToastStore } from '@/stores/toast'

import { createLike, getLikeState, removeLike } from './api'
import { getVideoEngagement, updateVideoLikeState, type VideoEngagement } from './engagement'

const props = withDefaults(
  defineProps<{
    videoId: number
    likesCount: number
    commentsCount?: number
    variant?: 'surface' | 'overlay'
  }>(),
  {
    commentsCount: 0,
    variant: 'surface',
  },
)

const route = useRoute()
const router = useRouter()
const toast = useToastStore()
const engagement = ref<VideoEngagement>()
const isPending = ref(false)
let stateRequestID = 0

const liked = computed(() => engagement.value?.liked ?? false)
const displayedCount = computed(() => engagement.value?.likesCount ?? props.likesCount)

function syncEngagement() {
  engagement.value = getVideoEngagement(props.videoId, props.likesCount, props.commentsCount)
}

async function loadState() {
  const requestID = ++stateRequestID
  const entry = engagement.value
  if (!entry || !isAuthenticated.value) {
    if (entry) {
      entry.liked = false
    }
    return
  }

  try {
    const state = await getLikeState(props.videoId)
    if (requestID === stateRequestID) {
      updateVideoLikeState(props.videoId, state)
    }
  } catch {
    if (requestID === stateRequestID) {
      entry.liked = false
    }
  }
}

async function redirectToLogin() {
  toast.info('请先登录后再点赞')
  await router.push({ name: 'login', query: { redirect: route.fullPath } })
}

async function toggleLike() {
  if (!isAuthenticated.value) {
    await redirectToLogin()
    return
  }
  if (isPending.value || !engagement.value) {
    return
  }

  isPending.value = true
  ++stateRequestID
  try {
    const state = liked.value ? await removeLike(props.videoId) : await createLike(props.videoId)
    updateVideoLikeState(props.videoId, state)
  } catch (error) {
    const message = error instanceof ApiError ? error.message : '点赞操作失败，请稍后重试'
    toast.error(message)
  } finally {
    isPending.value = false
  }
}

watch(
  () => [props.videoId, props.likesCount, props.commentsCount] as const,
  () => {
    syncEngagement()
    void loadState()
  },
  { immediate: true },
)

watch(isAuthenticated, () => {
  void loadState()
})
</script>

<template>
  <button
    class="like-button"
    :class="`like-button--${variant}`"
    type="button"
    :aria-pressed="liked"
    :aria-label="
      liked ? `取消点赞，当前 ${displayedCount} 个赞` : `点赞，当前 ${displayedCount} 个赞`
    "
    :title="liked ? '取消点赞' : '点赞'"
    :disabled="isPending"
    @click="toggleLike"
  >
    <span class="like-button__label">{{ liked ? '已赞' : '点赞' }}</span>
    <strong class="like-button__count">{{ displayedCount }}</strong>
  </button>
</template>

<style scoped>
.like-button {
  display: inline-flex;
  min-width: 76px;
  min-height: 38px;
  align-items: center;
  justify-content: center;
  gap: 7px;
  border: 1px solid var(--content-border-strong);
  border-radius: 6px;
  padding: 7px 10px;
  color: var(--content-ink);
  background: var(--content-surface-raised);
  font-size: 0.86rem;
  cursor: pointer;
}

.like-button:hover:not(:disabled),
.like-button[aria-pressed='true'] {
  border-color: var(--content-accent-strong);
  color: var(--content-accent);
  background: #1a3933;
}

.like-button:disabled {
  cursor: wait;
  opacity: 0.65;
}

.like-button__count {
  min-width: 1.3em;
  color: inherit;
  font-variant-numeric: tabular-nums;
}

.like-button--overlay {
  border-color: #ffffff66;
  color: #ffffff;
  background: #0b1110a8;
  box-shadow: 0 4px 16px #00000033;
}

.like-button--overlay:hover:not(:disabled),
.like-button--overlay[aria-pressed='true'] {
  border-color: #72d5c4;
  color: #c7fff1;
  background: #144b41e8;
}
</style>
