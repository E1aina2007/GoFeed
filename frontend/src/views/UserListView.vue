<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'

import { listUsers, type PublicUser } from '@/features/user/api'
import { ApiError } from '@/lib/api'

const users = ref<PublicUser[]>([])
const isLoading = ref(true)
const errorMessage = ref('')

async function load() {
  isLoading.value = true
  errorMessage.value = ''
  try {
    users.value = (await listUsers()).users
  } catch (error) {
    errorMessage.value = error instanceof ApiError ? error.message : '用户列表加载失败，请稍后重试'
  } finally {
    isLoading.value = false
  }
}

onMounted(load)
</script>

<template>
  <main class="content-page">
    <header class="page-header">
      <div>
        <p class="eyebrow">社区</p>
        <h1>用户</h1>
      </div>
    </header>
    <section v-if="isLoading" class="state-message" role="status">正在加载用户</section>
    <section v-else-if="errorMessage" class="state-message" role="alert">
      <p>{{ errorMessage }}</p>
      <button class="secondary-action" type="button" @click="load">重试</button>
    </section>
    <section v-else-if="users.length" class="user-list">
      <RouterLink v-for="user in users" :key="user.id" class="user-item" :to="{ name: 'user-profile', params: { id: user.id } }">
        <span class="user-avatar">
          <img v-if="user.avatar_url" :src="user.avatar_url" :alt="`${user.username} 的头像`">
          <span v-else>{{ user.username.slice(0, 1).toUpperCase() }}</span>
        </span>
        <span class="user-copy">
          <strong>@{{ user.username }}</strong>
          <small>{{ user.bio || '暂无简介' }}</small>
        </span>
      </RouterLink>
    </section>
    <section v-else class="state-message">暂时没有用户</section>
  </main>
</template>

<style scoped>
.content-page {
  min-height: calc(100dvh - 64px);
  padding: 36px 16px 64px;
  background: #f3f5f4;
}

.page-header,
.user-list {
  width: min(100%, 760px);
  margin: 0 auto;
}

.page-header {
  margin-bottom: 24px;
}

.eyebrow {
  margin: 0 0 6px;
  color: var(--accent-strong);
  font-size: 0.86rem;
  font-weight: 750;
}

.page-header h1 {
  margin: 0;
  color: var(--ink-strong);
  font-size: 1.7rem;
}

.user-item {
  display: flex;
  align-items: center;
  gap: 14px;
  border-bottom: 1px solid var(--border-subtle);
  padding: 16px 0;
  color: inherit;
  text-decoration: none;
}

.user-avatar {
  display: grid;
  width: 48px;
  height: 48px;
  flex: 0 0 auto;
  place-items: center;
  overflow: hidden;
  border-radius: 50%;
  color: #ffffff;
  background: var(--accent);
  font-weight: 700;
}

.user-avatar img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.user-copy {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.user-copy strong {
  color: var(--ink-strong);
}

.user-copy small {
  overflow: hidden;
  color: var(--ink-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.state-message {
  width: min(100%, 760px);
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

@media (max-width: 640px) {
  .content-page {
    min-height: calc(100dvh - 56px);
    padding: 24px 12px 40px;
  }
}
</style>
