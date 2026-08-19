<script setup lang="ts">
import { RouterLink, RouterView, useRouter } from 'vue-router'

import { currentUser, isAuthenticated, logout } from '@/features/auth/session'

const router = useRouter()

async function handleLogout() {
  try {
    await logout()
  } finally {
    await router.push({ name: 'feed' })
  }
}
</script>

<template>
  <header class="app-header">
    <div class="app-header__inner">
      <RouterLink class="brand" to="/" aria-label="GoFeed 首页">GoFeed</RouterLink>
      <nav class="primary-nav" aria-label="主导航">
        <RouterLink class="primary-nav__link" to="/">发现</RouterLink>
        <template v-if="!isAuthenticated">
          <RouterLink class="primary-nav__link" :to="{ name: 'login' }">登录</RouterLink>
          <RouterLink class="primary-nav__link" :to="{ name: 'register' }">注册</RouterLink>
        </template>
        <span v-if="currentUser" class="current-user">{{ currentUser.username }}</span>
        <button v-if="isAuthenticated" class="logout-button" type="button" @click="handleLogout">退出</button>
      </nav>
    </div>
  </header>

  <RouterView />
</template>

<style scoped>
.app-header {
  position: sticky;
  z-index: 10;
  top: 0;
  border-bottom: 1px solid #2b3035;
  background: #111417;
}

.app-header__inner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: min(100% - 32px, 1180px);
  min-height: 64px;
  margin: 0 auto;
}

.brand {
  color: #ffffff;
  font-size: 1.2rem;
  font-weight: 750;
  text-decoration: none;
}

.primary-nav {
  display: flex;
  align-items: stretch;
  align-self: stretch;
  gap: 18px;
}

.primary-nav__link {
  display: grid;
  align-items: center;
  min-width: 52px;
  border-bottom: 2px solid transparent;
  color: #aab0b7;
  font-size: 0.92rem;
  text-decoration: none;
}

.current-user {
  display: grid;
  align-items: center;
  max-width: 12ch;
  overflow: hidden;
  color: #d6d9dd;
  font-size: 0.86rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.logout-button {
  border: 0;
  padding: 0;
  color: #aab0b7;
  background: transparent;
  font-size: 0.92rem;
  cursor: pointer;
}

.logout-button:hover {
  color: #ffffff;
}

.primary-nav__link.router-link-exact-active {
  border-bottom-color: var(--accent);
  color: #ffffff;
  font-weight: 650;
}

@media (max-width: 640px) {
  .app-header__inner {
    width: min(100% - 24px, 1180px);
    min-height: 56px;
  }

  .primary-nav {
    gap: 12px;
  }

  .current-user {
    display: none;
  }

}
</style>
