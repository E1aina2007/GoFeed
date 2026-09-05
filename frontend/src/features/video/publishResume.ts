// 发布页进行中记录的 sessionStorage 持久化
// 仅保存 draft ID 供恢复定位，草稿与视频的真实状态始终以服务端查询结果为准
const storageKey = 'gofeed:publishing_draft_id'

function browserStorage(): Storage | undefined {
  if (typeof window === 'undefined') {
    return undefined
  }

  try {
    const storage = window.sessionStorage
    return typeof storage.getItem === 'function'
      && typeof storage.setItem === 'function'
      && typeof storage.removeItem === 'function'
      ? storage
      : undefined
  } catch {
    return undefined
  }
}

export function readPublishingDraftID(): number | undefined {
  const storage = browserStorage()
  const stored = storage?.getItem(storageKey)
  if (!stored) {
    return undefined
  }

  const value = Number(stored)
  if (!Number.isSafeInteger(value) || value <= 0) {
    storage?.removeItem(storageKey)
    return undefined
  }
  return value
}

export function savePublishingDraftID(draftID: number): void {
  if (!Number.isSafeInteger(draftID) || draftID <= 0) {
    return
  }
  browserStorage()?.setItem(storageKey, String(draftID))
}

export function clearPublishingDraftID(): void {
  browserStorage()?.removeItem(storageKey)
}
