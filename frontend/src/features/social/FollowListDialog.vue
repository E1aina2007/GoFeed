<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'

import { ApiError } from '@/lib/api'

import { getFollowerList, getFollowingList, type FollowListItem } from './api'

type FollowListMode = 'followers' | 'following'

const props = defineProps<{
  open: boolean
  userId: number
  mode: FollowListMode
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
  'update:mode': [mode: FollowListMode]
}>()

const items = ref<FollowListItem[]>([])
const nextCursor = ref<string>()
const isLoading = ref(false)
const isLoadingMore = ref(false)
const errorMessage = ref('')
let listRequestID = 0

const title = computed(() => (props.mode === 'followers' ? '粉丝' : '关注'))
const hasMore = computed(() => Boolean(nextCursor.value))

function apiMessage(error: unknown) {
  return error instanceof ApiError ? error.message : '列表加载失败，请稍后重试'
}

function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN')
}

async function loadList(cursor?: string) {
  if (!props.open) {
    return
  }

  const requestID = ++listRequestID
  if (cursor) {
    isLoadingMore.value = true
  } else {
    isLoading.value = true
    errorMessage.value = ''
  }

  try {
    const getList = props.mode === 'followers' ? getFollowerList : getFollowingList
    const response = await getList(props.userId, { cursor })
    if (requestID !== listRequestID || !props.open) {
      return
    }
    items.value = cursor ? [...items.value, ...response.items] : response.items
    nextCursor.value = response.next_cursor
  } catch (error) {
    if (requestID === listRequestID && props.open) {
      errorMessage.value = apiMessage(error)
    }
  } finally {
    if (requestID === listRequestID) {
      isLoading.value = false
      isLoadingMore.value = false
    }
  }
}

function close() {
  emit('update:open', false)
}

function selectMode(mode: FollowListMode) {
  if (mode !== props.mode) {
    emit('update:mode', mode)
  }
}

function retry() {
  void loadList()
}

function loadMore() {
  if (!nextCursor.value || isLoadingMore.value) {
    return
  }
  void loadList(nextCursor.value)
}

watch(
  () => [props.open, props.userId, props.mode] as const,
  ([open]) => {
    ++listRequestID
    items.value = []
    nextCursor.value = undefined
    errorMessage.value = ''
    isLoading.value = false
    isLoadingMore.value = false
    if (open) {
      void loadList()
    }
  },
  { immediate: true },
)
</script>

<template>
  <Teleport to="body">
    <section v-if="open" class="follow-dialog-backdrop" role="presentation" @click.self="close">
      <div class="follow-dialog" role="dialog" aria-modal="true" :aria-label="`${title}列表`">
        <header class="follow-dialog__header">
          <div class="follow-tabs" role="tablist" aria-label="关系列表">
            <button
              class="follow-tabs__item"
              :class="{ 'follow-tabs__item--active': mode === 'followers' }"
              type="button"
              role="tab"
              :aria-selected="mode === 'followers'"
              @click="selectMode('followers')"
            >
              粉丝
            </button>
            <button
              class="follow-tabs__item"
              :class="{ 'follow-tabs__item--active': mode === 'following' }"
              type="button"
              role="tab"
              :aria-selected="mode === 'following'"
              @click="selectMode('following')"
            >
              关注
            </button>
          </div>
          <button
            class="close-button"
            type="button"
            aria-label="关闭列表"
            title="关闭"
            @click="close"
          >
            ×
          </button>
        </header>

        <div v-if="isLoading" class="list-state" role="status">正在加载{{ title }}</div>
        <div
          v-else-if="errorMessage && !items.length"
          class="list-state list-state--error"
          role="alert"
        >
          <p>{{ errorMessage }}</p>
          <button class="retry-button" type="button" @click="retry">重试</button>
        </div>
        <div v-else-if="!items.length" class="list-state">暂时没有{{ title }}</div>
        <ul v-else class="follow-list">
          <li v-for="item in items" :key="item.user.id">
            <RouterLink
              class="follow-user"
              :to="{ name: 'user-profile', params: { id: item.user.id } }"
              @click="close"
            >
              <span class="follow-user__avatar" aria-hidden="true">
                <img v-if="item.user.avatar_url" :src="item.user.avatar_url" alt="" />
                <span v-else>{{ item.user.username.slice(0, 1).toUpperCase() }}</span>
              </span>
              <span class="follow-user__copy">
                <strong>@{{ item.user.username }}</strong>
                <small>{{ formatDate(item.followed_at) }}</small>
              </span>
            </RouterLink>
          </li>
        </ul>

        <p v-if="errorMessage && items.length" class="inline-error" role="alert">
          {{ errorMessage }}
        </p>
        <button
          v-if="hasMore"
          class="more-button"
          type="button"
          :disabled="isLoadingMore"
          @click="loadMore"
        >
          {{ isLoadingMore ? '正在加载' : '加载更多' }}
        </button>
      </div>
    </section>
  </Teleport>
</template>

<style scoped>
.follow-dialog-backdrop {
  --content-surface: #182020;
  --content-surface-raised: #202b2a;
  --content-border: #2b3a38;
  --content-border-strong: #3a504c;
  --content-ink: #edf2f0;
  --content-muted: #a4b5b1;
  --content-subtle: #7f938e;
  --content-accent: #55d2b4;
  --content-accent-strong: #35b795;
  --content-danger: #f0a096;
  position: fixed;
  z-index: 50;
  inset: 0;
  display: grid;
  place-items: center;
  padding: 16px;
  background: #060b0ba8;
}

.follow-dialog {
  display: grid;
  width: min(100%, 520px);
  max-height: min(72dvh, 620px);
  overflow: auto;
  border: 1px solid var(--content-border-strong);
  border-radius: 8px;
  color: var(--content-ink);
  background: var(--content-surface);
}

.follow-dialog__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  border-bottom: 1px solid var(--content-border);
  padding: 12px 14px;
}

.follow-tabs {
  display: inline-flex;
  border: 1px solid var(--content-border-strong);
  border-radius: 6px;
  overflow: hidden;
}

.follow-tabs__item {
  min-width: 72px;
  min-height: 34px;
  border: 0;
  border-left: 1px solid var(--content-border-strong);
  color: var(--content-muted);
  background: transparent;
  cursor: pointer;
}

.follow-tabs__item:first-child {
  border-left: 0;
}

.follow-tabs__item--active {
  color: #062922;
  background: var(--content-accent);
  font-weight: 700;
}

.close-button {
  display: grid;
  width: 32px;
  height: 32px;
  place-items: center;
  border: 1px solid var(--content-border-strong);
  border-radius: 5px;
  color: var(--content-ink);
  background: var(--content-surface-raised);
  font-size: 1.18rem;
  line-height: 1;
  cursor: pointer;
}

.list-state {
  padding: 42px 20px;
  color: var(--content-muted);
  text-align: center;
}

.list-state--error,
.inline-error {
  color: var(--content-danger);
}

.list-state p {
  margin-top: 0;
}

.follow-list {
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}

.follow-list li {
  border-top: 1px solid var(--content-border);
}

.follow-user {
  display: grid;
  grid-template-columns: 40px minmax(0, 1fr);
  gap: 11px;
  align-items: center;
  padding: 12px 14px;
  color: inherit;
  text-decoration: none;
}

.follow-user:hover {
  background: var(--content-surface-raised);
}

.follow-user__avatar {
  display: grid;
  width: 40px;
  height: 40px;
  place-items: center;
  overflow: hidden;
  border-radius: 50%;
  color: #ffffff;
  background: var(--content-accent-strong);
  font-size: 0.86rem;
  font-weight: 700;
}

.follow-user__avatar img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.follow-user__copy {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.follow-user__copy strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.follow-user__copy small {
  color: var(--content-subtle);
  font-size: 0.75rem;
}

.retry-button,
.more-button {
  min-height: 34px;
  border: 1px solid var(--content-border-strong);
  border-radius: 6px;
  padding: 7px 12px;
  color: var(--content-accent);
  background: var(--content-surface-raised);
  font-size: 0.84rem;
  font-weight: 700;
  cursor: pointer;
}

.more-button {
  justify-self: center;
  margin: 14px;
}

.more-button:disabled {
  cursor: wait;
  opacity: 0.65;
}

.inline-error {
  margin: 12px 14px 0;
  font-size: 0.86rem;
}

@media (max-width: 640px) {
  .follow-dialog-backdrop {
    align-items: end;
    padding: 0;
  }

  .follow-dialog {
    width: 100%;
    max-height: min(76dvh, 640px);
    border-right: 0;
    border-bottom: 0;
    border-left: 0;
    border-radius: 8px 8px 0 0;
  }
}
</style>
