<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import CommentSection from '@/features/social/CommentSection.vue'
import LikeButton from '@/features/social/LikeButton.vue'
import { getVideoEngagement, type VideoEngagement } from '@/features/social/engagement'
import { getPublishedVideo, type VideoItem } from '@/features/video/api'
import { ApiError } from '@/lib/api'

const route = useRoute()
const video = ref<VideoItem>()
const engagement = ref<VideoEngagement>()
const isLoading = ref(true)
const errorMessage = ref('')
const playerAspectRatio = ref<number>()
const commentsCount = computed(
  () => engagement.value?.commentsCount ?? video.value?.comments_count ?? 0,
)

// 元数据加载后把播放器框收敛到视频真实比例，避免竖屏视频两侧大面积留黑
const playerStyle = computed(() => {
  const ratio = playerAspectRatio.value
  if (!ratio) {
    return undefined
  }
  return {
    aspectRatio: String(ratio),
    width: `min(100%, calc(70dvh * ${ratio}))`,
  }
})

function handlePlayerMetadata(event: Event) {
  const player = event.currentTarget
  if (player instanceof HTMLVideoElement && player.videoWidth > 0 && player.videoHeight > 0) {
    playerAspectRatio.value = player.videoWidth / player.videoHeight
  }
}

function videoID() {
  const id = Number(route.params.id)
  return Number.isSafeInteger(id) && id > 0 ? id : undefined
}

async function load() {
  playerAspectRatio.value = undefined
  const id = videoID()
  if (!id) {
    video.value = undefined
    engagement.value = undefined
    errorMessage.value = '视频地址无效'
    isLoading.value = false
    return
  }

  isLoading.value = true
  errorMessage.value = ''
  try {
    video.value = (await getPublishedVideo(id)).video
    engagement.value = getVideoEngagement(
      video.value.id,
      video.value.likes_count,
      video.value.comments_count,
    )
  } catch (error) {
    video.value = undefined
    engagement.value = undefined
    errorMessage.value = error instanceof ApiError ? error.message : '视频加载失败，请稍后重试'
  } finally {
    isLoading.value = false
  }
}

watch(
  () => route.params.id,
  () => {
    void load()
  },
  { immediate: true },
)
</script>

<template>
  <main class="content-page content-page--dark detail-page">
    <div class="page-toolbar">
      <RouterLink class="back-link" :to="{ name: 'feed' }">返回 Feed</RouterLink>
    </div>
    <section v-if="isLoading" class="state-message" role="status">正在加载视频</section>
    <section v-else-if="errorMessage" class="state-message" role="alert">
      <p>{{ errorMessage }}</p>
      <button class="secondary-action" type="button" @click="load">重试</button>
    </section>
    <article v-else-if="video" class="detail-content">
      <video
        class="detail-player"
        :src="video.play_url"
        :poster="video.cover_url"
        :style="playerStyle"
        controls
        playsinline
        preload="metadata"
        @loadedmetadata="handlePlayerMetadata"
      >
        抱歉，你的浏览器不支持视频播放。
      </video>
      <div class="detail-copy">
        <h1>{{ video.title }}</h1>
        <RouterLink
          class="author-link"
          :to="{ name: 'user-profile', params: { id: video.author.id } }"
        >
          @{{ video.author.username }}
        </RouterLink>
        <p v-if="video.description">{{ video.description }}</p>
        <div class="detail-actions" aria-label="视频互动">
          <LikeButton
            :video-id="video.id"
            :likes-count="video.likes_count"
            :comments-count="video.comments_count"
          />
          <span class="detail-actions__comments">{{ commentsCount }} 条评论</span>
        </div>
        <small class="detail-meta">{{ video.published_at }}</small>
      </div>
      <CommentSection :video-id="video.id" :comments-count="video.comments_count" />
    </article>
  </main>
</template>

<style scoped>
.content-page {
  min-height: calc(100dvh - 64px);
  padding: 32px 16px 64px;
  background: var(--content-bg);
}

.detail-page {
  width: 100%;
}

.page-toolbar,
.detail-content {
  width: min(100%, 980px);
  margin-right: auto;
  margin-left: auto;
}

.page-toolbar {
  margin-bottom: 18px;
}

.back-link,
.author-link {
  color: var(--content-accent);
  font-weight: 700;
}

.back-link {
  text-decoration: none;
}

.back-link:hover,
.author-link:hover {
  color: var(--content-ink);
}

.detail-content {
  overflow: hidden;
  border: 1px solid var(--content-border);
  border-radius: 8px;
  background: var(--content-surface);
}

.detail-player {
  display: block;
  width: 100%;
  max-height: 70dvh;
  margin-inline: auto;
  object-fit: contain;
  background: #080b0c;
}

.detail-copy {
  padding: 24px;
}

.detail-copy h1 {
  margin: 0 0 10px;
  color: var(--content-ink);
  font-size: 1.5rem;
}

.detail-copy p {
  color: var(--content-muted);
  line-height: 1.6;
}

.detail-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  margin-top: 18px;
}

.detail-actions__comments,
.detail-meta {
  color: var(--content-subtle);
  font-size: 0.86rem;
}

.detail-meta {
  display: block;
  margin-top: 14px;
}

.state-message {
  padding: 64px 20px;
  color: var(--content-muted);
  text-align: center;
}

.secondary-action {
  min-height: 36px;
  border: 1px solid var(--content-border-strong);
  border-radius: 6px;
  padding: 8px 14px;
  color: var(--content-accent);
  background: var(--content-surface);
  cursor: pointer;
}

.secondary-action:hover {
  border-color: var(--content-accent-strong);
  background: var(--content-surface-raised);
}

@media (max-width: 640px) {
  .content-page {
    min-height: calc(100dvh - 56px);
    padding: 20px 12px 40px;
  }
}
</style>
