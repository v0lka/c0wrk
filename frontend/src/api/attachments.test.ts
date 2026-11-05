// Unit tests for the attachment event-payload → store mapping (pure functions).
//
// These cover the snake_case backend AttachmentInfo → camelCase AttachmentInfoUI
// boundary used by the `attachments:changed` event handler and the attachment
// RPCs. No React, no Wails runtime — just the mappers and the event guard.

import { describe, it, expect, vi, afterEach } from 'vitest'
import { mapAttachment, mapAttachments, isImagePath, attachmentBaseName, mapPasteResult, attachFileContent, MAX_ATTACHMENT_UPLOAD_BYTES } from '@/api/attachments'
import { isAttachmentsChangedData, isAttachmentInfoRaw, isPasteResultRaw } from '@/types/events'
import { logger } from '@/lib/logger'

// The RPC wrapper tests below mock the transport shim; the pure-mapping
// tests above never touch it.
const rpcApp = vi.hoisted(() => ({ AttachFileContent: vi.fn() }))
vi.mock('@/api/runtime', () => ({ getApp: () => rpcApp }))

describe('mapAttachment', () => {
  it('maps snake_case backend fields to camelCase UI fields', () => {
    expect(mapAttachment({ id: 'x', original_name: 'doc.md', format: 'md', size_bytes: 42 })).toEqual({
      id: 'x',
      originalName: 'doc.md',
      format: 'md',
      sizeBytes: 42,
    })
  })

  it('round-trips an empty/zero payload', () => {
    expect(mapAttachment({ id: '', original_name: '', format: '', size_bytes: 0 })).toEqual({
      id: '',
      originalName: '',
      format: '',
      sizeBytes: 0,
    })
  })

  it('maps image fields (is_image → isImage, thumbnail → thumbnail)', () => {
    expect(
      mapAttachment({
        id: 'img1',
        original_name: 'screenshot.png',
        format: 'png',
        size_bytes: 4096,
        is_image: true,
        thumbnail: 'data:image/jpeg;base64,abc',
        path: '/tmp/screenshot.png',
        media_type: 'image/png',
      }),
    ).toEqual({
      id: 'img1',
      originalName: 'screenshot.png',
      format: 'png',
      sizeBytes: 4096,
      isImage: true,
      thumbnail: 'data:image/jpeg;base64,abc',
      path: '/tmp/screenshot.png',
      mediaType: 'image/png',
    })
  })

  it('omits image fields when absent (non-image attachment)', () => {
    expect(mapAttachment({ id: 'doc', original_name: 'note.md', format: 'md', size_bytes: 10 })).toEqual({
      id: 'doc',
      originalName: 'note.md',
      format: 'md',
      sizeBytes: 10,
    })
  })
})

describe('mapAttachments', () => {
  it('maps a list preserving order', () => {
    const out = mapAttachments([
      { id: '1', original_name: 'a.txt', format: 'txt', size_bytes: 1 },
      { id: '2', original_name: 'b.txt', format: 'txt', size_bytes: 2 },
    ])
    expect(out.map((a) => a.id)).toEqual(['1', '2'])
    expect(out[1]).toEqual({ id: '2', originalName: 'b.txt', format: 'txt', sizeBytes: 2 })
  })

  it('returns a fresh array (does not mutate the input)', () => {
    const input = [{ id: '1', original_name: 'a.txt', format: 'txt', size_bytes: 1 }]
    const out = mapAttachments(input)
    expect(out).not.toBe(input)
    expect(input).toEqual([{ id: '1', original_name: 'a.txt', format: 'txt', size_bytes: 1 }])
  })
})

describe('isAttachmentsChangedData (event payload guard)', () => {
  it('accepts an object with a valid attachments array', () => {
    expect(
      isAttachmentsChangedData({
        attachments: [{ id: '1', original_name: 'a', format: 'txt', size_bytes: 1 }],
      }),
    ).toBe(true)
  })

  it('accepts an object with an empty attachments array (pending list flushed after send)', () => {
    expect(isAttachmentsChangedData({ attachments: [] })).toBe(true)
  })

  it('accepts an object with an optional failed array', () => {
    expect(
      isAttachmentsChangedData({
        attachments: [],
        failed: [{ path: 'bad.mp3', error: 'unsupported file format' }],
      }),
    ).toBe(true)
  })

  it('accepts an absent failed array (remove/send-clear events)', () => {
    expect(isAttachmentsChangedData({ attachments: [] })).toBe(true)
  })

  it('rejects a non-array failed value', () => {
    // The handler iterates failed[].path to build the user-facing toast — a
    // malformed value must drop the event at the boundary, not throw there.
    expect(isAttachmentsChangedData({ attachments: [], failed: 'bad.mp3' })).toBe(false)
    expect(isAttachmentsChangedData({ attachments: [], failed: { path: 'bad.mp3' } })).toBe(false)
  })

  it('rejects a failed element missing or mangling its string fields', () => {
    expect(isAttachmentsChangedData({ attachments: [], failed: [{ path: 42 }] })).toBe(false)
    expect(
      isAttachmentsChangedData({ attachments: [], failed: [{ path: 'bad.mp3', error: 7 }] }),
    ).toBe(false)
    expect(isAttachmentsChangedData({ attachments: [], failed: ['bad.mp3'] })).toBe(false)
  })

  it('rejects a bare array (old payload shape)', () => {
    expect(isAttachmentsChangedData([{ id: '1', format: 'txt' }])).toBe(false)
  })

  it('rejects an object with a malformed attachment element', () => {
    expect(isAttachmentsChangedData({ attachments: [{ id: '1', format: 'txt' }] })).toBe(false)
  })

  it('rejects null and non-objects', () => {
    expect(isAttachmentsChangedData(null)).toBe(false)
    expect(isAttachmentsChangedData('x')).toBe(false)
  })
})

describe('isAttachmentInfoRaw', () => {
  it('accepts a well-formed record', () => {
    expect(isAttachmentInfoRaw({ id: '1', original_name: 'a', format: 'txt', size_bytes: 1 })).toBe(true)
  })

  it('accepts a record with image fields (is_image/thumbnail)', () => {
    expect(
      isAttachmentInfoRaw({
        id: '1',
        original_name: 'a.png',
        format: 'png',
        size_bytes: 1,
        is_image: true,
        thumbnail: 'data:image/jpeg;base64,xyz',
      }),
    ).toBe(true)
  })

  it('rejects null and non-objects', () => {
    expect(isAttachmentInfoRaw(null)).toBe(false)
    expect(isAttachmentInfoRaw('x')).toBe(false)
  })

  it('rejects a record with a non-number size_bytes', () => {
    expect(isAttachmentInfoRaw({ id: '1', original_name: 'a', format: 'txt', size_bytes: '1' })).toBe(false)
  })
})

describe('isImagePath', () => {
  it('returns true for png/jpg/jpeg/gif/webp extensions', () => {
    expect(isImagePath('/tmp/photo.png')).toBe(true)
    expect(isImagePath('/tmp/photo.jpg')).toBe(true)
    expect(isImagePath('/tmp/photo.jpeg')).toBe(true)
    expect(isImagePath('/tmp/photo.gif')).toBe(true)
    expect(isImagePath('/tmp/photo.webp')).toBe(true)
  })

  it('is case-insensitive', () => {
    expect(isImagePath('/tmp/PHOTO.PNG')).toBe(true)
    expect(isImagePath('/tmp/Photo.Jpg')).toBe(true)
  })

  it('returns false for non-image extensions', () => {
    expect(isImagePath('/tmp/doc.pdf')).toBe(false)
    expect(isImagePath('/tmp/note.md')).toBe(false)
    expect(isImagePath('/tmp/data.csv')).toBe(false)
  })

  it('returns false for files without an extension', () => {
    expect(isImagePath('/tmp/README')).toBe(false)
  })
})

describe('attachmentBaseName', () => {
  it('returns the last POSIX path segment', () => {
    expect(attachmentBaseName('/home/user/docs/report.pdf')).toBe('report.pdf')
    expect(attachmentBaseName('report.pdf')).toBe('report.pdf')
  })

  it('understands Windows backslash separators (the backend runs on Windows too)', () => {
    expect(attachmentBaseName('C:\\Users\\vk\\photo.png')).toBe('photo.png')
    expect(attachmentBaseName('C:\\photo.png')).toBe('photo.png')
  })

  it('handles mixed separators', () => {
    expect(attachmentBaseName('/mnt/c\\Users\\vk\\note.md')).toBe('note.md')
  })
})

describe('mapPasteResult', () => {
  it('maps snake_case skipped_images → skippedImages and defaults files to []', () => {
    expect(mapPasteResult({ kind: 'files', skipped_images: 3 })).toEqual({
      kind: 'files',
      text: undefined,
      files: [],
      rejected: undefined,
      skippedImages: 3,
    })
  })

  it('forwards text and rejected, omitting skippedImages when absent', () => {
    expect(
      mapPasteResult({ kind: 'image', rejected: 'vision_unsupported', files: [] }),
    ).toEqual({
      kind: 'image',
      text: undefined,
      files: [],
      rejected: 'vision_unsupported',
      skippedImages: undefined,
    })
  })
})

describe('isPasteResultRaw', () => {
  it('accepts a well-formed files result with skipped_images', () => {
    expect(
      isPasteResultRaw({
        kind: 'files',
        files: [],
        skipped_images: 2,
      }),
    ).toBe(true)
  })

  it('accepts a result without skipped_images (optional)', () => {
    expect(isPasteResultRaw({ kind: 'text', text: 'hi' })).toBe(true)
  })

  it('rejects a non-number skipped_images', () => {
    expect(isPasteResultRaw({ kind: 'files', skipped_images: 'two' })).toBe(false)
  })

  it('rejects an unknown kind', () => {
    expect(isPasteResultRaw({ kind: 'binary', files: [] })).toBe(false)
  })
})

describe('attachFileContent (web upload path)', () => {
  const rawInfo = { id: 'a1', original_name: 'dropped.md', format: 'md', size_bytes: 3 }
  const mappedInfo = { id: 'a1', originalName: 'dropped.md', format: 'md', sizeBytes: 3 }

  afterEach(() => {
    rpcApp.AttachFileContent.mockReset()
    vi.restoreAllMocks()
  })

  it('a browser file drop creates an attachment: bytes travel base64, response maps to UI records', async () => {
    rpcApp.AttachFileContent.mockResolvedValue([rawInfo])

    const result = await attachFileContent('sess-1', {
      name: 'dropped.md',
      type: 'text/markdown',
      bytes: new Uint8Array([1, 2, 3]),
    })

    // arg4 is the Go []byte wire form: a base64 string ("AQID" = 01 02 03).
    expect(rpcApp.AttachFileContent).toHaveBeenCalledWith('sess-1', 'dropped.md', 'text/markdown', 'AQID')
    expect(result).toEqual([mappedInfo])
  })

  it('rejects oversize payloads before any RPC round-trip', async () => {
    await expect(
      attachFileContent('sess-1', {
        name: 'huge.bin',
        type: 'application/octet-stream',
        bytes: new Uint8Array(MAX_ATTACHMENT_UPLOAD_BYTES + 1),
      }),
    ).rejects.toThrow('above the')

    expect(rpcApp.AttachFileContent).not.toHaveBeenCalled()
  })

  it('accepts a payload at exactly the limit', async () => { // max-size payload encode/transport: generous bound under parallel suite load
    rpcApp.AttachFileContent.mockResolvedValue([])
    await expect(
      attachFileContent('sess-1', {
        name: 'max.bin',
        type: 'application/octet-stream',
        bytes: new Uint8Array(MAX_ATTACHMENT_UPLOAD_BYTES),
      }),
    ).resolves.toEqual([])
    expect(rpcApp.AttachFileContent).toHaveBeenCalledTimes(1)
  }, 30_000)

  it('logs and returns [] on an unexpected response shape', async () => {
    rpcApp.AttachFileContent.mockResolvedValue({ nope: true })
    const errorSpy = vi.spyOn(logger, 'error').mockImplementation(() => {})

    await expect(
      attachFileContent('sess-1', { name: 'a.md', type: 'text/markdown', bytes: new Uint8Array([1]) }),
    ).resolves.toEqual([])

    expect(errorSpy).toHaveBeenCalledTimes(1)
  })

  it('propagates a backend failure (logged at the boundary)', async () => {
    rpcApp.AttachFileContent.mockRejectedValue(new Error('boom'))
    const errorSpy = vi.spyOn(logger, 'error').mockImplementation(() => {})

    await expect(
      attachFileContent('sess-1', { name: 'a.md', type: 'text/markdown', bytes: new Uint8Array([1]) }),
    ).rejects.toThrow('boom')

    expect(errorSpy).toHaveBeenCalledTimes(1)
  })
})
