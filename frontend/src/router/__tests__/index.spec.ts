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
})
