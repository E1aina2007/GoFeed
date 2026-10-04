<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { currentUser, isAuthenticated } from '@/features/auth/session'
import LikeButton from '@/features/social/LikeButton.vue'
import { usePublishedFeed, type FeedScene } from '@/features/video/usePublishedFeed'

const route = useRoute()
const router = useRouter()
const feedElement = ref<HTMLElement>()
const publishedMessage = ref('')
const {
  scene,
  videos,
  nextCursor,
  isInitialLoading,
  isLoadingMore,
  errorMessage,
  hasMore,
  isSceneLoaded,
  setScene,
  loadFirstPage: loadFeedFirstPage,
  loadMore: loadFeedMore,
  dispose,
} = usePublishedFeed()
const playerElements = new Map<number, HTMLVideoElement>()
const visiblePlayerRatios = new Map<number, number>()
let playerObserver: IntersectionObserver | undefined
let activePlayerID: number | undefined
// 切换代次：场景切换或观看者变化时递增，迟到的异步回调不得作用于新状态
let sceneGeneration = 0

// 场景的唯一事实来源是 URL 查询参数：单值 following 进入 Following，其余一律回退 Timeline
function sceneFromRoute(): FeedScene {
  return route.query.scene === 'following' ? 'following' : 'timeline'
}

// 初始化场景（登录回跳、外部直达 /?scene=following、刷新后恢复）
setScene(sceneFromRoute())

const viewerID = computed(() => currentUser.value?.id ?? null)
const showFollowingSignIn = computed(() => scene.value === 'following' && !isAuthenticated.value)
const emptyFeedText = computed(() =>
  scene.value === 'following' ? '还没有可看的关注视频' : '暂时没有公开视频',
)
const followingRedirectFullPath = computed(() =>
  router.resolve({ path: '/', query: { ...route.query, scene: 'following' } }).fullPath,
)

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

// 作废旧场景的播放器观察与播放状态，防止迟到回调跨场景生效
function detachPlayersObserver() {
  playerObserver?.disconnect()
  playerObserver = undefined
  visiblePlayerRatios.clear()
  activePlayerID = undefined
}

// 根据当前场景的视频可见范围更新播放状态
function observePlayers() {
  if (typeof IntersectionObserver === 'undefined') {
    return
  }

  detachPlayersObserver()
  const observer = new IntersectionObserver(
    (entries) => {
      // 丢弃旧场景观察者的迟到回调
      if (playerObserver !== observer) {
        return
      }
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
  playerObserver = observer

  for (const [id, player] of playerElements) {
    player.dataset.videoId = String(id)
    observer.observe(player)
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

function scrollFeedToTop() {
  const container = feedElement.value
  if (container) {
    container.scrollTop = 0
  }
}

// 加载当前场景首屏并重置滚动位置与播放状态
async function loadFirstPage() {
  const generation = sceneGeneration
  const startedScene = scene.value
  publishedMessage.value = ''
  pausePlayers()
  detachPlayersObserver()
  scrollFeedToTop()
  const publishedID = publishedVideoID()
  const response = await loadFeedFirstPage()
  if (!response || generation !== sceneGeneration || scene.value !== startedScene) {
    return
  }

  if (publishedID) {
    if (response.items.some((video) => video.id === publishedID)) {
      publishedMessage.value = '视频已发布'
    }
    await clearPublishedQuery()
  }
  await nextTick()
  if (generation !== sceneGeneration || scene.value !== startedScene) {
    return
  }
  scrollFeedToTop()
  observePlayers()
}

// 追加当前场景的视频并更新播放器观察
async function loadMore() {
  const generation = sceneGeneration
  const response = await loadFeedMore()
  if (!response || generation !== sceneGeneration) {
    return
  }

  await nextTick()
  if (generation !== sceneGeneration) {
    return
  }
  observePlayers()
}

function handleScroll(event: Event) {
  const container = event.currentTarget as HTMLElement
  const remaining = container.scrollHeight - container.scrollTop - container.clientHeight
  if (remaining < container.clientHeight * 0.75 && !errorMessage.value) {
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

let pendingSceneNavigation: { scene: FeedScene } | undefined

// 切换 URL 场景，快速连点时以最后一次选择为准
function switchScene(next: FeedScene) {
  if (pendingSceneNavigation?.scene === next) {
    return
  }
  if (!pendingSceneNavigation && sceneFromRoute() === next) {
    return
  }
  const navigation = { scene: next }
  pendingSceneNavigation = navigation
  const query = { ...route.query }
  if (next === 'following') {
    query.scene = 'following'
  } else {
    delete query.scene
  }
  void router.replace({ query }).finally(() => {
    // 被更新导航取代的旧请求不得清除新一次点击的目标
    if (pendingSceneNavigation === navigation) {
      pendingSceneNavigation = undefined
    }
  })
}

// 应用路由场景，将列表与播放器恢复到首屏状态
async function applyScene(next: FeedScene) {
  if (scene.value === next) {
    return
  }
  sceneGeneration += 1
  const generation = sceneGeneration
  pausePlayers()
  detachPlayersObserver()
  scrollFeedToTop()
  setScene(next)
  await nextTick()
  if (generation !== sceneGeneration || scene.value !== next) {
    return
  }
  scrollFeedToTop()
  observePlayers()
  if (!isSceneLoaded.value) {
    await loadFirstPage()
  }
}

onMounted(() => {
  document.addEventListener('visibilitychange', handleVisibilityChange)
  void loadFirstPage()
})

watch(
  () => route.query.scene,
  () => {
    // 离开 Feed 路由（如跳转登录页）时不响应查询参数变化
    if (route.name !== 'feed') {
      return
    }
    void applyScene(sceneFromRoute())
  },
)

watch(viewerID, () => {
  // 退出或更换观看者：hook 已作废旧状态，这里重新加载当前场景并回到顶部
  sceneGeneration += 1
  void loadFirstPage()
})

onBeforeUnmount(() => {
  dispose()
  detachPlayersObserver()
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  pausePlayers()
  playerElements.clear()
  visiblePlayerRatios.clear()
})
</script>

<template>
  <main ref="feedElement" class="short-feed" aria-label="最新视频" @scroll="handleScroll">
    <nav class="feed-tabs" aria-label="切换视频流">
      <button
        type="button"
        class="feed-tab"
        :class="{ 'feed-tab--active': scene === 'timeline' }"
        :aria-pressed="scene === 'timeline'"
        @click="switchScene('timeline')"
      >
        最新
      </button>
      <button
        type="button"
        class="feed-tab"
        :class="{ 'feed-tab--active': scene === 'following' }"
        :aria-pressed="scene === 'following'"
        @click="switchScene('following')"
      >
        关注
      </button>
    </nav>

    <h1 class="sr-only">最新视频</h1>

    <p v-if="publishedMessage" class="feed-notice" role="status">{{ publishedMessage }}</p>

    <section v-if="showFollowingSignIn" class="feed-message" role="status">
      <p>{{ errorMessage || '登录后查看关注作者的最新视频' }}</p>
      <RouterLink
        class="feed-signin-link"
        :to="{ name: 'login', query: { redirect: followingRedirectFullPath } }"
      >
        登录
      </RouterLink>
    </section>

    <section v-else-if="isInitialLoading" class="loading-feed" aria-label="正在加载视频">
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
      <p>{{ errorMessage || emptyFeedText }}</p>
      <button v-if="errorMessage" type="button" @click="retry">重试</button>
    </section>
  </main>
</template>

<style scoped>
/* 视觉隐藏的标题必须离开文档流：否则它会占位滚动容器顶部，
   把第一张卡片的 scroll-snap 吸附点推离 scrollTop=0，切换后的回顶会被弹回占位处 */
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  padding: 0;
  overflow: hidden;
  clip: rect(0 0 0 0);
  clip-path: inset(50%);
  white-space: nowrap;
  border: 0;
}

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
  top: 128px;
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

/* 场景切换悬浮在视频流上方，桌面与移动都固定在顶部栏下方 */
.feed-tabs {
  position: fixed;
  z-index: 15;
  top: 72px;
  left: 50%;
  display: inline-flex;
  gap: 4px;
  border: 1px solid #ffffff26;
  border-radius: 999px;
  padding: 4px;
  background: #0b1110a8;
  box-shadow: 0 4px 16px #00000033;
  transform: translateX(-50%);
  backdrop-filter: blur(8px);
}

.feed-tab {
  min-height: 32px;
  border: 0;
  border-radius: 999px;
  padding: 6px 16px;
  color: #cfd6d4;
  background: transparent;
  font: inherit;
  font-size: 0.86rem;
  font-weight: 700;
  cursor: pointer;
}

.feed-tab--active {
  color: #062922;
  background: #72d5c4;
}

.feed-signin-link {
  display: inline-flex;
  min-height: 38px;
  align-items: center;
  border: 1px solid #2e8e7c;
  border-radius: 8px;
  padding: 8px 18px;
  color: #f5fffc;
  background: #176557;
  font-weight: 700;
  text-decoration: none;
}

.feed-signin-link:hover {
  background: #1d7968;
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
  scroll-snap-align: end;
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
    top: 162px;
  }

  .feed-tabs {
    top: 110px;
  }

  .short-video__meta h2 {
    font-size: 1.2rem;
  }
}
</style>
