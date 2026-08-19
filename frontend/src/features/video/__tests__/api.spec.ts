import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api'
import { listPublishedVideos } from '../api'

describe('listPublishedVideos', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('requests the public feed with its cursor', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      items: [],
      next_cursor: 'next-page',
    }), {
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await listPublishedVideos({ cursor: 'current-page', limit: 8 })

    expect(fetchMock).toHaveBeenCalledWith('/api/video?limit=8&cursor=current-page', expect.any(Object))
    expect(result.next_cursor).toBe('next-page')
  })

  it('exposes the server error for a failed request', async () => {
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ error: 'invalid cursor' }), {
      status: 400,
      headers: { 'content-type': 'application/json' },
    })))

    await expect(listPublishedVideos()).rejects.toEqual(new ApiError(400, 'invalid cursor'))
  })

})
