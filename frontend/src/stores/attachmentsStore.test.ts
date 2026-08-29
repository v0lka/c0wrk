// Unit tests for attachmentsStore — keyed setAttachments / setImageError
// reducers, dropSessions, and the id→name accumulation cache used to resolve
// read_attachment tool cards.

import { describe, it, expect, beforeEach } from 'vitest'
import { useAttachmentsStore, EMPTY_ATTACHMENTS } from '@/stores/attachmentsStore'
import type { AttachmentInfoUI, AttachmentUploadUI } from '@/types/models'

const A1: AttachmentInfoUI = { id: 'a1', originalName: 'report.pdf', format: 'pdf', sizeBytes: 1024 }
const A2: AttachmentInfoUI = { id: 'a2', originalName: 'image.png', format: 'png', sizeBytes: 2048 }

const U1: AttachmentUploadUI = { id: 'u1', fileName: 'notes.md', path: '/p/notes.md', isImage: false }
const U2: AttachmentUploadUI = { id: 'u2', fileName: 'photo.png', path: '/p/photo.png', isImage: true }

function resetStore() {
  useAttachmentsStore.setState({ attachmentsBySession: {}, uploadsBySession: {}, namesById: {}, imageErrorBySession: {} })
}

describe('attachmentsStore', () => {
  beforeEach(() => {
    resetStore()
  })

  it('starts empty', () => {
    expect(useAttachmentsStore.getState().attachmentsBySession).toEqual({})
  })

  it('setAttachments writes the session slice and keeps sessions isolated', () => {
    useAttachmentsStore.getState().setAttachments('sess-a', [A1])
    expect(useAttachmentsStore.getState().attachmentsBySession['sess-a']).toEqual([A1])
    // Another session's slice is untouched.
    expect(useAttachmentsStore.getState().attachmentsBySession['sess-b']).toBeUndefined()

    // A second setAttachments replaces, not appends.
    useAttachmentsStore.getState().setAttachments('sess-a', [A2])
    expect(useAttachmentsStore.getState().attachmentsBySession['sess-a']).toEqual([A2])
  })

  it('setAttachments replaces with a new reference (stable selector contract)', () => {
    const first = [A1]
    useAttachmentsStore.getState().setAttachments('sess-a', first)
    const before = useAttachmentsStore.getState().attachmentsBySession['sess-a']
    useAttachmentsStore.getState().setAttachments('sess-a', [A1]) // same contents
    const after = useAttachmentsStore.getState().attachmentsBySession['sess-a']
    // Identity MUST change so useSyncExternalStore sees a new snapshot.
    expect(after).not.toBe(before)
  })

  it('setAttachments with the same array reference is a no-op (stable state)', () => {
    const list = [A1]
    useAttachmentsStore.getState().setAttachments('sess-a', list)
    const before = useAttachmentsStore.getState()
    useAttachmentsStore.getState().setAttachments('sess-a', list)
    expect(useAttachmentsStore.getState()).toBe(before)
  })

  it('setAttachments(sid, []) drops the key (send-flush keeps the record sparse)', () => {
    useAttachmentsStore.getState().setAttachments('sess-a', [A1, A2])
    useAttachmentsStore.getState().setAttachments('sess-a', [])
    expect(useAttachmentsStore.getState().attachmentsBySession['sess-a']).toBeUndefined()
    // Setting empty on an absent key is a no-op.
    const before = useAttachmentsStore.getState()
    useAttachmentsStore.getState().setAttachments('sess-nope', [])
    expect(useAttachmentsStore.getState()).toBe(before)
  })

  it('EMPTY_ATTACHMENTS is a stable module constant distinct from stored slices', () => {
    useAttachmentsStore.getState().setAttachments('sess-a', [A1])
    expect(useAttachmentsStore.getState().attachmentsBySession['sess-a']).not.toBe(EMPTY_ATTACHMENTS)
  })

  describe('namesById accumulation', () => {
    it('setAttachments folds attachment names into namesById', () => {
      useAttachmentsStore.getState().setAttachments('sess-a', [A1, A2])
      expect(useAttachmentsStore.getState().namesById).toEqual({
        a1: 'report.pdf',
        a2: 'image.png',
      })
    })

    it('survives the send-flush empty list (committed attachments stay resolvable)', () => {
      useAttachmentsStore.getState().setAttachments('sess-a', [A1, A2])
      useAttachmentsStore.getState().setAttachments('sess-a', [])
      expect(useAttachmentsStore.getState().attachmentsBySession['sess-a']).toBeUndefined()
      // namesById must NOT be cleared by an empty list.
      expect(useAttachmentsStore.getState().namesById).toEqual({
        a1: 'report.pdf',
        a2: 'image.png',
      })
    })

    it('accumulates across successive non-empty lists', () => {
      useAttachmentsStore.getState().setAttachments('sess-a', [A1])
      useAttachmentsStore.getState().setAttachments('sess-a', [A2])
      expect(useAttachmentsStore.getState().namesById).toEqual({
        a1: 'report.pdf',
        a2: 'image.png',
      })
    })

    it('accumulates across sessions (cross-session name resolution)', () => {
      useAttachmentsStore.getState().setAttachments('sess-a', [A1])
      useAttachmentsStore.getState().setAttachments('sess-b', [A2])
      expect(useAttachmentsStore.getState().namesById).toEqual({
        a1: 'report.pdf',
        a2: 'image.png',
      })
    })
  })

  describe('imageErrorBySession', () => {
    it('starts absent (no banner)', () => {
      expect(useAttachmentsStore.getState().imageErrorBySession).toEqual({})
    })

    it('setImageError sets the message per session and isolates sessions', () => {
      useAttachmentsStore.getState().setImageError('sess-a', 'Model does not support images')
      expect(useAttachmentsStore.getState().imageErrorBySession['sess-a']).toBe(
        'Model does not support images',
      )
      expect(useAttachmentsStore.getState().imageErrorBySession['sess-b']).toBeUndefined()
    })

    it('setImageError(null) clears the message and keeps the record sparse', () => {
      useAttachmentsStore.getState().setImageError('sess-a', 'Model does not support images')
      useAttachmentsStore.getState().setImageError('sess-a', null)
      expect('sess-a' in useAttachmentsStore.getState().imageErrorBySession).toBe(false)
    })

    it('setting the same message again is a no-op (stable state reference)', () => {
      useAttachmentsStore.getState().setImageError('sess-a', 'msg')
      const before = useAttachmentsStore.getState()
      useAttachmentsStore.getState().setImageError('sess-a', 'msg')
      expect(useAttachmentsStore.getState()).toBe(before)
    })

    it('clearing an absent key is a no-op (stable state reference)', () => {
      const before = useAttachmentsStore.getState()
      useAttachmentsStore.getState().setImageError('sess-nope', null)
      expect(useAttachmentsStore.getState()).toBe(before)
    })
  })

  describe('dropSessions', () => {
    it('drops only the listed sessions from both keyed maps', () => {
      const { setAttachments, setImageError, dropSessions } = useAttachmentsStore.getState()
      setAttachments('sess-a', [A1])
      setAttachments('sess-b', [A2])
      setImageError('sess-a', 'no vision')
      setImageError('sess-b', 'no vision')

      dropSessions(['sess-a'])

      const s = useAttachmentsStore.getState()
      expect(s.attachmentsBySession['sess-a']).toBeUndefined()
      expect(s.imageErrorBySession['sess-a']).toBeUndefined()
      expect(s.attachmentsBySession['sess-b']).toEqual([A2])
      expect(s.imageErrorBySession['sess-b']).toBe('no vision')
    })

    it('is a no-op for unknown ids (stable reference)', () => {
      useAttachmentsStore.getState().setAttachments('sess-a', [A1])
      const before = useAttachmentsStore.getState()
      useAttachmentsStore.getState().dropSessions(['sess-nope'])
      expect(useAttachmentsStore.getState()).toBe(before)
    })

    it('keeps namesById (committed names stay resolvable for tool cards)', () => {
      useAttachmentsStore.getState().setAttachments('sess-a', [A1])
      useAttachmentsStore.getState().dropSessions(['sess-a'])
      expect(useAttachmentsStore.getState().namesById).toEqual({ a1: 'report.pdf' })
    })

    it('drops in-flight upload placeholders with the session slice', () => {
      const { beginUploads, dropSessions } = useAttachmentsStore.getState()
      beginUploads('sess-a', [U1])
      beginUploads('sess-b', [U2])

      dropSessions(['sess-a'])

      const s = useAttachmentsStore.getState()
      expect(s.uploadsBySession['sess-a']).toBeUndefined()
      expect(s.uploadsBySession['sess-b']).toEqual([U2])
    })
  })

  describe('uploadsBySession (optimistic placeholders)', () => {
    it('begins empty and beginUploads creates the slice on demand', () => {
      expect(useAttachmentsStore.getState().uploadsBySession).toEqual({})
      useAttachmentsStore.getState().beginUploads('sess-a', [U1])
      expect(useAttachmentsStore.getState().uploadsBySession['sess-a']).toEqual([U1])
    })

    it('beginUploads appends to an existing slice (batched staging)', () => {
      const { beginUploads } = useAttachmentsStore.getState()
      beginUploads('sess-a', [U1])
      beginUploads('sess-a', [U2])
      expect(useAttachmentsStore.getState().uploadsBySession['sess-a']).toEqual([U1, U2])
    })

    it('beginUploads with an empty list is a no-op', () => {
      const before = useAttachmentsStore.getState()
      useAttachmentsStore.getState().beginUploads('sess-a', [])
      expect(useAttachmentsStore.getState()).toBe(before)
    })

    it('endUploads removes the listed ids and drops the key when emptied', () => {
      const { beginUploads, endUploads } = useAttachmentsStore.getState()
      beginUploads('sess-a', [U1, U2])

      endUploads('sess-a', [U1.id])
      expect(useAttachmentsStore.getState().uploadsBySession['sess-a']).toEqual([U2])

      endUploads('sess-a', [U2.id])
      expect(useAttachmentsStore.getState().uploadsBySession['sess-a']).toBeUndefined()
    })

    it('endUploads with unknown ids or an absent slice is a stable no-op', () => {
      const { beginUploads, endUploads } = useAttachmentsStore.getState()
      beginUploads('sess-a', [U1])
      const before = useAttachmentsStore.getState()
      endUploads('sess-a', ['nope'])
      endUploads('sess-nope', [U1.id])
      expect(useAttachmentsStore.getState()).toBe(before)
    })
  })
})
