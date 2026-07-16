// Unit tests for attachmentsStore — setAttachments / clear reducer.

import { describe, it, expect, beforeEach } from 'vitest'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import type { AttachmentInfoUI } from '@/types/models'

const A1: AttachmentInfoUI = { id: 'a1', originalName: 'report.pdf', format: 'pdf', sizeBytes: 1024 }
const A2: AttachmentInfoUI = { id: 'a2', originalName: 'image.png', format: 'png', sizeBytes: 2048 }

function resetStore() {
  useAttachmentsStore.setState({ attachments: [] })
}

describe('attachmentsStore', () => {
  beforeEach(() => {
    resetStore()
  })

  it('starts empty', () => {
    expect(useAttachmentsStore.getState().attachments).toEqual([])
  })

  it('setAttachments replaces the entire list', () => {
    useAttachmentsStore.getState().setAttachments([A1])
    expect(useAttachmentsStore.getState().attachments).toEqual([A1])

    // A second setAttachments replaces, not appends.
    useAttachmentsStore.getState().setAttachments([A2])
    expect(useAttachmentsStore.getState().attachments).toEqual([A2])
  })

  it('setAttachments replaces with a new reference (stable selector contract)', () => {
    const first = [A1]
    useAttachmentsStore.getState().setAttachments(first)
    const before = useAttachmentsStore.getState().attachments
    useAttachmentsStore.getState().setAttachments([A1]) // same contents
    const after = useAttachmentsStore.getState().attachments
    // Identity MUST change so useSyncExternalStore sees a new snapshot.
    expect(after).not.toBe(before)
  })

  it('setAttachments([]) empties the list', () => {
    useAttachmentsStore.getState().setAttachments([A1, A2])
    useAttachmentsStore.getState().setAttachments([])
    expect(useAttachmentsStore.getState().attachments).toEqual([])
  })

  it('clear empties the list', () => {
    useAttachmentsStore.getState().setAttachments([A1, A2])
    useAttachmentsStore.getState().clear()
    expect(useAttachmentsStore.getState().attachments).toEqual([])
  })
})
