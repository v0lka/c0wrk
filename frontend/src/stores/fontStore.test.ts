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
  FONT_SMOOTHING_SANS_CSS_VAR,
  FONT_SMOOTHING_MONO_CSS_VAR,
} from '@/stores/fontStore'
import { FONT_SANS_STACK, FONT_MONO_STACK, MAX_FONT_NAME_LENGTH } from '@/lib/fonts'

const LEGACY_KEY = 'c0wrk-follow-system-font'
const STORE_KEY = 'c0wrk-fonts'

function sansVar(): string {
  return document.documentElement.style.getPropertyValue(FONT_SANS_CSS_VAR)
}

function monoVar(): string {
  return document.documentElement.style.getPropertyValue(FONT_MONO_CSS_VAR)
}

function sansSmoothingVar(): string {
  return document.documentElement.style.getPropertyValue(FONT_SMOOTHING_SANS_CSS_VAR)
}

function monoSmoothingVar(): string {
  return document.documentElement.style.getPropertyValue(FONT_SMOOTHING_MONO_CSS_VAR)
}

describe('fontStore', () => {
  beforeEach(() => {
    // Reset to defaults and clear any persisted state between tests.
    useFontStore.setState({
      uiFontFamily: null,
      monoFontFamily: null,
      uiFontSmoothing: '',
      monoFontSmoothing: '',
      detectedUIFamily: null,
      detectedMonoFamily: null,
    })
    localStorage.clear()
    document.documentElement.style.removeProperty(FONT_SANS_CSS_VAR)
    document.documentElement.style.removeProperty(FONT_MONO_CSS_VAR)
    document.documentElement.style.removeProperty(FONT_SMOOTHING_SANS_CSS_VAR)
    document.documentElement.style.removeProperty(FONT_SMOOTHING_MONO_CSS_VAR)
  })

  it('defaults to null families and empty smoothing everywhere', () => {
    const s = useFontStore.getState()
    expect(s.uiFontFamily).toBeNull()
    expect(s.monoFontFamily).toBeNull()
    expect(s.uiFontSmoothing).toBe('')
    expect(s.monoFontSmoothing).toBe('')
    expect(s.detectedUIFamily).toBeNull()
    expect(s.detectedMonoFamily).toBeNull()
  })

  describe('applyFontsToDocument', () => {
    it('writes the sans family quoted and prepended to the default stack', () => {
      applyFontsToDocument({ uiFontFamily: 'Noto Sans', monoFontFamily: null, uiSmoothing: '', monoSmoothing: '' })
      // Double quotes keep a multi-word family name one CSS token; the stock
      // stack rides along so a missing font degrades to the default, not to
      // a bare serif fallback.
      expect(sansVar()).toBe(`"Noto Sans", ${FONT_SANS_STACK}`)
      expect(monoVar()).toBe('')
    })

    it('writes the mono family independently of the sans one', () => {
      applyFontsToDocument({ uiFontFamily: 'Noto Sans', monoFontFamily: 'JetBrains Mono', uiSmoothing: '', monoSmoothing: '' })
      expect(sansVar()).toBe(`"Noto Sans", ${FONT_SANS_STACK}`)
      expect(monoVar()).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
    })

    it('removes the property when the family is null (default stack applies)', () => {
      applyFontsToDocument({ uiFontFamily: 'Noto Sans', monoFontFamily: 'JetBrains Mono', uiSmoothing: '', monoSmoothing: '' })
      applyFontsToDocument({ uiFontFamily: null, monoFontFamily: 'JetBrains Mono', uiSmoothing: '', monoSmoothing: '' })
      expect(sansVar()).toBe('')
      expect(monoVar()).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
      applyFontsToDocument({ uiFontFamily: null, monoFontFamily: null, uiSmoothing: '', monoSmoothing: '' })
      expect(monoVar()).toBe('')
    })

    it('maps the smoothing modes onto -webkit-font-smoothing values', () => {
      applyFontsToDocument({ uiFontFamily: null, monoFontFamily: null, uiSmoothing: 'grayscale', monoSmoothing: 'subpixel' })
      expect(sansSmoothingVar()).toBe('antialiased')
      expect(monoSmoothingVar()).toBe('subpixel-antialiased')

      applyFontsToDocument({ uiFontFamily: null, monoFontFamily: null, uiSmoothing: 'none', monoSmoothing: 'grayscale' })
      expect(sansSmoothingVar()).toBe('none')
      expect(monoSmoothingVar()).toBe('antialiased')
    })

    it('removes the smoothing property for the "Not set" default', () => {
      applyFontsToDocument({ uiFontFamily: null, monoFontFamily: null, uiSmoothing: 'grayscale', monoSmoothing: 'none' })
      expect(sansSmoothingVar()).toBe('antialiased')
      applyFontsToDocument({ uiFontFamily: null, monoFontFamily: null, uiSmoothing: '', monoSmoothing: '' })
      expect(sansSmoothingVar()).toBe('')
      expect(monoSmoothingVar()).toBe('')
    })

    it('collapses an unrecognized persisted smoothing to "Not set" on apply', () => {
      // A hand-edited localStorage must not inject an arbitrary CSS value:
      // the unknown mode degrades to the default (property removed).
      applyFontsToDocument({
        uiFontFamily: null,
        monoFontFamily: null,
        uiSmoothing: 'url(evil)' as never,
        monoSmoothing: '' ,
      })
      expect(sansSmoothingVar()).toBe('')
      expect(monoSmoothingVar()).toBe('')
    })

    it('canonicalizes a hand-edited family on apply (quotes/backslashes stripped)', () => {
      // Rehydration reaches <html> through applyFontsToDocument without a
      // write action, so a hand-edited localStorage payload must meet the
      // same canonicalization the actions run — the family-side mirror of
      // the smoothing collapse test above.
      applyFontsToDocument({
        uiFontFamily: 'Can"t\\arell',
        monoFontFamily: '"JetBrains Mono"',
        uiSmoothing: '',
        monoSmoothing: '',
      })
      expect(sansVar()).toBe(`"Cantarell", ${FONT_SANS_STACK}`)
      expect(monoVar()).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
    })

    it('canonicalizes a hand-edited family on apply (control characters stripped)', () => {
      // The remaining half of the shared character set: a hand-edited
      // payload carrying control characters must not reach the CSS var —
      // the apply path runs the same stripUnsafeFontNameChars helper as the
      // actions and api/fonts, so all three boundaries agree by construction.
      applyFontsToDocument({
        uiFontFamily: 'Can\u0000tarell',
        monoFontFamily: null,
        uiSmoothing: '',
        monoSmoothing: '',
      })
      expect(sansVar()).toBe(`"Cantarell", ${FONT_SANS_STACK}`)
    })

    it('removes the family property when an apply-side family sanitizes to nothing', () => {
      // A corrupted value of only quotes/backslashes degrades to the default
      // stack (property removed) — never an empty quoted token in the var.
      applyFontsToDocument({ uiFontFamily: '"\\', monoFontFamily: null, uiSmoothing: '', monoSmoothing: '' })
      expect(sansVar()).toBe('')
      // Control characters only: the same collapse.
      applyFontsToDocument({ uiFontFamily: '\t\n\u0000', monoFontFamily: null, uiSmoothing: '', monoSmoothing: '' })
      expect(sansVar()).toBe('')
    })

    it('caps a family at MAX_FONT_NAME_LENGTH on apply', () => {
      applyFontsToDocument({
        uiFontFamily: 'A'.repeat(MAX_FONT_NAME_LENGTH + 50),
        monoFontFamily: null,
        uiSmoothing: '',
        monoSmoothing: '',
      })
      expect(sansVar()).toBe(`"${'A'.repeat(MAX_FONT_NAME_LENGTH)}", ${FONT_SANS_STACK}`)
    })

    it('writes/removes all four properties together (full-set contract)', () => {
      applyFontsToDocument({ uiFontFamily: 'Noto Sans', monoFontFamily: null, uiSmoothing: 'none', monoSmoothing: '' })
      expect(sansVar()).toBe(`"Noto Sans", ${FONT_SANS_STACK}`)
      expect(sansSmoothingVar()).toBe('none')

      // A later call with defaults must not leave stale properties behind.
      applyFontsToDocument({ uiFontFamily: null, monoFontFamily: null, uiSmoothing: '', monoSmoothing: '' })
      expect(sansVar()).toBe('')
      expect(sansSmoothingVar()).toBe('')
    })

    it('is idempotent (safe to call repeatedly)', () => {
      expect(() => {
        applyFontsToDocument({ uiFontFamily: 'Cantarell', monoFontFamily: 'JetBrains Mono', uiSmoothing: 'grayscale', monoSmoothing: 'none' })
        applyFontsToDocument({ uiFontFamily: 'Cantarell', monoFontFamily: 'JetBrains Mono', uiSmoothing: 'grayscale', monoSmoothing: 'none' })
        applyFontsToDocument({ uiFontFamily: null, monoFontFamily: null, uiSmoothing: '', monoSmoothing: '' })
        applyFontsToDocument({ uiFontFamily: null, monoFontFamily: null, uiSmoothing: '', monoSmoothing: '' })
      }).not.toThrow()
      expect(sansVar()).toBe('')
      expect(monoVar()).toBe('')
      expect(sansSmoothingVar()).toBe('')
      expect(monoSmoothingVar()).toBe('')
    })

    it('is a no-op without document', () => {
      const g = globalThis as Record<string, unknown>
      const originalDocument = g.document
      try {
        delete g.document
        expect(() =>
          applyFontsToDocument({ uiFontFamily: 'Noto Sans', monoFontFamily: 'JetBrains Mono', uiSmoothing: 'none', monoSmoothing: '' }),
        ).not.toThrow()
        expect(() => applyFontsToDocument({ uiFontFamily: null, monoFontFamily: null, uiSmoothing: '', monoSmoothing: '' })).not.toThrow()
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

    it('setUIFontFamily strips control characters like the api boundary', () => {
      // The same shared character set api/fonts strips from backend data:
      // a typed/pasted name carrying control characters must not reach the
      // store or the CSS var.
      useFontStore.getState().setUIFontFamily('Can\u0000tarell\u0007')
      expect(useFontStore.getState().uiFontFamily).toBe('Cantarell')
      expect(sansVar()).toBe(`"Cantarell", ${FONT_SANS_STACK}`)
    })

    it('setUIFontFamily collapses an empty/blank family to the default', () => {
      useFontStore.getState().setUIFontFamily('Noto Sans')
      useFontStore.getState().setUIFontFamily('   ')
      expect(useFontStore.getState().uiFontFamily).toBeNull()
      expect(sansVar()).toBe('')
    })

    it('setUIFontFamily caps the stored and applied family at MAX_FONT_NAME_LENGTH', () => {
      useFontStore.getState().setUIFontFamily('A'.repeat(MAX_FONT_NAME_LENGTH + 50))
      expect(useFontStore.getState().uiFontFamily).toHaveLength(MAX_FONT_NAME_LENGTH)
      expect(sansVar()).toBe(`"${'A'.repeat(MAX_FONT_NAME_LENGTH)}", ${FONT_SANS_STACK}`)
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

    it('a family change preserves the already-applied smoothing', () => {
      useFontStore.getState().setUIFontSmoothing('grayscale')
      useFontStore.getState().setMonoFontSmoothing('none')
      useFontStore.getState().setUIFontFamily('Cantarell')
      useFontStore.getState().setMonoFontFamily('JetBrains Mono')
      expect(sansSmoothingVar()).toBe('antialiased')
      expect(monoSmoothingVar()).toBe('none')
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

  describe('smoothing actions', () => {
    it('setUIFontSmoothing applies before set and is independent of mono', () => {
      useFontStore.getState().setUIFontSmoothing('grayscale')
      expect(sansSmoothingVar()).toBe('antialiased')
      expect(monoSmoothingVar()).toBe('')
      expect(useFontStore.getState().uiFontSmoothing).toBe('grayscale')

      useFontStore.getState().setMonoFontSmoothing('none')
      expect(monoSmoothingVar()).toBe('none')
      expect(sansSmoothingVar()).toBe('antialiased')
      expect(useFontStore.getState().monoFontSmoothing).toBe('none')
    })

    it('every mode maps to its -webkit-font-smoothing value', () => {
      const sans = useFontStore.getState()
      sans.setUIFontSmoothing('none')
      expect(sansSmoothingVar()).toBe('none')
      useFontStore.getState().setUIFontSmoothing('subpixel')
      expect(sansSmoothingVar()).toBe('subpixel-antialiased')
      useFontStore.getState().setUIFontSmoothing('grayscale')
      expect(sansSmoothingVar()).toBe('antialiased')
    })

    it('reverting to the default removes the property', () => {
      useFontStore.getState().setUIFontSmoothing('subpixel')
      expect(sansSmoothingVar()).toBe('subpixel-antialiased')
      useFontStore.getState().setUIFontSmoothing('')
      expect(sansSmoothingVar()).toBe('')
      expect(useFontStore.getState().uiFontSmoothing).toBe('')
    })

    it('collapses an unrecognized value to the default', () => {
      useFontStore.getState().setUIFontSmoothing('url(x)' as never)
      expect(useFontStore.getState().uiFontSmoothing).toBe('')
      expect(sansSmoothingVar()).toBe('')
    })
  })

  describe('persistence', () => {
    it('persists exactly the families and smoothing modes under c0wrk-fonts v2', () => {
      useFontStore.getState().setUIFontFamily('Noto Sans')
      useFontStore.getState().setMonoFontFamily('JetBrains Mono')
      useFontStore.getState().setUIFontSmoothing('grayscale')
      useFontStore.getState().setMonoFontSmoothing('none')
      useFontStore.getState().setDetectedFonts('DejaVu Sans', 'Cascadia Code')

      const raw = localStorage.getItem(STORE_KEY)
      expect(raw).not.toBeNull()
      // zustand's persist middleware wraps the payload as {state, version};
      // partialize must keep `state` limited to the four chosen values (the
      // session detections and the actions never hit storage).
      const parsed = JSON.parse(raw as string) as {
        state: Record<string, unknown>
        version: number
      }
      expect(parsed.state).toEqual({
        uiFontFamily: 'Noto Sans',
        monoFontFamily: 'JetBrains Mono',
        uiFontSmoothing: 'grayscale',
        monoFontSmoothing: 'none',
      })
      expect(parsed.version).toBe(2)
    })

    it('rehydrates the persisted values synchronously on first access (startup contract)', async () => {
      // Seed the payload a previous run persisted, then re-import the store
      // module fresh — this is what main.tsx relies on: getState() at module
      // top level already carries the persisted values, so the startup
      // apply lands before React's first render.
      localStorage.setItem(
        STORE_KEY,
        JSON.stringify({
          state: {
            uiFontFamily: 'Cantarell',
            monoFontFamily: 'JetBrains Mono',
            uiFontSmoothing: 'none',
            monoFontSmoothing: 'subpixel',
          },
          version: 2,
        }),
      )
      vi.resetModules()
      const fresh = await import('@/stores/fontStore')

      const s = fresh.useFontStore.getState()
      expect(s.uiFontFamily).toBe('Cantarell')
      expect(s.monoFontFamily).toBe('JetBrains Mono')
      expect(s.uiFontSmoothing).toBe('none')
      expect(s.monoFontSmoothing).toBe('subpixel')
      // Session detections never persist: a fresh launch starts undetected.
      expect(s.detectedUIFamily).toBeNull()
      expect(s.detectedMonoFamily).toBeNull()

      // The startup-apply pattern from main.tsx paints all four overrides.
      fresh.applyFontsToDocument({
        uiFontFamily: s.uiFontFamily,
        monoFontFamily: s.monoFontFamily,
        uiSmoothing: s.uiFontSmoothing,
        monoSmoothing: s.monoFontSmoothing,
      })
      expect(sansVar()).toBe(`"Cantarell", ${FONT_SANS_STACK}`)
      expect(monoVar()).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
      expect(sansSmoothingVar()).toBe('none')
      expect(monoSmoothingVar()).toBe('subpixel-antialiased')
    })

    it('a v1 payload (pre-smoothing) rehydrates with the smoothing defaults', async () => {
      // Additive-field migration: v2 only appended keys, so a v1 payload
      // shallow-merges over the initial state and the new fields fall back
      // to their '' defaults.
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
      expect(s.uiFontSmoothing).toBe('')
      expect(s.monoFontSmoothing).toBe('')
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
