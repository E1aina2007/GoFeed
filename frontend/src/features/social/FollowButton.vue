<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { currentUser, isAuthenticated } from '@/features/auth/session'
import { apiUserMessage } from '@/lib/api'
import { useToastStore } from '@/stores/toast'

import { createFollow, getFollowState, removeFollow, type FollowState } from './api'

const props = defineProps<{
  userId: number
  followerCount: number
}>()

const emit = defineEmits<{
  stateChange: [state: FollowState]
}>()

const route = useRoute()
const router = useRouter()
const toast = useToastStore()
const state = ref<FollowState>({ following: false, follower_count: props.followerCount })
const isPending = ref(false)
let stateRequestID = 0

const isSelf = computed(() => currentUser.value?.id === props.userId)

function normalizeCount(value: number) {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function applyState(nextState: FollowState) {
  state.value = {
    following: nextState.following,
    follower_count: normalizeCount(nextState.follower_count),
  }
  emit('stateChange', state.value)
}

async function loadState() {
  const requestID = ++stateRequestID
  if (!isAuthenticated.value || isSelf.value) {
    state.value = { following: false, follower_count: normalizeCount(props.followerCount) }
    return
  }

  try {
    const nextState = await getFollowState(props.userId)
    if (requestID === stateRequestID) {
      applyState(nextState)
    }
  } catch {
    if (requestID === stateRequestID) {
      state.value.following = false
    }
  }
}

async function redirectToLogin() {
  toast.info('请先登录后再关注')
  await router.push({ name: 'login', query: { redirect: route.fullPath } })
}

async function toggleFollow() {
  if (!isAuthenticated.value) {
    await redirectToLogin()
    return
  }
  if (isSelf.value || isPending.value) {
    return
  }

  isPending.value = true
  ++stateRequestID
  try {
    const nextState = state.value.following
      ? await removeFollow(props.userId)
      : await createFollow(props.userId)
    applyState(nextState)
    if (nextState.following) {
      toast.success('已关注')
    } else {
      toast.info('已取消关注')
    }
  } catch (error) {
    toast.error(apiUserMessage(error, '关注操作失败，请稍后重试'))
  } finally {
    isPending.value = false
  }
}

watch(
  () => props.userId,
  () => {
    state.value = { following: false, follower_count: normalizeCount(props.followerCount) }
    void loadState()
  },
  { immediate: true },
)

watch(
  () => props.followerCount,
  (followerCount) => {
    state.value.follower_count = normalizeCount(followerCount)
  },
)

watch(isAuthenticated, () => {
  void loadState()
})
</script>

<template>
  <button
    v-if="!isSelf"
    class="follow-button"
    type="button"
    :aria-pressed="state.following"
    :disabled="isPending"
    @click="toggleFollow"
  >
    {{ isPending ? '正在处理' : state.following ? '已关注' : '关注' }}
  </button>
</template>

<style scoped>
.follow-button {
  min-width: 76px;
  min-height: 38px;
  border: 1px solid var(--content-accent-strong);
  border-radius: 6px;
  padding: 8px 13px;
  color: #062922;
  background: var(--content-accent);
  font-size: 0.86rem;
  font-weight: 700;
  cursor: pointer;
}

.follow-button:hover:not(:disabled) {
  background: #7de3ca;
}

.follow-button[aria-pressed='true'] {
  border-color: var(--content-border-strong);
  color: var(--content-accent);
  background: var(--content-surface-raised);
}

.follow-button:disabled {
  cursor: wait;
  opacity: 0.65;
}
</style>
