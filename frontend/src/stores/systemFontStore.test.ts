// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'

// jsdom in this environment does not expose `window.localStorage`, which
// zustand's `persist` middleware captures at store-creation time. Install an
// in-memory polyfill before any store module is imported so systemFontStore
// works (same setup as uiScaleStore.test.ts).
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => { map.set(k, v) },
    removeItem: (k: string) => { map.delete(k) },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    get length() { return map.size },
  }
})

import {
  useSystemFontStore,
  applySystemFontToDocument,
  SYSTEM_FONT_CSS_VAR,
} from '@/stores/systemFontStore'

function cssVar(): string {
  return document.documentElement.style.getPropertyValue(SYSTEM_FONT_CSS_VAR)
}

describe('systemFontStore', () => {
  beforeEach(() => {
    // Reset to defaults and clear any persisted state between tests.
    useSystemFontStore.setState({ followSystemFont: false, systemFontFamily: null })
    localStorage.clear()
    document.documentElement.style.removeProperty(SYSTEM_FONT_CSS_VAR)
  })

  it('defaults to off with no family (opt-in, nothing detected yet)', () => {
    expect(useSystemFontStore.getState().followSystemFont).toBe(false)
    expect(useSystemFontStore.getState().systemFontFamily).toBeNull()
  })

  it('applySystemFontToDocument writes a quoted family onto <html>', () => {
    applySystemFontToDocument(true, 'Noto Sans')
    // Double quotes keep a multi-word family name one CSS token.
    expect(cssVar()).toBe('"Noto Sans"')
  })

  it('applySystemFontToDocument removes the property when not following', () => {
    applySystemFontToDocument(true, 'Noto Sans')
    applySystemFontToDocument(false, 'Noto Sans')
    expect(cssVar()).toBe('')
  })

  it('applySystemFontToDocument removes the property when no family is known', () => {
    applySystemFontToDocument(true, 'Noto Sans')
    applySystemFontToDocument(true, null)
    expect(cssVar()).toBe('')
  })

  it('applySystemFontToDocument is idempotent (safe to call repeatedly)', () => {
    expect(() => {
      applySystemFontToDocument(true, 'Cantarell')
      applySystemFontToDocument(true, 'Cantarell')
      applySystemFontToDocument(false, null)
      applySystemFontToDocument(false, null)
    }).not.toThrow()
  })

  it('applySystemFontToDocument is a no-op without document', () => {
    const g = globalThis as Record<string, unknown>
    const originalDocument = g.document
    try {
      delete g.document
      expect(() => applySystemFontToDocument(true, 'Noto Sans')).not.toThrow()
      expect(() => applySystemFontToDocument(false, null)).not.toThrow()
    } finally {
      g.document = originalDocument
    }
  })

  it('setFollowSystemFont applies the flag to <html> immediately', () => {
    useSystemFontStore.getState().setSystemFontFamily('Noto Sans')
    // Family cached while off: nothing on <html> yet.
    expect(cssVar()).toBe('')

    useSystemFontStore.getState().setFollowSystemFont(true)
    expect(cssVar()).toBe('"Noto Sans"')

    useSystemFontStore.getState().setFollowSystemFont(false)
    expect(cssVar()).toBe('')
  })

  it('setSystemFontFamily applies to <html> only while following', () => {
    useSystemFontStore.getState().setFollowSystemFont(true)
    useSystemFontStore.getState().setSystemFontFamily('DejaVu Sans')
    expect(cssVar()).toBe('"DejaVu Sans"')

    // A later family update swaps the applied value…
    useSystemFontStore.getState().setSystemFontFamily('Cantarell')
    expect(cssVar()).toBe('"Cantarell"')

    // …and clearing it (desktop reports none) removes the override entirely.
    useSystemFontStore.getState().setSystemFontFamily(null)
    expect(cssVar()).toBe('')
  })

  it('setSystemFontFamily records the family even while off (no re-fetch needed on toggle)', () => {
    useSystemFontStore.getState().setSystemFontFamily('Noto Sans')
    expect(useSystemFontStore.getState().systemFontFamily).toBe('Noto Sans')
    expect(cssVar()).toBe('')
  })

  it('persists only {followSystemFont} under the c0wrk-follow-system-font key', () => {
    useSystemFontStore.getState().setSystemFontFamily('Noto Sans')
    useSystemFontStore.getState().setFollowSystemFont(true)

    const raw = localStorage.getItem('c0wrk-follow-system-font')
    expect(raw).not.toBeNull()
    // zustand's persist middleware wraps the payload as {state, version};
    // partialize must keep `state` limited to the flag (the family and the
    // actions never hit storage).
    const parsed = JSON.parse(raw as string) as {
      state: Record<string, unknown>
      version: number
    }
    expect(parsed.state).toEqual({ followSystemFont: true })
    expect(parsed.version).toBe(1)
  })
})
