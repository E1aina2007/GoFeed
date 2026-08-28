import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import type { Router } from 'vue-router'

import {
  createDraft,
  discardDraft,
  getDraft,
  publishDraft,
  uploadCover,
  uploadVideo,
  type DraftItem,
} from '@/features/video/api'
import { ApiError } from '@/lib/api'
import PublishVideoView from '../PublishVideoView.vue'

const routerReplace = vi.hoisted(() => vi.fn<Router['replace']>())

vi.mock('vue-router', () => ({
  useRouter: () => ({ replace: routerReplace }),
}))

vi.mock('@/features/video/api', () => ({
  createDraft: vi.fn<typeof createDraft>(),
  discardDraft: vi.fn<typeof discardDraft>(),
  getDraft: vi.fn<typeof getDraft>(),
  publishDraft: vi.fn<typeof publishDraft>(),
  uploadCover: vi.fn<typeof uploadCover>(),
  uploadVideo: vi.fn<typeof uploadVideo>(),
}))

function draft(overrides: Partial<DraftItem> = {}): DraftItem {
  return {
    id: 7,
    title: '春日散步',
    description: '',
    status: 'draft',
    has_video: false,
    has_cover: false,
    created_at: '2026-08-22T08:00:00Z',
    updated_at: '2026-08-22T08:00:00Z',
    ...overrides,
  }
}

function publishedVideo(id = 100) {
  return {
    id,
    title: '春日散步',
    description: '',
    play_url: '/static/videos/42/clip.mp4',
    play_file_name: 'clip.mp4',
    play_original_name: '服务端视频.mp4',
    cover_url: '/static/covers/42/cover.png',
    cover_file_name: 'cover.png',
    cover_original_name: '服务端封面.png',
    published_at: '2026-08-22T08:01:00Z',
    likes_count: 0,
    comments_count: 0,
    author: { id: 42, username: 'alice', avatar_url: '' },
  }
}

function abortableUpload<T>(signal: AbortSignal | undefined) {
  return new Promise<T>((_, reject) => {
    const rejectAbort = () => {
      const error = new Error('上传已取消')
      error.name = 'AbortError'
      reject(error)
    }
    if (!signal) {
      return
    }
    if (signal.aborted) {
      rejectAbort()
      return
    }
    signal.addEventListener('abort', rejectAbort, { once: true })
  })
}

async function mountWithSelectedMedia() {
  const wrapper = mount(PublishVideoView, {
    global: { plugins: [createPinia()], stubs: { RouterLink: true } },
  })
  const fields = wrapper.findAll('input')
  const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
  const cover = new File(['cover'], 'local.png', { type: 'image/png' })
  await fields[0]?.setValue('春日散步')
  Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
  Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
  await fields[1]?.trigger('change')
  await fields[2]?.trigger('change')

  return { wrapper, video, cover }
}

const uploadFailureCases: Array<[string, ApiError, string]> = [
  [
    'authentication',
    new ApiError(401, 'invalid or expired token'),
    '登录状态已失效，请重新登录后重试',
  ],
  [
    'validation',
    new ApiError(400, 'invalid media file'),
    '视频文件未通过服务端校验，请检查文件格式和内容后重试',
  ],
  [
    'size validation',
    new ApiError(400, 'media file too large'),
    '视频文件超过 200 MiB，请重新选择',
  ],
  ['size', new ApiError(413, 'media file too large'), '视频文件超过 200 MiB，请重新选择'],
  ['server', new ApiError(500, 'video operation failed'), '服务暂时不可用，草稿已保留，请稍后重试'],
]

describe('PublishVideoView', () => {
  beforeEach(() => {
    vi.resetAllMocks()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('requires a title before starting an upload', async () => {
    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })

    await wrapper.get('form').trigger('submit')

    expect(wrapper.get('[role="alert"]').text()).toBe('请填写视频标题')
  })

  it('requires both media files after the title is provided', async () => {
    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    await wrapper.get('input').setValue('春日散步')

    await wrapper.get('form').trigger('submit')

    expect(wrapper.get('[role="alert"]').text()).toBe('请选择一个视频文件')
  })

  it('renders local media previews and releases their object URLs when replaced or unmounted', async () => {
    const createObjectURL = vi
      .fn<typeof URL.createObjectURL>()
      .mockReturnValueOnce('blob:video-first')
      .mockReturnValueOnce('blob:cover-first')
      .mockReturnValueOnce('blob:video-second')
      .mockReturnValueOnce('blob:cover-second')
    const revokeObjectURL = vi.fn<typeof URL.revokeObjectURL>()
    vi.stubGlobal('URL', { createObjectURL, revokeObjectURL })

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const firstVideo = new File(['video'], 'first.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'cover.png', { type: 'image/png' })
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [firstVideo] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')

    expect(wrapper.get('video.media-preview').attributes('src')).toBe('blob:video-first')
    expect(wrapper.get('img.media-preview').attributes('src')).toBe('blob:cover-first')

    const secondVideo = new File(['video'], 'second.mp4', { type: 'video/mp4' })
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [secondVideo] })
    await fields[1]?.trigger('change')

    expect(revokeObjectURL).toHaveBeenCalledWith('blob:video-first')
    expect(wrapper.get('video.media-preview').attributes('src')).toBe('blob:video-second')

    const secondCover = new File(['cover'], 'second-cover.png', { type: 'image/png' })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [secondCover] })
    await fields[2]?.trigger('change')

    expect(revokeObjectURL).toHaveBeenCalledWith('blob:cover-first')
    expect(wrapper.get('img.media-preview').attributes('src')).toBe('blob:cover-second')

    wrapper.unmount()

    expect(revokeObjectURL).toHaveBeenCalledWith('blob:video-second')
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:cover-second')
  })

  it('rejects invalid media selection before creating a preview', async () => {
    const createObjectURL = vi.fn<typeof URL.createObjectURL>()
    const revokeObjectURL = vi.fn<typeof URL.revokeObjectURL>()
    vi.stubGlobal('URL', { createObjectURL, revokeObjectURL })
    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fileInput = wrapper.findAll('input')[1]
    if (!fileInput) {
      throw new Error('video file input was not rendered')
    }
    const file = new File(['not a video'], 'notes.txt', { type: 'text/plain' })
    Object.defineProperty(fileInput.element, 'files', { configurable: true, value: [file] })

    await fileInput.trigger('change')

    expect(wrapper.get('[role="alert"]').text()).toBe(
      '视频仅支持不超过 200 MiB 的 MP4、WebM 或 MOV 文件',
    )
    expect(wrapper.find('video.media-preview').exists()).toBe(false)
    expect(createObjectURL).not.toHaveBeenCalled()
  })

  it('creates a draft, binds both media files, then publishes it', async () => {
    vi.mocked(createDraft).mockResolvedValue({
      draft: draft(),
    })
    vi.mocked(uploadVideo).mockResolvedValue({
      draft_id: 7,
      play_url: '/static/videos/42/clip.mp4',
      play_file_name: 'clip.mp4',
      play_original_name: '服务端视频.mp4',
    })
    vi.mocked(uploadCover).mockResolvedValue({
      draft_id: 7,
      cover_url: '/static/covers/42/cover.png',
      cover_file_name: 'cover.png',
      cover_original_name: '服务端封面.png',
    })
    vi.mocked(publishDraft).mockResolvedValue({
      video: publishedVideo(),
    })

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'local.png', { type: 'image/png' })
    await fields[0]?.setValue('春日散步')
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createDraft).toHaveBeenCalledWith({ title: '春日散步', description: '' })
    expect(uploadVideo).toHaveBeenCalledWith(
      7,
      video,
      expect.any(Function),
      expect.any(AbortSignal),
    )
    expect(uploadCover).toHaveBeenCalledWith(
      7,
      cover,
      expect.any(Function),
      expect.any(AbortSignal),
    )
    expect(publishDraft).toHaveBeenCalledWith(7)
    expect(routerReplace).toHaveBeenCalledWith({ name: 'feed', query: { published: '100' } })
  })

  it.each(uploadFailureCases)(
    'shows a recoverable %s upload error',
    async (_category, uploadError, expectedMessage) => {
      vi.mocked(createDraft).mockResolvedValue({ draft: draft() })
      vi.mocked(uploadVideo).mockRejectedValue(uploadError)
      const { wrapper } = await mountWithSelectedMedia()

      await wrapper.get('form').trigger('submit')
      await flushPromises()

      expect(wrapper.get('[role="alert"]').text()).toBe(expectedMessage)
      expect(uploadCover).not.toHaveBeenCalled()
      expect(publishDraft).not.toHaveBeenCalled()
    },
  )

  it('cancels a video upload and keeps server-bound media on the draft', async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft() })
    let videoSignal: AbortSignal | undefined
    vi.mocked(uploadVideo).mockImplementation((_draftID, _file, _onProgress, signal) => {
      videoSignal = signal
      return abortableUpload(signal)
    })
    vi.mocked(getDraft).mockResolvedValue({
      draft: draft({ has_video: true, play_original_name: '服务端视频.mp4' }),
    })

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'local.png', { type: 'image/png' })
    await fields[0]?.setValue('春日散步')
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')

    await wrapper.get('form').trigger('submit')
    await vi.waitFor(() => expect(uploadVideo).toHaveBeenCalledTimes(1))
    await wrapper.get('.cancel-upload-action').trigger('click')
    await flushPromises()

    expect(videoSignal?.aborted).toBe(true)
    expect(getDraft).toHaveBeenCalledWith(7)
    expect(uploadCover).not.toHaveBeenCalled()
    expect(publishDraft).not.toHaveBeenCalled()
    expect(wrapper.find('.draft-actions').exists()).toBe(true)
    expect(wrapper.find('.cancel-upload-action').exists()).toBe(false)
  })

  it('cancels a cover upload without publishing the draft', async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft() })
    vi.mocked(uploadVideo).mockResolvedValue({
      draft_id: 7,
      play_url: '/static/videos/42/clip.mp4',
      play_file_name: 'clip.mp4',
      play_original_name: '服务端视频.mp4',
    })
    let coverSignal: AbortSignal | undefined
    vi.mocked(uploadCover).mockImplementation((_draftID, _file, _onProgress, signal) => {
      coverSignal = signal
      return abortableUpload(signal)
    })
    vi.mocked(getDraft).mockResolvedValue({
      draft: draft({ has_video: true, play_original_name: '服务端视频.mp4' }),
    })

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'local.png', { type: 'image/png' })
    await fields[0]?.setValue('春日散步')
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')

    await wrapper.get('form').trigger('submit')
    await vi.waitFor(() => expect(uploadCover).toHaveBeenCalledTimes(1))
    await wrapper.get('.cancel-upload-action').trigger('click')
    await flushPromises()

    expect(coverSignal?.aborted).toBe(true)
    expect(getDraft).toHaveBeenCalledWith(7)
    expect(publishDraft).not.toHaveBeenCalled()
  })

  it('keeps a canceled missing upload retryable', async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft() })
    let videoSignal: AbortSignal | undefined
    vi.mocked(uploadVideo).mockImplementationOnce((_draftID, _file, _onProgress, signal) => {
      videoSignal = signal
      return abortableUpload(signal)
    })
    vi.mocked(getDraft).mockResolvedValue({ draft: draft() })

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'local.png', { type: 'image/png' })
    await fields[0]?.setValue('春日散步')
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')

    await wrapper.get('form').trigger('submit')
    await vi.waitFor(() => expect(uploadVideo).toHaveBeenCalledTimes(1))
    await wrapper.get('.cancel-upload-action').trigger('click')
    await flushPromises()

    expect(videoSignal?.aborted).toBe(true)
    expect(getDraft).toHaveBeenCalledWith(7)
    expect(uploadCover).not.toHaveBeenCalled()
    expect(publishDraft).not.toHaveBeenCalled()

    vi.mocked(uploadVideo).mockResolvedValue({
      draft_id: 7,
      play_url: '/static/videos/42/clip.mp4',
      play_file_name: 'clip.mp4',
      play_original_name: '服务端视频.mp4',
    })
    vi.mocked(uploadCover).mockResolvedValue({
      draft_id: 7,
      cover_url: '/static/covers/42/cover.png',
      cover_file_name: 'cover.png',
      cover_original_name: '服务端封面.png',
    })
    vi.mocked(publishDraft).mockResolvedValue({ video: publishedVideo() })

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(uploadVideo).toHaveBeenCalledTimes(2)
    expect(uploadCover).toHaveBeenCalledTimes(1)
    expect(publishDraft).toHaveBeenCalledWith(7)
  })

  it('does not continue after cancellation when the draft is purging', async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft() })
    vi.mocked(uploadVideo).mockImplementation((_draftID, _file, _onProgress, signal) =>
      abortableUpload(signal),
    )
    vi.mocked(getDraft).mockResolvedValue({ draft: draft({ status: 'purging' }) })

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'local.png', { type: 'image/png' })
    await fields[0]?.setValue('春日散步')
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')

    await wrapper.get('form').trigger('submit')
    await vi.waitFor(() => expect(uploadVideo).toHaveBeenCalledTimes(1))
    await wrapper.get('.cancel-upload-action').trigger('click')
    await flushPromises()

    expect(uploadCover).not.toHaveBeenCalled()
    expect(publishDraft).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toBe('草稿已进入清扫，无法继续上传')
  })

  it('reconciles a lost video upload response from the server draft state', async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft() })
    vi.mocked(uploadVideo).mockRejectedValue(new ApiError(0, '网络连接失败，请检查网络后重试'))
    vi.mocked(getDraft).mockResolvedValue({
      draft: draft({
        has_video: true,
        play_original_name: '服务端视频.mp4',
        updated_at: '2026-08-22T08:01:00Z',
      }),
    })
    vi.mocked(uploadCover).mockResolvedValue({
      draft_id: 7,
      cover_url: '/static/covers/42/cover.png',
      cover_file_name: 'cover.png',
      cover_original_name: '服务端封面.png',
    })
    vi.mocked(publishDraft).mockResolvedValue({ video: publishedVideo() })

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'local.png', { type: 'image/png' })
    await fields[0]?.setValue('春日散步')
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(uploadVideo).toHaveBeenCalledTimes(1)
    expect(getDraft).toHaveBeenCalledWith(7)
    expect(uploadCover).toHaveBeenCalledWith(
      7,
      cover,
      expect.any(Function),
      expect.any(AbortSignal),
    )
    expect(publishDraft).toHaveBeenCalledWith(7)
    expect(routerReplace).toHaveBeenCalledWith({ name: 'feed', query: { published: '100' } })
  })

  it('keeps the upload error when the server confirms media is still missing', async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft() })
    vi.mocked(uploadVideo).mockRejectedValue(new ApiError(0, '网络连接失败，请检查网络后重试'))
    vi.mocked(getDraft).mockResolvedValue({ draft: draft() })

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'local.png', { type: 'image/png' })
    await fields[0]?.setValue('春日散步')
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(getDraft).toHaveBeenCalledWith(7)
    expect(uploadCover).not.toHaveBeenCalled()
    expect(publishDraft).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toBe('网络连接失败，请检查网络后重试')
  })

  it('stops when the server reports that the draft is already purging', async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft() })
    vi.mocked(uploadVideo).mockRejectedValue(new ApiError(0, '网络连接失败，请检查网络后重试'))
    vi.mocked(getDraft).mockResolvedValue({
      draft: draft({ status: 'purging', has_video: true }),
    })

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'local.png', { type: 'image/png' })
    await fields[0]?.setValue('春日散步')
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(uploadCover).not.toHaveBeenCalled()
    expect(publishDraft).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toBe('草稿已进入清扫，无法继续上传')
  })

  it('discards the active draft before leaving the publishing page', async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft() })
    vi.mocked(uploadVideo).mockRejectedValue(new ApiError(0, '网络连接失败，请检查网络后重试'))
    vi.mocked(getDraft).mockResolvedValue({ draft: draft() })
    vi.mocked(discardDraft).mockResolvedValue({ draft: draft({ status: 'purging' }) })
    vi.spyOn(window, 'confirm').mockReturnValue(true)

    const wrapper = mount(PublishVideoView, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true } },
    })
    const fields = wrapper.findAll('input')
    const video = new File(['video'], 'local.mp4', { type: 'video/mp4' })
    const cover = new File(['cover'], 'local.png', { type: 'image/png' })
    await fields[0]?.setValue('春日散步')
    Object.defineProperty(fields[1]?.element, 'files', { configurable: true, value: [video] })
    Object.defineProperty(fields[2]?.element, 'files', { configurable: true, value: [cover] })
    await fields[1]?.trigger('change')
    await fields[2]?.trigger('change')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    await wrapper.get('.cancel-link').trigger('click')
    await flushPromises()

    expect(discardDraft).toHaveBeenCalledWith(7)
    expect(routerReplace).toHaveBeenCalledWith({ name: 'feed' })
    expect(wrapper.find('.draft-actions').exists()).toBe(false)
  })
})
