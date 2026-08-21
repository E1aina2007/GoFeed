<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import { getPublishedVideo, type VideoItem } from '@/features/video/api'
import { ApiError } from '@/lib/api'

const route = useRoute()
const video = ref<VideoItem>()
const isLoading = ref(true)
const errorMessage = ref('')

function videoID() {
  const id = Number(route.params.id)
  return Number.isSafeInteger(id) && id > 0 ? id : undefined
}

async function load() {
  const id = videoID()
  if (!id) {
    errorMessage.value = '视频地址无效'
    isLoading.value = false
    return
  }

  isLoading.value = true
  errorMessage.value = ''
  try {
    video.value = (await getPublishedVideo(id)).video
  } catch (error) {
    errorMessage.value = error instanceof ApiError ? error.message : '视频加载失败，请稍后重试'
  } finally {
    isLoading.value = false
  }
}

onMounted(load)
</script>

<template>
  <main class="content-page detail-page">
    <div class="page-toolbar">
      <RouterLink class="back-link" :to="{ name: 'feed' }">返回 Feed</RouterLink>
    </div>
    <section v-if="isLoading" class="state-message" role="status">正在加载视频</section>
    <section v-else-if="errorMessage" class="state-message" role="alert">
      <p>{{ errorMessage }}</p>
      <button class="secondary-action" type="button" @click="load">重试</button>
    </section>
    <article v-else-if="video" class="detail-content">
      <video class="detail-player" :src="video.play_url" :poster="video.cover_url" controls playsinline preload="metadata">
        抱歉，你的浏览器不支持视频播放。
      </video>
      <div class="detail-copy">
        <h1>{{ video.title }}</h1>
        <RouterLink class="author-link" :to="{ name: 'user-profile', params: { id: video.author.id } }">
          @{{ video.author.username }}
        </RouterLink>
        <p v-if="video.description">{{ video.description }}</p>
        <small>{{ video.published_at }} · {{ video.likes_count }} 个赞 · {{ video.comments_count }} 条评论</small>
      </div>
    </article>
  </main>
</template>

<style scoped>
.content-page {
  min-height: calc(100dvh - 64px);
  padding: 32px 16px 64px;
  background: #f3f5f4;
}

.detail-page {
  width: min(100%, 980px);
  margin: 0 auto;
}

.page-toolbar {
  margin-bottom: 18px;
}

.back-link,
.author-link {
  color: var(--accent-strong);
  font-weight: 700;
}

.detail-content {
  overflow: hidden;
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  background: var(--surface);
}

.detail-player {
  display: block;
  width: 100%;
  max-height: 70dvh;
  background: #000000;
}

.detail-copy {
  padding: 24px;
}

.detail-copy h1 {
  margin: 0 0 10px;
  color: var(--ink-strong);
  font-size: 1.5rem;
}

.detail-copy p {
  color: var(--ink-muted);
  line-height: 1.6;
}

.detail-copy small {
  color: var(--ink-subtle);
}

.state-message {
  padding: 64px 20px;
  color: var(--ink-muted);
  text-align: center;
}

.secondary-action {
  border: 1px solid var(--accent-strong);
  border-radius: 4px;
  padding: 8px 14px;
  color: var(--accent-strong);
  background: var(--surface);
  cursor: pointer;
}

@media (max-width: 640px) {
  .content-page {
    min-height: calc(100dvh - 56px);
    padding: 20px 12px 40px;
  }
}
</style>
