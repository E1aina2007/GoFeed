import { createRouter, createWebHistory } from 'vue-router'

import { isAuthenticated } from '@/features/auth/session'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/',
      name: 'feed',
      component: () => import('@/views/FeedView.vue'),
    },
    {
      path: '/login',
      name: 'login',
      component: () => import('@/views/LoginView.vue'),
    },
    {
      path: '/register',
      name: 'register',
      component: () => import('@/views/RegisterView.vue'),
    },
    {
      path: '/publish',
      name: 'publish',
      component: () => import('@/views/PublishVideoView.vue'),
      meta: { requiresAuth: true },
    },
    {
      path: '/video/:id',
      name: 'video-detail',
      component: () => import('@/views/VideoDetailView.vue'),
    },
    {
      path: '/users',
      name: 'user-list',
      component: () => import('@/views/UserListView.vue'),
    },
    {
      path: '/users/:id',
      name: 'user-profile',
      component: () => import('@/views/UserProfileView.vue'),
    },
    {
      path: '/mine',
      name: 'my-videos',
      component: () => import('@/views/MyVideosView.vue'),
      meta: { requiresAuth: true },
    },
    {
      path: '/settings',
      name: 'account-settings',
      component: () => import('@/views/AccountSettingsView.vue'),
      meta: { requiresAuth: true },
    },
  ],
})

router.beforeEach((to) => {
  if (to.meta.requiresAuth && !isAuthenticated.value) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  return true
})

export default router
