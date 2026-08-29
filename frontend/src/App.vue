<script setup lang="ts">
import { RouterLink, RouterView, useRouter } from 'vue-router'

import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ToastHost from '@/components/ToastHost.vue'
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
  <div class="app-shell">
    <aside class="app-sidebar" aria-label="主导航">
      <RouterLink class="app-brand" to="/" aria-label="GoFeed 首页">
        <strong>GoFeed</strong>
        <span>视频社区</span>
      </RouterLink>

      <nav class="sidebar-nav">
        <RouterLink class="sidebar-nav__link" to="/">发现</RouterLink>
        <RouterLink class="sidebar-nav__link" :to="{ name: 'user-list' }">用户</RouterLink>
        <RouterLink v-if="isAuthenticated" class="sidebar-nav__link" :to="{ name: 'publish' }">发布</RouterLink>
        <RouterLink v-if="isAuthenticated" class="sidebar-nav__link" :to="{ name: 'my-videos' }">我的视频</RouterLink>
      </nav>

      <div class="sidebar-footer">
        <div class="account-state">
          <span class="account-state__dot" :class="{ 'account-state__dot--online': isAuthenticated }" aria-hidden="true"></span>
          <span class="account-state__name">{{ currentUser?.username || '未登录' }}</span>
        </div>
        <div class="account-actions">
          <template v-if="isAuthenticated">
            <RouterLink class="shell-button shell-button--quiet" :to="{ name: 'account-settings' }">设置</RouterLink>
            <button class="shell-button shell-button--quiet" type="button" @click="handleLogout">退出</button>
          </template>
          <template v-else>
            <RouterLink class="shell-button shell-button--primary" :to="{ name: 'login' }">登录</RouterLink>
            <RouterLink class="shell-button shell-button--quiet" :to="{ name: 'register' }">注册</RouterLink>
          </template>
        </div>
      </div>
    </aside>

    <div class="app-main">
      <header class="app-topbar">
        <div class="app-topbar__context">
          <span class="app-topbar__eyebrow">GOFEED</span>
          <strong>发现内容</strong>
        </div>
        <RouterLink v-if="isAuthenticated" class="shell-button shell-button--primary" :to="{ name: 'publish' }">
          + 发布视频
        </RouterLink>
      </header>

      <nav class="mobile-nav" aria-label="移动端主导航">
        <RouterLink class="mobile-nav__link" to="/">发现</RouterLink>
        <RouterLink class="mobile-nav__link" :to="{ name: 'user-list' }">用户</RouterLink>
        <RouterLink v-if="isAuthenticated" class="mobile-nav__link" :to="{ name: 'publish' }">发布</RouterLink>
        <RouterLink v-if="isAuthenticated" class="mobile-nav__link" :to="{ name: 'my-videos' }">我的</RouterLink>
        <RouterLink v-else class="mobile-nav__link" :to="{ name: 'login' }">登录</RouterLink>
      </nav>

      <div class="app-content">
        <RouterView />
      </div>
    </div>

    <ToastHost />
    <ConfirmDialog />
  </div>
</template>

<style scoped>
.app-shell {
  display: grid;
  min-height: 100dvh;
  grid-template-columns: 232px minmax(0, 1fr);
  color: #edf2f0;
  background: #101415;
}

.app-sidebar {
  position: sticky;
  top: 0;
  display: flex;
  height: 100dvh;
  flex-direction: column;
  gap: 18px;
  border-right: 1px solid #293232;
  padding: 14px 12px;
  background: #151b1c;
}

.app-brand {
  display: grid;
  gap: 2px;
  border: 1px solid #254d48;
  border-radius: 8px;
  padding: 11px 12px;
  color: #f6fbf9;
  text-decoration: none;
  background: #18302d;
}

.app-brand strong {
  font-size: 1.12rem;
  line-height: 1.2;
}

.app-brand span {
  color: #93b4ae;
  font-size: 0.75rem;
}

.sidebar-nav {
  display: grid;
  gap: 7px;
}

.sidebar-nav__link,
.mobile-nav__link {
  border: 1px solid transparent;
  border-radius: 8px;
  color: #bdc9c6;
  text-decoration: none;
}

.sidebar-nav__link {
  padding: 11px 12px;
}

.sidebar-nav__link:hover,
.sidebar-nav__link.router-link-active {
  border-color: #32675f;
  color: #f4faf8;
  background: #1b3935;
}

.sidebar-footer {
  display: grid;
  gap: 12px;
  margin-top: auto;
  border-top: 1px solid #293232;
  padding-top: 14px;
}

.account-state {
  display: flex;
  align-items: center;
  gap: 9px;
  min-width: 0;
}

.account-state__dot {
  width: 9px;
  height: 9px;
  flex: 0 0 auto;
  border-radius: 50%;
  background: #9b6057;
  box-shadow: 0 0 0 3px #9b605726;
}

.account-state__dot--online {
  background: #35b795;
  box-shadow: 0 0 0 3px #35b7952b;
}

.account-state__name {
  overflow: hidden;
  color: #d7e2df;
  font-size: 0.84rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.account-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.shell-button {
  display: inline-flex;
  min-height: 36px;
  align-items: center;
  justify-content: center;
  border: 1px solid #35413f;
  border-radius: 8px;
  padding: 8px 11px;
  color: #e5efec;
  font-size: 0.82rem;
  text-decoration: none;
  background: #202827;
  cursor: pointer;
}

.shell-button:hover {
  background: #293332;
}

.shell-button--primary {
  border-color: #2e8e7c;
  color: #f5fffc;
  background: #176557;
}

.shell-button--primary:hover {
  background: #1d7968;
}

.app-main {
  display: flex;
  height: 100dvh;
  min-width: 0;
  flex-direction: column;
}

.app-topbar {
  display: flex;
  min-height: 60px;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  border-bottom: 1px solid #293232;
  padding: 0 18px;
  background: #151b1c;
}

.app-topbar__context {
  display: grid;
  gap: 2px;
}

.app-topbar__eyebrow {
  color: #7fa39c;
  font-size: 0.68rem;
  font-weight: 750;
  letter-spacing: 0.12em;
}

.app-topbar__context strong {
  color: #f1f7f5;
  font-size: 0.98rem;
}

.app-content {
  min-height: 0;
  flex: 1;
  overflow: auto;
}

.mobile-nav {
  display: none;
}

@media (max-width: 900px) {
  .app-shell {
    display: block;
  }

  .app-sidebar {
    display: none;
  }

  .app-main {
    height: 100dvh;
  }

  .app-topbar {
    min-height: 56px;
    padding: 0 12px;
  }

  .mobile-nav {
    display: grid;
    min-height: 46px;
    grid-auto-columns: 1fr;
    grid-auto-flow: column;
    border-bottom: 1px solid #293232;
    background: #151b1c;
  }

  .mobile-nav__link {
    display: grid;
    min-height: 46px;
    place-items: center;
    border-width: 0 0 2px;
    border-radius: 0;
    font-size: 0.82rem;
  }

  .mobile-nav__link.router-link-active {
    border-bottom-color: #35b795;
    color: #f4faf8;
  }
}
</style>
