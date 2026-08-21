<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import VideoListItem from '@/components/VideoListItem.vue'
import { deleteVideo, listMyVideos, type VideoItem } from '@/features/video/api'
import { ApiError } from '@/lib/api'
import { useToastStore } from '@/stores/toast'

const videos = ref<VideoItem[]>([])
const toast = useToastStore()
const nextCursor = ref<string>()
const isLoading = ref(true)
const isLoadingMore = ref(false)
const errorMessage = ref('')
const actionMessage = ref('')
const hasMore = computed(() => Boolean(nextCursor.value))

function apiMessage(error: unknown, fallback: string) {
  return error instanceof ApiError ? error.message : fallback
}

async function loadFirstPage() {
  isLoading.value = true
  errorMessage.value = ''
  try {
    const response = await listMyVideos()
    videos.value = response.items
    nextCursor.value = response.next_cursor
  } catch (error) {
    errorMessage.value = apiMessage(error, '我的视频加载失败，请稍后重试')
    toast.error(errorMessage.value)
  } finally {
    isLoading.value = false
  }
}

async function loadMore() {
  if (!nextCursor.value || isLoadingMore.value) {
    return
  }
  isLoadingMore.value = true
  errorMessage.value = ''
  try {
    const response = await listMyVideos({ cursor: nextCursor.value })
    videos.value.push(...response.items)
    nextCursor.value = response.next_cursor
  } catch (error) {
    errorMessage.value = apiMessage(error, '更多视频加载失败，请重试')
    toast.error(errorMessage.value)
  } finally {
    isLoadingMore.value = false
  }
}

async function removeVideo(video: VideoItem) {
  if (!window.confirm(`确定删除“${video.title}”吗？`)) {
    return
  }
  actionMessage.value = ''
  try {
    await deleteVideo(video.id)
    videos.value = videos.value.filter((item) => item.id !== video.id)
    actionMessage.value = '视频已删除'
    toast.success('视频已删除')
  } catch (error) {
    errorMessage.value = apiMessage(error, '删除失败，请稍后重试')
    toast.error(errorMessage.value)
  }
}

onMounted(loadFirstPage)
</script>

<template>
  <main class="content-page">
    <header class="page-header">
      <div>
        <p class="eyebrow">创作管理</p>
        <h1>我的视频</h1>
      </div>
      <RouterLink class="primary-link" :to="{ name: 'publish' }">发布视频</RouterLink>
    </header>
    <p v-if="actionMessage" class="success-message" role="status">{{ actionMessage }}</p>
    <section v-if="isLoading" class="state-message" role="status">正在加载我的视频</section>
    <section v-else-if="errorMessage && !videos.length" class="state-message" role="alert">
      <p>{{ errorMessage }}</p>
      <button class="secondary-action" type="button" @click="loadFirstPage">重试</button>
    </section>
    <section v-else-if="videos.length" class="video-list">
      <VideoListItem v-for="video in videos" :key="video.id" :video="video" deletable @delete="removeVideo" />
      <p v-if="errorMessage" class="inline-error" role="alert">{{ errorMessage }}</p>
      <button v-if="hasMore" class="secondary-action load-more" type="button" :disabled="isLoadingMore" @click="loadMore">
        {{ isLoadingMore ? '正在加载' : '加载更多' }}
      </button>
      <p v-else class="end-message">已经到底了</p>
    </section>
    <section v-else class="state-message">还没有发布视频</section>
  </main>
</template>

<style scoped>
.content-page {
  min-height: calc(100dvh - 64px);
  padding: 36px 16px 64px;
  background: #f3f5f4;
}

.page-header,
.video-list {
  width: min(100%, 900px);
  margin: 0 auto;
}

.page-header {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: 18px;
  margin-bottom: 24px;
}

.eyebrow {
  margin: 0 0 6px;
  color: var(--accent-strong);
  font-size: 0.86rem;
  font-weight: 750;
}

h1 {
  margin: 0;
  color: var(--ink-strong);
  font-size: 1.7rem;
}

.primary-link {
  border-radius: 4px;
  padding: 10px 14px;
  color: #ffffff;
  background: var(--accent);
  font-weight: 700;
  text-decoration: none;
}

.state-message,
.end-message {
  width: min(100%, 900px);
  margin: 0 auto;
  padding: 52px 20px;
  color: var(--ink-muted);
  text-align: center;
}

.state-message p {
  margin-top: 0;
}

.secondary-action {
  border: 1px solid var(--accent-strong);
  border-radius: 4px;
  padding: 8px 14px;
  color: var(--accent-strong);
  background: var(--surface);
  cursor: pointer;
}

.secondary-action:disabled {
  cursor: wait;
  opacity: 0.6;
}

.load-more {
  display: block;
  margin: 22px auto 0;
}

.success-message {
  width: min(100%, 900px);
  margin: 0 auto 12px;
  color: var(--accent-strong);
}

.inline-error {
  color: #ae2c20;
}

@media (max-width: 640px) {
  .content-page {
    min-height: calc(100dvh - 56px);
    padding: 24px 12px 40px;
  }

  .page-header {
    align-items: start;
  }

  h1 {
    font-size: 1.45rem;
  }
}
</style>
