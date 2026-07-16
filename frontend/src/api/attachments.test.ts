// Unit tests for the attachment event-payload → store mapping (pure functions).
//
// These cover the snake_case backend AttachmentInfo → camelCase AttachmentInfoUI
// boundary used by the `attachments:changed` event handler and the attachment
// RPCs. No React, no Wails runtime — just the mappers and the event guard.

import { describe, it, expect } from 'vitest'
import { mapAttachment, mapAttachments } from '@/api/attachments'
import { isAttachmentsChangedData, isAttachmentInfoRaw } from '@/types/events'

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

  it('rejects null and non-objects', () => {
    expect(isAttachmentInfoRaw(null)).toBe(false)
    expect(isAttachmentInfoRaw('x')).toBe(false)
  })

  it('rejects a record with a non-number size_bytes', () => {
    expect(isAttachmentInfoRaw({ id: '1', original_name: 'a', format: 'txt', size_bytes: '1' })).toBe(false)
  })
})
