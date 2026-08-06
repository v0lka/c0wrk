// Unit tests for shouldFastPathPaste — the fast-path heuristic that decides
// whether the chat editor should let CodeMirror insert a paste natively (pure
// text/html) or route it through the Go backend (image / copied files).
//
// This is a pure function over a DataTransfer, so it needs no React/jsdom
// rendering — we build minimal fake DataTransfer objects inline.

import { describe, it, expect } from 'vitest'
import { shouldFastPathPaste } from '@/hooks/useChatEditor'

/** Build a minimal DataTransfer-like object from a list of {kind, type} items. */
function fakeDataTransfer(items: Array<{ kind: 'string' | 'file'; type: string }>): DataTransfer {
  return {
    items: items as unknown as DataTransferItemList,
  } as DataTransfer
}

describe('shouldFastPathPaste', () => {
  it('fast-paths plain text (string item, text/plain)', () => {
    expect(shouldFastPathPaste(fakeDataTransfer([{ kind: 'string', type: 'text/plain' }]))).toBe(true)
  })

  it('fast-paths rich text (string items, text/plain + text/html, no file)', () => {
    const data = fakeDataTransfer([
      { kind: 'string', type: 'text/plain' },
      { kind: 'string', type: 'text/html' },
    ])
    expect(shouldFastPathPaste(data)).toBe(true)
  })

  it('fast-paths text/html only (rich text from a browser/AI chat)', () => {
    expect(shouldFastPathPaste(fakeDataTransfer([{ kind: 'string', type: 'text/html' }]))).toBe(true)
  })

  it('does NOT fast-path an image paste (file item, image/png)', () => {
    expect(shouldFastPathPaste(fakeDataTransfer([{ kind: 'file', type: 'image/png' }]))).toBe(false)
  })

  it('does NOT fast-path a screenshot that also carries a text/html shadow', () => {
    // Browsers often expose a screenshot as BOTH an image file and a text/html
    // representation; the file item must force the backend route.
    const data = fakeDataTransfer([
      { kind: 'string', type: 'text/html' },
      { kind: 'file', type: 'image/png' },
    ])
    expect(shouldFastPathPaste(data)).toBe(false)
  })

  it('does NOT fast-path a copied file (file item, generic/empty type)', () => {
    expect(shouldFastPathPaste(fakeDataTransfer([{ kind: 'file', type: '' }]))).toBe(false)
  })

  it('treats an empty clipboard as a fast path (nothing to route)', () => {
    expect(shouldFastPathPaste(fakeDataTransfer([]))).toBe(true)
  })
})
