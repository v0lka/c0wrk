// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'

// localStorage itself is polyfilled globally by src/test/setup.ts (in-memory
// Storage installed before any store module import); this file only needs the
// jsdom environment for `document`.

import {
  useFontStore,
  applyFontsToDocument,
  removeOrphanSystemFontKey,
  FONT_SANS_CSS_VAR,
  FONT_MONO_CSS_VAR,
} from '@/stores/fontStore'
import { FONT_SANS_STACK, FONT_MONO_STACK } from '@/lib/fonts'

const LEGACY_KEY = 'c0wrk-follow-system-font'
const STORE_KEY = 'c0wrk-fonts'

function sansVar(): string {
  return document.documentElement.style.getPropertyValue(FONT_SANS_CSS_VAR)
}

function monoVar(): string {
  return document.documentElement.style.getPropertyValue(FONT_MONO_CSS_VAR)
}

describe('fontStore', () => {
  beforeEach(() => {
    // Reset to defaults and clear any persisted state between tests.
    useFontStore.setState({
      uiFontFamily: null,
      monoFontFamily: null,
      detectedUIFamily: null,
      detectedMonoFamily: null,
    })
    localStorage.clear()
    document.documentElement.style.removeProperty(FONT_SANS_CSS_VAR)
    document.documentElement.style.removeProperty(FONT_MONO_CSS_VAR)
  })

  it('defaults to null everywhere (default index.css stacks, nothing detected)', () => {
    const s = useFontStore.getState()
    expect(s.uiFontFamily).toBeNull()
    expect(s.monoFontFamily).toBeNull()
    expect(s.detectedUIFamily).toBeNull()
    expect(s.detectedMonoFamily).toBeNull()
  })

  describe('applyFontsToDocument', () => {
    it('writes the sans family quoted and prepended to the default stack', () => {
      applyFontsToDocument('Noto Sans', null)
      // Double quotes keep a multi-word family name one CSS token; the stock
      // stack rides along so a missing font degrades to the default, not to
      // a bare serif fallback.
      expect(sansVar()).toBe(`"Noto Sans", ${FONT_SANS_STACK}`)
      expect(monoVar()).toBe('')
    })

    it('writes the mono family independently of the sans one', () => {
      applyFontsToDocument('Noto Sans', 'JetBrains Mono')
      expect(sansVar()).toBe(`"Noto Sans", ${FONT_SANS_STACK}`)
      expect(monoVar()).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
    })

    it('removes the property when the family is null (default stack applies)', () => {
      applyFontsToDocument('Noto Sans', 'JetBrains Mono')
      applyFontsToDocument(null, 'JetBrains Mono')
      expect(sansVar()).toBe('')
      expect(monoVar()).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
      applyFontsToDocument(null, null)
      expect(monoVar()).toBe('')
    })

    it('is idempotent (safe to call repeatedly)', () => {
      expect(() => {
        applyFontsToDocument('Cantarell', 'JetBrains Mono')
        applyFontsToDocument('Cantarell', 'JetBrains Mono')
        applyFontsToDocument(null, null)
        applyFontsToDocument(null, null)
      }).not.toThrow()
      expect(sansVar()).toBe('')
      expect(monoVar()).toBe('')
    })

    it('is a no-op without document', () => {
      const g = globalThis as Record<string, unknown>
      const originalDocument = g.document
      try {
        delete g.document
        expect(() => applyFontsToDocument('Noto Sans', 'JetBrains Mono')).not.toThrow()
        expect(() => applyFontsToDocument(null, null)).not.toThrow()
      } finally {
        g.document = originalDocument
      }
    })
  })

  describe('family actions', () => {
    it('setUIFontFamily applies to <html> before set, normalized', () => {
      useFontStore.getState().setUIFontFamily('  No"to Sans  ')
      // Quote stripped, whitespace trimmed — applied AND stored canonically.
      expect(sansVar()).toBe(`"Noto Sans", ${FONT_SANS_STACK}`)
      expect(useFontStore.getState().uiFontFamily).toBe('Noto Sans')
    })

    it('setUIFontFamily collapses an empty/blank family to the default', () => {
      useFontStore.getState().setUIFontFamily('Noto Sans')
      useFontStore.getState().setUIFontFamily('   ')
      expect(useFontStore.getState().uiFontFamily).toBeNull()
      expect(sansVar()).toBe('')
    })

    it('setMonoFontFamily applies its own variable, leaving sans untouched', () => {
      useFontStore.getState().setUIFontFamily('Noto Sans')
      useFontStore.getState().setMonoFontFamily('JetBrains Mono')
      expect(sansVar()).toBe(`"Noto Sans", ${FONT_SANS_STACK}`)
      expect(monoVar()).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)

      useFontStore.getState().setMonoFontFamily(null)
      expect(monoVar()).toBe('')
      expect(sansVar()).toBe(`"Noto Sans", ${FONT_SANS_STACK}`)
    })

    it('setDetectedFonts records the candidates but never touches <html>', () => {
      useFontStore.getState().setDetectedFonts('Noto Sans', 'JetBrains Mono')
      expect(useFontStore.getState().detectedUIFamily).toBe('Noto Sans')
      expect(useFontStore.getState().detectedMonoFamily).toBe('JetBrains Mono')
      expect(sansVar()).toBe('')
      expect(monoVar()).toBe('')

      // Not even when a chosen family is already applied: a late detection
      // arriving must not repaint text behind the user's choice.
      useFontStore.getState().setUIFontFamily('Cantarell')
      useFontStore.getState().setDetectedFonts('DejaVu Sans', null)
      expect(sansVar()).toBe(`"Cantarell", ${FONT_SANS_STACK}`)
      expect(useFontStore.getState().detectedUIFamily).toBe('DejaVu Sans')
    })
  })

  describe('persistence', () => {
    it('persists exactly {uiFontFamily, monoFontFamily} under c0wrk-fonts v1', () => {
      useFontStore.getState().setUIFontFamily('Noto Sans')
      useFontStore.getState().setMonoFontFamily('JetBrains Mono')
      useFontStore.getState().setDetectedFonts('DejaVu Sans', 'Cascadia Code')

      const raw = localStorage.getItem(STORE_KEY)
      expect(raw).not.toBeNull()
      // zustand's persist middleware wraps the payload as {state, version};
      // partialize must keep `state` limited to the two chosen families (the
      // session detections and the actions never hit storage).
      const parsed = JSON.parse(raw as string) as {
        state: Record<string, unknown>
        version: number
      }
      expect(parsed.state).toEqual({
        uiFontFamily: 'Noto Sans',
        monoFontFamily: 'JetBrains Mono',
      })
      expect(parsed.version).toBe(1)
    })

    it('rehydrates the persisted families synchronously on first access (startup contract)', async () => {
      // Seed the payload a previous run persisted, then re-import the store
      // module fresh — this is what main.tsx relies on: getState() at module
      // top level already carries the persisted families, so the startup
      // apply lands before React's first render.
      localStorage.setItem(
        STORE_KEY,
        JSON.stringify({
          state: { uiFontFamily: 'Cantarell', monoFontFamily: 'JetBrains Mono' },
          version: 1,
        }),
      )
      vi.resetModules()
      const fresh = await import('@/stores/fontStore')

      const s = fresh.useFontStore.getState()
      expect(s.uiFontFamily).toBe('Cantarell')
      expect(s.monoFontFamily).toBe('JetBrains Mono')
      // Session detections never persist: a fresh launch starts undetected.
      expect(s.detectedUIFamily).toBeNull()
      expect(s.detectedMonoFamily).toBeNull()

      // The startup-apply pattern from main.tsx paints both overrides.
      fresh.applyFontsToDocument(s.uiFontFamily, s.monoFontFamily)
      expect(sansVar()).toBe(`"Cantarell", ${FONT_SANS_STACK}`)
      expect(monoVar()).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
    })
  })

  describe('removeOrphanSystemFontKey', () => {
    it('removes the replaced follow-system-font store key', () => {
      localStorage.setItem(LEGACY_KEY, JSON.stringify({ state: { enabled: true }, version: 1 }))
      removeOrphanSystemFontKey()
      expect(localStorage.getItem(LEGACY_KEY)).toBeNull()
    })

    it('is a no-op (and does not throw) when the key is already gone', () => {
      expect(() => removeOrphanSystemFontKey()).not.toThrow()
      expect(localStorage.getItem(LEGACY_KEY)).toBeNull()
    })

    it('is a no-op without a storage', () => {
      const g = globalThis as Record<string, unknown>
      const original = g.localStorage
      try {
        delete g.localStorage
        expect(() => removeOrphanSystemFontKey()).not.toThrow()
      } finally {
        g.localStorage = original
      }
    })
  })
})
