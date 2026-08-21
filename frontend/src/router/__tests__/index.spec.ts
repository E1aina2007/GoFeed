import { afterEach, describe, expect, it } from 'vitest'

import router from '../index'
import { clearSession } from '@/features/auth/session'

describe('router session boundaries', () => {
  afterEach(async () => {
    clearSession()
    await router.push({ name: 'feed' })
  })

  it('keeps the public feed accessible without a session', async () => {
    clearSession()

    await router.push({ name: 'feed' })

    expect(router.currentRoute.value.name).toBe('feed')
  })

  it('redirects anonymous users from publishing to login with a return path', async () => {
    clearSession()

    await router.push({ name: 'publish' })

    expect(router.currentRoute.value.name).toBe('login')
    expect(router.currentRoute.value.query.redirect).toBe('/publish')
  })

  it('protects personal videos and account settings while keeping public profiles open', async () => {
    clearSession()

    await router.push({ name: 'my-videos' })
    expect(router.currentRoute.value.name).toBe('login')
    expect(router.currentRoute.value.query.redirect).toBe('/mine')

    await router.push({ name: 'user-profile', params: { id: 42 } })
    expect(router.currentRoute.value.name).toBe('user-profile')
  })
})
