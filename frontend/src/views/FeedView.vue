<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'

import { listPublishedVideos, type VideoItem } from '@/features/video/api'
import { ApiError } from '@/lib/api'

const feedElement = ref<HTMLElement>()
const videos = ref<VideoItem[]>([])
const nextCursor = ref<string>()
const isInitialLoading = ref(true)
const isLoadingMore = ref(false)
const errorMessage = ref('')

const hasMore = computed(() => Boolean(nextCursor.value))
const playerElements = new Map<number, HTMLVideoElement>()
let playerObserver: IntersectionObserver | undefined

function registerPlayer(id: number, element: Element | null) {
  if (element instanceof HTMLVideoElement) {
    playerElements.set(id, element)
    return
  }
  playerElements.delete(id)
}

function syncPlayback(activeID: number) {
  for (const [id, player] of playerElements) {
    if (id === activeID) {
      void player.play().catch(() => undefined)
    } else {
      player.pause()
    }
  }
}

function observePlayers() {
  if (typeof IntersectionObserver === 'undefined') {
    return
  }

  playerObserver?.disconnect()
  playerObserver = new IntersectionObserver(
    (entries) => {
      const active = entries
        .filter((entry) => entry.isIntersecting)
        .sort((left, right) => right.intersectionRatio - left.intersectionRatio)[0]
      if (active) {
        const id = Number((active.target as HTMLElement).dataset.videoId)
        if (Number.isSafeInteger(id)) {
          syncPlayback(id)
        }
      }
    },
    { root: feedElement.value, threshold: [0.6, 0.75] },
  )

  for (const [id, player] of playerElements) {
    player.dataset.videoId = String(id)
    playerObserver.observe(player)
  }
}

function requestErrorMessage(error: unknown) {
  return error instanceof ApiError ? error.message : '视频加载失败，请检查网络后重试'
}

async function loadFirstPage() {
  isInitialLoading.value = true
  errorMessage.value = ''

  try {
    const response = await listPublishedVideos()
    videos.value = response.items
    nextCursor.value = response.next_cursor
    await nextTick()
    observePlayers()
  } catch (error) {
    videos.value = []
    nextCursor.value = undefined
    errorMessage.value = requestErrorMessage(error)
  } finally {
    isInitialLoading.value = false
  }
}

async function loadMore() {
  if (!nextCursor.value || isLoadingMore.value) {
    return
  }

  isLoadingMore.value = true
  errorMessage.value = ''

  try {
    const response = await listPublishedVideos({ cursor: nextCursor.value })
    videos.value.push(...response.items)
    nextCursor.value = response.next_cursor
    await nextTick()
    observePlayers()
  } catch (error) {
    errorMessage.value = requestErrorMessage(error)
  } finally {
    isLoadingMore.value = false
  }
}

function handleScroll(event: Event) {
  const container = event.currentTarget as HTMLElement
  const remaining = container.scrollHeight - container.scrollTop - container.clientHeight
  if (remaining < container.clientHeight * 0.75) {
    void loadMore()
  }
}

function retry() {
  if (videos.value.length && nextCursor.value) {
    void loadMore()
    return
  }
  void loadFirstPage()
}

onMounted(loadFirstPage)

onBeforeUnmount(() => {
  playerObserver?.disconnect()
  for (const player of playerElements.values()) {
    player.pause()
  }
})
</script>

<template>
  <main ref="feedElement" class="short-feed" aria-label="最新视频" @scroll="handleScroll">
    <h1 class="sr-only">最新视频</h1>

    <section v-if="isInitialLoading" class="loading-feed" aria-label="正在加载视频">
      <article v-for="index in 2" :key="index" class="short-video short-video--skeleton" aria-hidden="true">
        <div class="skeleton-copy">
          <span></span>
          <span></span>
          <span></span>
        </div>
      </article>
    </section>

    <section v-else-if="videos.length" class="video-stream" aria-label="视频列表">
      <article v-for="video in videos" :key="video.id" class="short-video">
        <video
          :ref="(element) => registerPlayer(video.id, element as Element | null)"
          class="short-video__player"
          :poster="video.cover_url"
          :src="video.play_url"
          controls
          controlslist="nodownload noplaybackrate"
          loop
          muted
          playsinline
          preload="metadata"
        >
          抱歉，你的浏览器不支持视频播放。
        </video>
        <div class="short-video__meta">
          <p class="short-video__author">@{{ video.author.username }}</p>
          <h2>{{ video.title }}</h2>
          <p v-if="video.description" class="short-video__description">{{ video.description }}</p>
        </div>
      </article>

      <div v-if="isLoadingMore" class="stream-status" role="status">正在加载更多视频</div>
      <div v-else-if="errorMessage" class="stream-status stream-status--error" role="alert">
        {{ errorMessage }}
        <button type="button" @click="retry">重试</button>
      </div>
      <div v-else-if="!hasMore" class="stream-status">已经到底了</div>
    </section>

    <section v-else class="feed-message" role="alert">
      <p>{{ errorMessage || '暂时没有公开视频' }}</p>
      <button v-if="errorMessage" type="button" @click="retry">重试</button>
    </section>
  </main>
</template>

<style scoped>
.short-feed {
  height: calc(100dvh - 64px);
  overflow-y: auto;
  background: #0e1012;
  scroll-snap-type: y mandatory;
  overscroll-behavior-y: contain;
}

.loading-feed,
.video-stream {
  min-height: 100%;
}

.short-video {
  position: relative;
  min-height: calc(100dvh - 64px);
  overflow: hidden;
  background: #171a1e;
  scroll-snap-align: start;
  scroll-snap-stop: always;
}

.short-video__player {
  display: block;
  width: 100%;
  height: calc(100dvh - 64px);
  background: #000000;
  object-fit: cover;
}

.short-video__meta {
  position: absolute;
  right: max(24px, calc((100vw - 1180px) / 2));
  bottom: 100px;
  left: max(24px, calc((100vw - 1180px) / 2));
  max-width: 680px;
  color: #ffffff;
  pointer-events: none;
  text-shadow: 0 1px 4px #000000, 0 2px 16px #000000;
}

.short-video__author,
.short-video__description,
.short-video__meta h2 {
  margin-top: 0;
}

.short-video__author {
  margin-bottom: 8px;
  font-size: 0.92rem;
  font-weight: 700;
}

.short-video__meta h2 {
  margin-bottom: 8px;
  font-size: 1.35rem;
  line-height: 1.25;
}

.short-video__description {
  display: -webkit-box;
  max-width: 54ch;
  margin-bottom: 0;
  overflow: hidden;
  font-size: 0.95rem;
  line-height: 1.5;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.short-video--skeleton {
  background: #20262b;
}

.short-video--skeleton::after {
  position: absolute;
  inset: 0;
  background: #ffffff0d;
  animation: loading-pulse 1.35s ease-in-out infinite alternate;
  content: '';
}

.skeleton-copy {
  position: absolute;
  right: 24px;
  bottom: 72px;
  left: 24px;
}

.skeleton-copy span {
  display: block;
  width: min(80%, 420px);
  height: 15px;
  margin-top: 12px;
  background: #ffffff26;
}

.skeleton-copy span:nth-child(1) {
  width: 110px;
}

.skeleton-copy span:nth-child(2) {
  height: 24px;
}

.stream-status {
  min-height: 72px;
  padding: 24px;
  color: #aab0b7;
  text-align: center;
}

.stream-status--error {
  color: #f2b8aa;
}

.stream-status button,
.feed-message button {
  margin-left: 12px;
  border: 0;
  padding: 0;
  color: #72d5c4;
  background: transparent;
  font: inherit;
  font-weight: 700;
  text-decoration: underline;
  text-underline-offset: 3px;
  cursor: pointer;
}

.feed-message {
  display: grid;
  min-height: calc(100dvh - 64px);
  place-content: center;
  gap: 12px;
  padding: 24px;
  color: #d6d9dd;
  text-align: center;
}

.feed-message p {
  margin: 0;
}

.feed-message button {
  margin: 0;
}

@keyframes loading-pulse {
  to {
    opacity: 0.25;
  }
}

@media (max-width: 640px) {
  .short-feed,
  .short-video,
  .short-video__player,
  .feed-message {
    min-height: calc(100dvh - 56px);
    height: calc(100dvh - 56px);
  }

  .short-video__meta {
    right: 18px;
    bottom: 92px;
    left: 18px;
  }

  .short-video__meta h2 {
    font-size: 1.2rem;
  }
}
</style>
