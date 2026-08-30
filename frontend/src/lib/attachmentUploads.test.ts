// Unit tests for the optimistic attachment-upload lifecycle: placeholder
// begin/complete/fail, ID-window claiming of landed records (incremental
// one-by-one retirement), cancellation (immediate chip removal + backend
// removal of the claimed id), and the event-side filter that keeps cancelled
// attachments from flashing back via the backend's incremental
// `attachments:changed` events.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import {
  beginAttachmentUploads,
  beginAttachmentUploadsFresh,
  cancelAttachmentUpload,
  collectPasteUploadDescriptors,
  completeAttachmentUploads,
  failAttachmentUploads,
  processIncomingAttachments,
  resetAttachmentUploadState,
} from '@/lib/attachmentUploads'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import type { AttachmentInfoUI } from '@/types/models'

vi.mock('@/api/attachments', () => ({
  removeAttachment: spies.removeAttachment,
  getAttachments: spies.getAttachments,
  // Pure helper used by collectPasteUploadDescriptors; the real one lives in
  // the mocked module's source — a minimal stand-in keeps this factory
  // self-contained.
  isImagePath: (path: string) => /\.(png|jpe?g|gif|webp)$/i.test(path),
}))

const spies = vi.hoisted(() => ({
  removeAttachment: vi.fn<(sessionId: string, attachmentId: string) => Promise<void>>(),
  getAttachments: vi.fn<(sessionId: string) => Promise<AttachmentInfoUI[]>>(),
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

const DOC_A: AttachmentInfoUI = { id: 'd1', originalName: 'a.md', format: 'md', sizeBytes: 1 }
const DOC_B: AttachmentInfoUI = { id: 'd2', originalName: 'b.md', format: 'md', sizeBytes: 1 }
/** Landed image record as the backend really shapes it: OriginalName is the
 *  source basename and `path` points at the PROCESSED copy — neither ever
 *  equals the upload's source path. */
const IMG_LANDED: AttachmentInfoUI = {
  id: 'i1',
  originalName: 'IMG_2024.heic',
  format: 'png',
  sizeBytes: 2,
  isImage: true,
  path: '/agent/sessions/s1/images/uuid.jpg',
}

function resetState() {
  useAttachmentsStore.setState({
    attachmentsBySession: {},
    uploadsBySession: {},
    namesById: {},
    imageErrorBySession: {},
  })
  resetAttachmentUploadState()
  spies.removeAttachment.mockReset().mockResolvedValue(undefined)
  spies.getAttachments.mockReset().mockResolvedValue([])
}

/** First element with a runtime guarantee (noUncheckedIndexedAccess-friendly). */
function first<T>(list: readonly T[]): T {
  if (list.length === 0) throw new Error('expected a non-empty list')
  return list[0] as T
}

/** Flush pending microtasks (removal RPC settlements, retire callbacks). */
async function flushMicrotasks(): Promise<void> {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

describe('attachmentUploads optimistic lifecycle', () => {
  beforeEach(() => {
    resetState()
  })

  it('begin registers spinner placeholders in the store and returns them', () => {
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
      { path: '/p/b.md', fileName: 'b.md', isImage: false },
    ])

    expect(uploads).toHaveLength(2)
    expect(useAttachmentsStore.getState().uploadsBySession['sess-1']).toEqual(uploads)
    expect(uploads.map((u) => u.fileName)).toEqual(['a.md', 'b.md'])
  })

  it('complete drains placeholders and returns the incoming list unchanged (no cancels)', () => {
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
    ])

    const kept = completeAttachmentUploads('sess-1', uploads, [DOC_A, DOC_B])

    expect(kept).toEqual([DOC_A, DOC_B])
    expect(useAttachmentsStore.getState().uploadsBySession['sess-1']).toBeUndefined()
    expect(spies.removeAttachment).not.toHaveBeenCalled()
  })

  it('fail drains placeholders without touching the staged list', () => {
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
    ])

    failAttachmentUploads(uploads)

    expect(useAttachmentsStore.getState().uploadsBySession['sess-1']).toBeUndefined()
  })

  it('incremental events retire placeholders one-by-one as conversions land', () => {
    beginAttachmentUploads('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
      { path: '/p/b.md', fileName: 'b.md', isImage: false },
    ])

    // First conversion lands: only its spinner retires, the list passes
    // through untouched (no duplicate chip alongside the spinner).
    expect(processIncomingAttachments('sess-1', [DOC_A])).toEqual([DOC_A])
    expect(useAttachmentsStore.getState().uploadsBySession['sess-1']?.map((u) => u.fileName)).toEqual(['b.md'])

    // Second conversion lands: the last spinner retires.
    expect(processIncomingAttachments('sess-1', [DOC_A, DOC_B])).toEqual([DOC_A, DOC_B])
    expect(useAttachmentsStore.getState().uploadsBySession['sess-1']).toBeUndefined()
  })
})

describe('attachmentUploads cancellation', () => {
  beforeEach(() => {
    resetState()
  })

  it('cancel removes the placeholder immediately and fires no removal when nothing landed', () => {
    const upload = first(beginAttachmentUploads('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
    ]))

    cancelAttachmentUpload('sess-1', upload)

    expect(useAttachmentsStore.getState().uploadsBySession['sess-1']).toBeUndefined()
    expect(spies.removeAttachment).not.toHaveBeenCalled()
  })

  it('cancel never touches a same-name record staged BEFORE the upload began', () => {
    // The ID window excludes pre-existing records: cancelling a re-added
    // report.pdf must not delete the report.pdf staged by an earlier batch.
    useAttachmentsStore.getState().setAttachments('sess-1', [DOC_A, DOC_B])
    const upload = first(beginAttachmentUploads('sess-1', [
      { path: '/other/a.md', fileName: 'a.md', isImage: false },
    ]))

    cancelAttachmentUpload('sess-1', upload)

    expect(spies.removeAttachment).not.toHaveBeenCalled()
  })

  it('a record landing after cancel is claimed and removed even when neither path nor name matches', () => {
    // The production shape of the issue: the backend re-encodes the image to
    // a server-side copy (path never matches) and the landed record's
    // originalName need not equal the placeholder's label. The ID window +
    // FIFO claim still attribute it to the cancelled upload.
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/x/IMG_2024.heic', fileName: 'IMG_2024.heic', isImage: true },
    ])
    cancelAttachmentUpload('sess-1', first(uploads))

    const filtered = processIncomingAttachments('sess-1', [IMG_LANDED, DOC_B])

    expect(filtered).toEqual([DOC_B])
    expect(spies.removeAttachment).toHaveBeenCalledWith('sess-1', IMG_LANDED.id)
  })

  it('cancel sweeps a record that already landed while the placeholder was still up', () => {
    // Real chip landed via the store, spinner not yet retired (event/claim
    // raced the user's click): the sweep claims it by id and removes it.
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/x/IMG_2024.heic', fileName: 'IMG_2024.heic', isImage: true },
    ])
    useAttachmentsStore.getState().setAttachments('sess-1', [IMG_LANDED])

    cancelAttachmentUpload('sess-1', first(uploads))

    expect(useAttachmentsStore.getState().uploadsBySession['sess-1']).toBeUndefined()
    expect(spies.removeAttachment).toHaveBeenCalledWith('sess-1', IMG_LANDED.id)
  })

  it('re-adding the same file after a cancel is not eaten by the stale cancel', () => {
    const firstBatch = beginAttachmentUploads('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
    ])
    cancelAttachmentUpload('sess-1', first(firstBatch))

    // The user changes their mind and attaches the same file again before the
    // first batch settles: the stale cancel is purged, the fresh upload's
    // record must survive.
    beginAttachmentUploads('sess-1', [{ path: '/p/a.md', fileName: 'a.md', isImage: false }])

    expect(processIncomingAttachments('sess-1', [DOC_A])).toEqual([DOC_A])
    expect(spies.removeAttachment).not.toHaveBeenCalled()
  })

  it('complete strips the cancelled upload from the incoming list and removes it on the backend', async () => {
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
      { path: '/p/b.md', fileName: 'b.md', isImage: false },
    ])
    cancelAttachmentUpload('sess-1', first(uploads))

    const kept = completeAttachmentUploads('sess-1', uploads, [DOC_A, DOC_B])

    expect(kept).toEqual([DOC_B])
    expect(spies.removeAttachment).toHaveBeenCalledWith('sess-1', DOC_A.id)
    expect(useAttachmentsStore.getState().uploadsBySession['sess-1']).toBeUndefined()
    await flushMicrotasks()
  })

  it('processIncomingAttachments strips cancelled claims from event lists (no flash-back)', async () => {
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
    ])
    cancelAttachmentUpload('sess-1', first(uploads))

    // The backend's incremental event still reports the cancelled file.
    const filtered = processIncomingAttachments('sess-1', [DOC_A, DOC_B])
    expect(filtered).toEqual([DOC_B])
    expect(spies.removeAttachment).toHaveBeenCalledWith('sess-1', DOC_A.id)

    // Repeated sightings within the removal window fire ONE removal (dedupe).
    processIncomingAttachments('sess-1', [DOC_A, DOC_B])
    expect(spies.removeAttachment).toHaveBeenCalledTimes(1)

    // The staging op settles → the cancelled entry retires once its removal
    // resolves; afterwards ordinary lists pass through untouched again.
    completeAttachmentUploads('sess-1', uploads, [DOC_A, DOC_B])
    await flushMicrotasks()
    expect(processIncomingAttachments('sess-1', [DOC_A, DOC_B])).toEqual([DOC_A, DOC_B])
  })

  it('processIncomingAttachments is a pass-through when nothing was cancelled', () => {
    beginAttachmentUploads('sess-1', [{ path: '/p/a.md', fileName: 'a.md', isImage: false }])

    expect(processIncomingAttachments('sess-1', [DOC_A])).toEqual([DOC_A])
    expect(spies.removeAttachment).not.toHaveBeenCalled()
  })

  it('processIncomingAttachments only considers the given session', () => {
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
    ])
    cancelAttachmentUpload('sess-1', first(uploads))

    // The same attachment in ANOTHER session is not the cancelled upload.
    expect(processIncomingAttachments('sess-2', [DOC_A])).toEqual([DOC_A])
    expect(spies.removeAttachment).not.toHaveBeenCalled()
  })
})

describe('collectPasteUploadDescriptors', () => {
  function fakeTransfer(items: Array<{ kind: string; type: string; name?: string }>): DataTransfer {
    return {
      items: items.map((i) => ({
        kind: i.kind,
        type: i.type,
        getAsFile: () => (i.name === undefined ? null : { name: i.name }),
      })),
    } as unknown as DataTransfer
  }

  it('maps an image paste to ONE display-labelled descriptor (vision on)', () => {
    const data = fakeTransfer([
      { kind: 'file', type: 'image/png' },
      { kind: 'string', type: 'text/plain' },
    ])

    expect(collectPasteUploadDescriptors(data, true)).toEqual([
      { path: '', fileName: 'pasted-image', isImage: true },
    ])
  })

  it('labels clipboard images without synthesizing an extension from the webview MIME', () => {
    // TIFF from the webview, PNG on the wire — the label stays MIME-agnostic
    // because claiming is ID-based, not name-based.
    const data = fakeTransfer([{ kind: 'file', type: 'image/tiff' }])

    expect(collectPasteUploadDescriptors(data, true)).toEqual([
      { path: '', fileName: 'pasted-image', isImage: true },
    ])
  })

  it('maps clipboard FILE pastes to per-file descriptors with their names', () => {
    const data = fakeTransfer([
      { kind: 'file', type: '', name: 'spec.pdf' },
      { kind: 'file', type: '', name: 'img.png' },
    ])

    expect(collectPasteUploadDescriptors(data, true)).toEqual([
      { path: '', fileName: 'spec.pdf', isImage: false },
      { path: '', fileName: 'img.png', isImage: true },
    ])
  })

  it('filters vision-gated image files out of file pastes (no vision)', () => {
    const data = fakeTransfer([
      { kind: 'file', type: '', name: 'spec.pdf' },
      { kind: 'file', type: '', name: 'img.png' },
    ])

    expect(collectPasteUploadDescriptors(data, false)).toEqual([
      { path: '', fileName: 'spec.pdf', isImage: false },
    ])
  })

  it('stages nothing when an image paste is vision-rejected', () => {
    const data = fakeTransfer([{ kind: 'file', type: 'image/jpeg' }])

    expect(collectPasteUploadDescriptors(data, false)).toEqual([])
  })

  it('returns no descriptors for a text-only paste', () => {
    const data = fakeTransfer([{ kind: 'string', type: 'text/plain' }])

    expect(collectPasteUploadDescriptors(data, true)).toEqual([])
  })
})

describe('attachmentUploads claim safety (stale baseline / unverified claims)', () => {
  beforeEach(() => {
    resetState()
  })

  it('a cancelled upload never claims via FIFO: an unrelated pre-existing record survives', () => {
    // The reviewer's stale-baseline scenario: the store slice was empty when
    // the batch began (initial fetch still in flight), so a pre-existing
    // unrelated attachment X falls OUTSIDE the baseline. FIFO would hand X
    // to the cancelled entry and RemoveAttachment would delete the user's
    // earlier staged file. Verified-claims-only prevents the removal; the
    // record must pass through untouched.
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/p/notes.txt', fileName: 'notes.txt', isImage: false },
    ])
    cancelAttachmentUpload('sess-1', first(uploads))

    const unrelated: AttachmentInfoUI = {
      id: 'x1',
      originalName: 'quarterly-report.pdf',
      format: 'pdf',
      sizeBytes: 9,
    }
    const filtered = processIncomingAttachments('sess-1', [unrelated])

    expect(filtered).toEqual([unrelated])
    expect(spies.removeAttachment).not.toHaveBeenCalled()
  })

  it('a same-stem sibling record is NOT verified (extensioned labels match exactly only)', () => {
    // MUST-FIX regression: the stem tolerance must not apply to extensioned
    // file names — cancelling notes.md must never remove notes.pdf.
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/p/notes.md', fileName: 'notes.md', isImage: false },
    ])
    cancelAttachmentUpload('sess-1', first(uploads))

    const sibling: AttachmentInfoUI = {
      id: 's1',
      originalName: 'notes.pdf',
      format: 'pdf',
      sizeBytes: 4,
    }
    const filtered = processIncomingAttachments('sess-1', [sibling])

    expect(filtered).toEqual([sibling])
    expect(spies.removeAttachment).not.toHaveBeenCalled()
  })

  it('a cancelled upload still claims records whose originalName verifies (re-encoded image)', () => {
    // Realistic cancel: the source file was re-encoded server-side (path
    // never matches), but the backend preserves the source basename in
    // originalName — a verified claim, so removal proceeds.
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '/x/IMG_2024.heic', fileName: 'IMG_2024.heic', isImage: true },
    ])
    cancelAttachmentUpload('sess-1', first(uploads))

    const filtered = processIncomingAttachments('sess-1', [IMG_LANDED])

    expect(filtered).toEqual([])
    expect(spies.removeAttachment).toHaveBeenCalledWith('sess-1', IMG_LANDED.id)
  })

  it('a cancelled clipboard-image upload claims the normalized record via stem tolerance', () => {
    // The paste descriptor carries the extension-less label while the
    // backend names the record pasted-image.png: 'pasted-image' must match
    // 'pasted-image.' as a verified claim.
    const uploads = beginAttachmentUploads('sess-1', [
      { path: '', fileName: 'pasted-image', isImage: true },
    ])
    cancelAttachmentUpload('sess-1', first(uploads))

    const landed: AttachmentInfoUI = {
      id: 'p1',
      originalName: 'pasted-image.png',
      format: 'png',
      sizeBytes: 3,
      isImage: true,
    }
    const filtered = processIncomingAttachments('sess-1', [landed])

    expect(filtered).toEqual([])
    expect(spies.removeAttachment).toHaveBeenCalledWith('sess-1', landed.id)
  })

  it('beginAttachmentUploadsFresh unions the backend pending list into the baseline', async () => {
    // Same stale-slice start, but the fresh variant's GetAttachments read
    // knows the pre-existing record: it lands INSIDE the window, so it can
    // never be claimed — even by a non-cancelled FIFO entry.
    const preExisting: AttachmentInfoUI = {
      id: 'old1',
      originalName: 'existing.md',
      format: 'md',
      sizeBytes: 1,
    }
    spies.getAttachments.mockResolvedValue([preExisting])

    const uploads = await beginAttachmentUploadsFresh('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
    ])
    const filtered = processIncomingAttachments('sess-1', [preExisting, DOC_A])

    // The upload's own record (a.md) is claimed — its spinner retires but the
    // record stays in the list (it is a real staged attachment). The
    // pre-existing record is inside the baseline, so it can never be claimed
    // by this batch.
    expect(filtered).toEqual([preExisting, DOC_A])
    expect(useAttachmentsStore.getState().uploadsBySession['sess-1']).toBeUndefined()
    expect(uploads).toHaveLength(1)
    expect(spies.getAttachments).toHaveBeenCalledWith('sess-1')
  })

  it('beginAttachmentUploadsFresh falls back to the store slice when the baseline RPC fails', async () => {
    spies.getAttachments.mockRejectedValue(new Error('rpc down'))

    const uploads = await beginAttachmentUploadsFresh('sess-1', [
      { path: '/p/a.md', fileName: 'a.md', isImage: false },
    ])

    expect(uploads).toHaveLength(1)
    expect(useAttachmentsStore.getState().uploadsBySession['sess-1']).toEqual(uploads)
  })
})
