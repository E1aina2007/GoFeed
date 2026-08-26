<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import VideoListItem from '@/components/VideoListItem.vue'
import FollowButton from '@/features/social/FollowButton.vue'
import FollowListDialog from '@/features/social/FollowListDialog.vue'
import type { FollowState } from '@/features/social/api'
import { listPublishedVideos, type VideoItem } from '@/features/video/api'
import { getUserProfile, type UserProfile } from '@/features/user/api'
import { ApiError } from '@/lib/api'

const route = useRoute()
const profile = ref<UserProfile>()
const videos = ref<VideoItem[]>([])
const nextCursor = ref<string>()
const isLoading = ref(true)
const isLoadingMore = ref(false)
const errorMessage = ref('')
const hasMore = computed(() => Boolean(nextCursor.value))
const isFollowListOpen = ref(false)
const followListMode = ref<'followers' | 'following'>('followers')

function userID() {
  const id = Number(route.params.id)
  return Number.isSafeInteger(id) && id > 0 ? id : undefined
}

function apiMessage(error: unknown) {
  return error instanceof ApiError ? error.message : '用户主页加载失败，请稍后重试'
}

async function load() {
  const id = userID()
  if (!id) {
    profile.value = undefined
    videos.value = []
    nextCursor.value = undefined
    errorMessage.value = '用户地址无效'
    isLoading.value = false
    return
  }

  isLoading.value = true
  errorMessage.value = ''
  profile.value = undefined
  videos.value = []
  nextCursor.value = undefined
  try {
    const [profileResponse, videoResponse] = await Promise.all([
      getUserProfile(id),
      listPublishedVideos({ authorID: id }),
    ])
    profile.value = profileResponse
    videos.value = videoResponse.items
    nextCursor.value = videoResponse.next_cursor
  } catch (error) {
    errorMessage.value = apiMessage(error)
  } finally {
    isLoading.value = false
  }
}

async function loadMore() {
  const id = userID()
  if (!id || !nextCursor.value || isLoadingMore.value) {
    return
  }
  isLoadingMore.value = true
  try {
    const response = await listPublishedVideos({ authorID: id, cursor: nextCursor.value })
    videos.value.push(...response.items)
    nextCursor.value = response.next_cursor
  } catch (error) {
    errorMessage.value = apiMessage(error)
  } finally {
    isLoadingMore.value = false
  }
}

function updateFollowState(state: FollowState) {
  if (profile.value) {
    profile.value.follower_count = state.follower_count
  }
}

function openFollowList(mode: 'followers' | 'following') {
  followListMode.value = mode
  isFollowListOpen.value = true
}

watch(
  () => route.params.id,
  () => {
    isFollowListOpen.value = false
    void load()
  },
  { immediate: true },
)
</script>

<template>
  <main class="content-page content-page--dark">
    <div class="page-toolbar">
      <RouterLink class="back-link" :to="{ name: 'user-list' }">返回用户</RouterLink>
    </div>
    <section v-if="isLoading" class="state-message" role="status">正在加载用户主页</section>
    <section v-else-if="errorMessage && !profile" class="state-message" role="alert">
      <p>{{ errorMessage }}</p>
      <button class="secondary-action" type="button" @click="load">重试</button>
    </section>
    <template v-else-if="profile">
      <header class="profile-header">
        <div class="profile-avatar">
          <img
            v-if="profile.account.avatar_url"
            :src="profile.account.avatar_url"
            :alt="`${profile.account.username} 的头像`"
          />
          <span v-else>{{ profile.account.username.slice(0, 1).toUpperCase() }}</span>
        </div>
        <div class="profile-copy">
          <h1>@{{ profile.account.username }}</h1>
          <p>{{ profile.account.bio || '这个用户还没有填写简介' }}</p>
          <FollowButton
            :user-id="profile.account.id"
            :follower-count="profile.follower_count"
            @state-change="updateFollowState"
          />
        </div>
        <div class="profile-stats" aria-label="用户统计">
          <div class="profile-stat">
            <span>视频</span><strong>{{ profile.video_count }}</strong>
          </div>
          <div class="profile-stat">
            <span>获赞</span><strong>{{ profile.total_likes }}</strong>
          </div>
          <button
            class="profile-stat profile-stat--action"
            type="button"
            @click="openFollowList('followers')"
          >
            <span>粉丝</span><strong>{{ profile.follower_count }}</strong>
          </button>
          <button
            class="profile-stat profile-stat--action"
            type="button"
            @click="openFollowList('following')"
          >
            <span>关注</span><strong>{{ profile.vlogger_count }}</strong>
          </button>
        </div>
      </header>
      <section class="profile-videos">
        <h2>公开视频</h2>
        <p v-if="errorMessage" class="inline-error" role="alert">{{ errorMessage }}</p>
        <VideoListItem v-for="video in videos" :key="video.id" :video="video" />
        <p v-if="!videos.length" class="state-message">暂时没有公开视频</p>
        <button
          v-if="hasMore"
          class="secondary-action load-more"
          type="button"
          :disabled="isLoadingMore"
          @click="loadMore"
        >
          {{ isLoadingMore ? '正在加载' : '加载更多' }}
        </button>
      </section>
    </template>
    <FollowListDialog
      v-if="profile"
      v-model:open="isFollowListOpen"
      v-model:mode="followListMode"
      :user-id="profile.account.id"
    />
  </main>
</template>

<style scoped>
.content-page {
  min-height: calc(100dvh - 64px);
  padding: 32px 16px 64px;
  background: var(--content-bg);
}

.page-toolbar,
.profile-header,
.profile-videos {
  width: min(100%, 900px);
  margin-right: auto;
  margin-left: auto;
}

.page-toolbar {
  margin-bottom: 18px;
}

.back-link {
  color: var(--content-accent);
  font-weight: 700;
  text-decoration: none;
}

.back-link:hover {
  color: var(--content-ink);
}

.profile-header {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 22px;
  border: 1px solid var(--content-border);
  border-radius: 8px;
  padding: 20px;
  background: var(--content-surface);
}

.profile-avatar {
  display: grid;
  width: 84px;
  height: 84px;
  place-items: center;
  overflow: hidden;
  border-radius: 50%;
  color: #ffffff;
  background: var(--content-accent-strong);
  font-size: 1.8rem;
  font-weight: 700;
}

.profile-avatar img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.profile-copy h1,
.profile-copy p,
.profile-videos h2 {
  margin-top: 0;
}

.profile-copy h1 {
  margin-bottom: 8px;
  color: var(--content-ink);
  font-size: 1.45rem;
}

.profile-copy p {
  margin-bottom: 0;
  color: var(--content-muted);
  line-height: 1.5;
}

.profile-copy :deep(.follow-button) {
  margin-top: 14px;
}

.profile-stats {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 20px;
}

.profile-stat {
  display: grid;
  min-width: 60px;
  border: 0;
  padding: 0;
  color: inherit;
  background: transparent;
  font: inherit;
  text-align: center;
}

.profile-stat span {
  color: var(--content-subtle);
  font-size: 0.8rem;
}

.profile-stat strong {
  margin: 5px 0 0;
  color: var(--content-ink);
  font-size: 1.1rem;
  font-weight: 700;
}

.profile-stat--action {
  cursor: pointer;
}

.profile-stat--action:hover span,
.profile-stat--action:focus-visible span {
  color: var(--content-accent);
}

.profile-videos {
  padding-top: 26px;
}

.profile-videos h2 {
  color: var(--content-ink);
  font-size: 1.2rem;
}

.state-message {
  padding: 52px 20px;
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

.secondary-action:disabled {
  cursor: wait;
  opacity: 0.6;
}

.load-more {
  display: block;
  margin: 22px auto 0;
}

.inline-error {
  color: var(--content-danger);
}

@media (max-width: 640px) {
  .content-page {
    min-height: calc(100dvh - 56px);
    padding: 20px 12px 40px;
  }

  .profile-header {
    grid-template-columns: auto minmax(0, 1fr);
    gap: 14px;
    padding: 16px;
  }

  .profile-stats {
    grid-column: 1 / -1;
    justify-content: space-around;
    width: 100%;
    gap: 12px;
  }
}
</style>
