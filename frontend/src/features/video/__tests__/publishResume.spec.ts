import { beforeEach, describe, expect, it } from 'vitest'

import {
  clearPublishingDraftID,
  readPublishingDraftID,
  savePublishingDraftID,
} from '../publishResume'

describe('publishResume storage', () => {
  beforeEach(() => {
    window.sessionStorage.clear()
  })

  it('round-trips a saved draft id', () => {
    expect(readPublishingDraftID()).toBeUndefined()

    savePublishingDraftID(42)

    expect(readPublishingDraftID()).toBe(42)
  })

  it('ignores invalid draft ids when saving', () => {
    savePublishingDraftID(0)
    savePublishingDraftID(-3)
    savePublishingDraftID(Number.NaN)

    expect(readPublishingDraftID()).toBeUndefined()
  })

  it('drops malformed stored values instead of returning them', () => {
    window.sessionStorage.setItem('gofeed:publishing_draft_id', 'not-a-number')

    expect(readPublishingDraftID()).toBeUndefined()
    expect(window.sessionStorage.getItem('gofeed:publishing_draft_id')).toBeNull()
  })

  it('drops non-positive or fractional stored values', () => {
    window.sessionStorage.setItem('gofeed:publishing_draft_id', '0')

    expect(readPublishingDraftID()).toBeUndefined()
    expect(window.sessionStorage.getItem('gofeed:publishing_draft_id')).toBeNull()

    window.sessionStorage.setItem('gofeed:publishing_draft_id', '4.5')

    expect(readPublishingDraftID()).toBeUndefined()
  })

  it('removes the stored id on clear', () => {
    savePublishingDraftID(9)

    clearPublishingDraftID()

    expect(readPublishingDraftID()).toBeUndefined()
    expect(window.sessionStorage.getItem('gofeed:publishing_draft_id')).toBeNull()
  })
})
