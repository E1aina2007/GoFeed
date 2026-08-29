<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import LikeButton from '@/features/social/LikeButton.vue'
import { usePublishedFeed } from '@/features/video/usePublishedFeed'

const route = useRoute()
const router = useRouter()
const feedElement = ref<HTMLElement>()
const publishedMessage = ref('')
const {
  videos,
  nextCursor,
  isInitialLoading,
  isLoadingMore,
  errorMessage,
  hasMore,
  loadFirstPage: loadFeedFirstPage,
  loadMore: loadFeedMore,
  dispose,
} = usePublishedFeed()
const playerElements = new Map<number, HTMLVideoElement>()
const visiblePlayerRatios = new Map<number, number>()
let playerObserver: IntersectionObserver | undefined
let activePlayerID: number | undefined

function registerPlayer(id: number, element: Element | null) {
  if (element instanceof HTMLVideoElement) {
    playerElements.set(id, element)
    return
  }
  playerElements.delete(id)
  visiblePlayerRatios.delete(id)
  if (activePlayerID === id) {
    activePlayerID = undefined
  }
}

function pausePlayers() {
  for (const player of playerElements.values()) {
    player.pause()
  }
}

function pageIsVisible() {
  return document.visibilityState !== 'hidden'
}

function syncPlayback(activeID?: number) {
  activePlayerID = activeID
  if (!activeID || !pageIsVisible()) {
    pausePlayers()
    return
  }

  for (const [id, player] of playerElements) {
    if (id === activeID) {
      void player
        .play()
        .then(() => {
          if (activePlayerID !== id || !pageIsVisible()) {
            player.pause()
          }
        })
        .catch(() => undefined)
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
  visiblePlayerRatios.clear()
  playerObserver = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        const id = Number((entry.target as HTMLElement).dataset.videoId)
        if (!Number.isSafeInteger(id)) {
          continue
        }
        if (entry.isIntersecting) {
          visiblePlayerRatios.set(id, entry.intersectionRatio)
        } else {
          visiblePlayerRatios.delete(id)
        }
      }

      const activeID = [...visiblePlayerRatios.entries()].sort(
        ([, leftRatio], [, rightRatio]) => rightRatio - leftRatio,
      )[0]?.[0]
      syncPlayback(activeID)
    },
    { root: feedElement.value, threshold: [0.6, 0.75] },
  )

  for (const [id, player] of playerElements) {
    player.dataset.videoId = String(id)
    playerObserver.observe(player)
  }
}

function handleVisibilityChange() {
  if (!pageIsVisible()) {
    pausePlayers()
    return
  }
  syncPlayback(activePlayerID)
}

function publishedVideoID() {
  const value = route.query.published
  const rawID = Array.isArray(value) ? value[0] : value
  if (typeof rawID !== 'string') {
    return undefined
  }

  const id = Number(rawID)
  return Number.isSafeInteger(id) && id > 0 ? id : undefined
}

async function clearPublishedQuery() {
  const query = { ...route.query }
  delete query.published
  await router.replace({ query })
}

async function loadFirstPage() {
  publishedMessage.value = ''
  const publishedID = publishedVideoID()
  const response = await loadFeedFirstPage()
  if (!response) {
    return
  }

  if (publishedID) {
    if (response.items.some((video) => video.id === publishedID)) {
      publishedMessage.value = '视频已发布'
    }
    await clearPublishedQuery()
  }
  await nextTick()
  observePlayers()
}

async function loadMore() {
  const response = await loadFeedMore()
  if (!response) {
    return
  }

  await nextTick()
  observePlayers()
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

onMounted(() => {
  document.addEventListener('visibilitychange', handleVisibilityChange)
  void loadFirstPage()
})

onBeforeUnmount(() => {
  dispose()
  playerObserver?.disconnect()
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  pausePlayers()
  playerElements.clear()
  visiblePlayerRatios.clear()
})
</script>

<template>
  <main ref="feedElement" class="short-feed" aria-label="最新视频" @scroll="handleScroll">
    <h1 class="sr-only">最新视频</h1>

    <p v-if="publishedMessage" class="feed-notice" role="status">{{ publishedMessage }}</p>

    <section v-if="isInitialLoading" class="loading-feed" aria-label="正在加载视频">
      <article
        v-for="index in 2"
        :key="index"
        class="short-video short-video--skeleton"
        aria-hidden="true"
      >
        <div class="skeleton-copy">
          <span></span>
          <span></span>
          <span></span>
        </div>
      </article>
    </section>

    <section v-else-if="videos.length" class="video-stream" aria-label="视频列表">
      <article
        v-for="video in videos"
        :key="video.id"
        class="short-video"
        :style="{ '--feed-cover': `url(${video.cover_url})` }"
      >
        <div class="short-video__stage">
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
            <RouterLink
              class="short-video__author"
              :to="{ name: 'user-profile', params: { id: video.author.id } }"
            >
              @{{ video.author.username }}
            </RouterLink>
            <h2>
              <RouterLink :to="{ name: 'video-detail', params: { id: video.id } }">{{
                video.title
              }}</RouterLink>
            </h2>
            <p v-if="video.description" class="short-video__description">{{ video.description }}</p>
          </div>
          <div class="short-video__actions" aria-label="视频互动">
            <LikeButton
              :video-id="video.id"
              :likes-count="video.likes_count"
              :comments-count="video.comments_count"
              variant="overlay"
            />
            <RouterLink
              class="short-video__comments"
              :to="{ name: 'video-detail', params: { id: video.id } }"
            >
              评论 {{ video.comments_count }}
            </RouterLink>
          </div>
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
  height: calc(100dvh - 60px);
  overflow-y: auto;
  background: #0e1012;
  scroll-snap-type: y mandatory;
  overscroll-behavior-y: contain;
}

.loading-feed,
.video-stream {
  min-height: 100%;
}

.feed-notice {
  position: fixed;
  z-index: 20;
  top: 80px;
  left: 50%;
  margin: 0;
  border: 1px solid #4c8e83;
  border-radius: 4px;
  padding: 8px 12px;
  color: #e6fffa;
  background: #193d36;
  font-size: 0.9rem;
  transform: translateX(-50%);
}

.short-video {
  position: relative;
  height: calc(100dvh - 60px);
  overflow: hidden;
  background: #171a1e;
  scroll-snap-align: start;
  scroll-snap-stop: always;
}

/* 参考抖音：contain 留出的区域用模糊压暗的封面填充，任何宽高比都不裁切 */
.short-video::before {
  content: '';
  position: absolute;
  inset: -24px;
  background: var(--feed-cover) center / cover no-repeat;
  filter: blur(32px) brightness(0.45);
  transform: scale(1.1);
}

/* 播放舞台：桌面端收敛为手机比例的居中列，移动端铺满视口 */
.short-video__stage {
  position: relative;
  width: min(100%, calc((100dvh - 60px) * 9 / 16));
  height: 100%;
  margin-inline: auto;
}

.short-video__player {
  display: block;
  width: 100%;
  height: 100%;
  background: transparent;
  object-fit: contain;
}

.short-video__meta {
  position: absolute;
  right: 96px;
  bottom: 100px;
  left: 24px;
  color: #ffffff;
  pointer-events: none;
  text-shadow:
    0 1px 4px #000000,
    0 2px 16px #000000;
}

.short-video__actions {
  position: absolute;
  right: 16px;
  bottom: 96px;
  display: grid;
  gap: 8px;
  justify-items: end;
}

.short-video__comments {
  min-height: 34px;
  padding: 8px 10px;
  border: 1px solid #ffffff66;
  border-radius: 6px;
  color: #ffffff;
  background: #0b1110a8;
  box-shadow: 0 4px 16px #00000033;
  font-size: 0.82rem;
  font-weight: 700;
  text-decoration: none;
}

.short-video__comments:hover {
  border-color: #72d5c4;
  color: #c7fff1;
  background: #144b41e8;
}

.short-video__author,
.short-video__description,
.short-video__meta h2 {
  margin-top: 0;
}

.short-video__author,
.short-video__meta h2 a {
  color: inherit;
  text-decoration: none;
  pointer-events: auto;
}

.short-video__author {
  margin-bottom: 8px;
  font-size: 0.92rem;
  font-weight: 700;
  pointer-events: auto;
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
  min-height: calc(100dvh - 60px);
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

@media (max-width: 900px) {
  .short-feed,
  .short-video,
  .short-video__stage,
  .feed-message {
    min-height: calc(100dvh - 102px);
    height: calc(100dvh - 102px);
  }

  .short-video__stage {
    width: 100%;
  }

  .short-video__meta {
    right: 96px;
    bottom: 92px;
    left: 18px;
  }

  .short-video__actions {
    right: 14px;
    bottom: 88px;
  }

  .feed-notice {
    top: 114px;
  }

  .short-video__meta h2 {
    font-size: 1.2rem;
  }
}
</style>
