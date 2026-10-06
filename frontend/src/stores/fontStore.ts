import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { FONT_SANS_STACK, FONT_MONO_STACK, composeFontFamily, MAX_FONT_NAME_LENGTH, stripUnsafeFontNameChars } from '@/lib/fonts'

/**
 * CSS custom properties carrying the user's font-family overrides on <html>.
 * Both names are the exact `@theme` tokens of `frontend/src/index.css`
 * (`--font-sans` — the Tailwind `font-sans` utility and the `html` base via
 * preflight — and `--font-mono` — the `font-mono` utility). An inline
 * declaration on `<html>` beats the `:root` theme rule for the same element
 * in the cascade, so the override needs no `index.css` change. The value
 * always leads with a double-quoted family name followed by the DEFAULT
 * stack from `lib/fonts.ts`, so per-family fallbacks survive (a missing
 * user font degrades to the stock stack, never to a bare serif default).
 */
export const FONT_SANS_CSS_VAR = '--font-sans'
export const FONT_MONO_CSS_VAR = '--font-mono'

/**
 * CSS custom properties carrying the smoothing overrides consumed by the
 * font-smoothing rules in index.css (`html` for sans, the monospace-surface
 * rule list for mono) through `var(..., auto)`: an absent property resolves
 * to the `auto` fallback — the webview default, which is exactly the "Not
 * set" behavior — so the store removes the property instead of ever writing
 * a sentinel value.
 */
export const FONT_SMOOTHING_SANS_CSS_VAR = '--font-smoothing-sans'
export const FONT_SMOOTHING_MONO_CSS_VAR = '--font-smoothing-mono'

/**
 * A font smoothing (anti-aliasing) preference. The empty string is the "Not
 * set" default — the webview's own text rendering is left untouched. The
 * three modes map onto the non-standard `-webkit-font-smoothing` property,
 * the only CSS-level AA lever a webview exposes; where the webview ignores
 * the property the knob renders as a harmless no-op (see {@link smoothingToCss}).
 */
export type FontSmoothingSetting = '' | 'none' | 'grayscale' | 'subpixel'

const SMOOTHING_SETTINGS: readonly FontSmoothingSetting[] = ['', 'none', 'grayscale', 'subpixel']

/** Collapses anything unrecognized (a hand-edited localStorage) to "Not set". */
function normalizeSmoothing(smoothing: string | null): FontSmoothingSetting {
  return SMOOTHING_SETTINGS.includes(smoothing as FontSmoothingSetting)
    ? (smoothing as FontSmoothingSetting)
    : ''
}

/**
 * The `-webkit-font-smoothing` value for a setting; `null` = write nothing
 * (the "Not set" default — the index.css `var(..., auto)` fallback applies).
 */
export function smoothingToCss(smoothing: FontSmoothingSetting): string | null {
  switch (smoothing) {
    case 'none':
      return 'none'
    case 'grayscale':
      return 'antialiased'
    case 'subpixel':
      return 'subpixel-antialiased'
    default:
      return null
  }
}

/** Storage key mirrors the persisted-store convention (c0wrk-*). */
const STORAGE_KEY = 'c0wrk-fonts'

/**
 * Key of the replaced opt-in "follow system font" store whose state model
 * this store supersedes. Its boolean payload does not map onto the new
 * chosen-family model, so the key is retired on startup instead of migrated —
 * `removeOrphanSystemFontKey`.
 */
const LEGACY_STORAGE_KEY = 'c0wrk-follow-system-font'

interface FontState {
  /** The user's UI (sans) font family. `null` = c0wrk's default — the
   *  `--font-sans` stack from index.css, left untouched on <html>. */
  uiFontFamily: string | null
  /** The user's monospaced font family. `null` = default index.css stack. */
  monoFontFamily: string | null
  /** The user's smoothing preference for the UI (sans) font. `''` = "Not
   *  set" — the webview's default anti-aliasing applies. Persisted. */
  uiFontSmoothing: FontSmoothingSetting
  /** The user's smoothing preference for the monospaced surfaces. `''` =
   *  "Not set". Persisted. */
  monoFontSmoothing: FontSmoothingSetting
  /** The desktop environment's UI font family detected this session (null
   *  until a successful fetch or when the desktop reports none).
   *  Session-scoped by design — a stale persisted detection would survive a
   *  system-side font change. Never persisted; never applied to <html> by
   *  itself — it is a candidate the user picks (fed once per launch by
   *  `useSystemFonts`). */
  detectedUIFamily: string | null
  /** The desktop's monospaced font family detected this session.
   *  Session-scoped, never persisted, never applied by itself. */
  detectedMonoFamily: string | null
}

interface FontActions {
  setUIFontFamily: (family: string | null) => void
  setMonoFontFamily: (family: string | null) => void
  setUIFontSmoothing: (smoothing: FontSmoothingSetting) => void
  setMonoFontSmoothing: (smoothing: FontSmoothingSetting) => void
  setDetectedFonts: (ui: string | null, mono: string | null) => void
}

/**
 * Canonicalizes a family name before it is stored or applied. `null` stays
 * `null` (the "default" sentinel); otherwise the shared unsafe character set
 * is stripped (quotes and backslashes — the value is interpolated into a
 * double-quoted CSS token — and control characters, via the same
 * `stripUnsafeFontNameChars` helper api/fonts runs, so pasted or hand-edited
 * input is normalized identically to backend data), surrounding whitespace
 * is trimmed, a survivor that is empty collapses back to `null`, and the
 * survivor is capped at the shared MAX_FONT_NAME_LENGTH (lib/fonts) so a
 * corrupted store payload cannot smuggle an unbounded value into CSS.
 */
function normalizeFamily(family: string | null): string | null {
  if (family === null) return null
  const cleaned = stripUnsafeFontNameChars(family).trim()
  if (cleaned === '') return null
  return cleaned.slice(0, MAX_FONT_NAME_LENGTH)
}

/**
 * The full override set painted onto <html>. A `null` family or an `''`
 * smoothing means "leave the webview/index.css default untouched" — the
 * property is removed, never overwritten with a sentinel.
 */
export interface FontOverrides {
  uiFontFamily: string | null
  monoFontFamily: string | null
  uiSmoothing: FontSmoothingSetting
  monoSmoothing: FontSmoothingSetting
}

/**
 * Writes the font overrides onto <html> as inline custom properties — the
 * double-quoted family prepended to the stock stack from `lib/fonts.ts`,
 * and the `-webkit-font-smoothing` modes — or removes a property when its
 * value is the default (`null` family / `''` smoothing), so index.css's
 * untouched rules apply. A no-op when the document is unavailable (e.g.
 * during tests). Safe to call repeatedly. Every caller passes the FULL
 * override set: the four properties are written or removed together, so a
 * partial apply would reset the untouched knobs to their defaults.
 *
 * Only families and smoothing are carried — never a size or weight: c0wrk
 * owns its type scale (14px base + the UI Scale setting), and the icon font
 * (`--font-icon`, SauceCodePro NF) stays untouched.
 */
export function applyFontsToDocument(overrides: FontOverrides): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  // Families are canonicalized at this boundary too, mirroring the smoothing
  // normalization below: rehydration (a hand-edited localStorage payload)
  // reaches <html> through this function without passing a write action, so
  // the same shared normalization (stripUnsafeFontNameChars + length cap)
  // must hold here.
  const uiFamily = normalizeFamily(overrides.uiFontFamily)
  if (uiFamily !== null) {
    root.style.setProperty(FONT_SANS_CSS_VAR, composeFontFamily(uiFamily, FONT_SANS_STACK))
  } else {
    root.style.removeProperty(FONT_SANS_CSS_VAR)
  }
  const monoFamily = normalizeFamily(overrides.monoFontFamily)
  if (monoFamily !== null) {
    root.style.setProperty(FONT_MONO_CSS_VAR, composeFontFamily(monoFamily, FONT_MONO_STACK))
  } else {
    root.style.removeProperty(FONT_MONO_CSS_VAR)
  }
  const sansSmoothing = smoothingToCss(normalizeSmoothing(overrides.uiSmoothing))
  if (sansSmoothing !== null) {
    root.style.setProperty(FONT_SMOOTHING_SANS_CSS_VAR, sansSmoothing)
  } else {
    root.style.removeProperty(FONT_SMOOTHING_SANS_CSS_VAR)
  }
  const monoSmoothing = smoothingToCss(normalizeSmoothing(overrides.monoSmoothing))
  if (monoSmoothing !== null) {
    root.style.setProperty(FONT_SMOOTHING_MONO_CSS_VAR, monoSmoothing)
  } else {
    root.style.removeProperty(FONT_SMOOTHING_MONO_CSS_VAR)
  }
}

/**
 * Removes the persisted key of the replaced follow-system-font store. Called
 * once from `main.tsx` at startup: its boolean flag has no
 * successor in this store's chosen-family model, so the value is retired
 * rather than migrated, and a stale key must not linger in localStorage.
 * A no-op without a storage (or when the key is already gone); storage
 * failures (privacy modes) are swallowed — the cleanup is best-effort.
 */
export function removeOrphanSystemFontKey(): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.removeItem(LEGACY_STORAGE_KEY)
  } catch {
    // Unavailable storage must never break startup.
  }
}

export const useFontStore = create<FontState & FontActions>()(
  persist(
    (set, get) => ({
      uiFontFamily: null,
      monoFontFamily: null,
      uiFontSmoothing: '',
      monoFontSmoothing: '',
      detectedUIFamily: null,
      detectedMonoFamily: null,

      setUIFontFamily: (family) => {
        const normalized = normalizeFamily(family)
        // Apply before persisting so the effect on <html> is immediate —
        // there is no apply/save step anywhere in the UI. The full override
        // set is always passed (see applyFontsToDocument).
        applyFontsToDocument({
          uiFontFamily: normalized,
          monoFontFamily: get().monoFontFamily,
          uiSmoothing: get().uiFontSmoothing,
          monoSmoothing: get().monoFontSmoothing,
        })
        set({ uiFontFamily: normalized })
      },

      setMonoFontFamily: (family) => {
        const normalized = normalizeFamily(family)
        applyFontsToDocument({
          uiFontFamily: get().uiFontFamily,
          monoFontFamily: normalized,
          uiSmoothing: get().uiFontSmoothing,
          monoSmoothing: get().monoFontSmoothing,
        })
        set({ monoFontFamily: normalized })
      },

      setUIFontSmoothing: (smoothing) => {
        applyFontsToDocument({
          uiFontFamily: get().uiFontFamily,
          monoFontFamily: get().monoFontFamily,
          uiSmoothing: normalizeSmoothing(smoothing),
          monoSmoothing: get().monoFontSmoothing,
        })
        set({ uiFontSmoothing: normalizeSmoothing(smoothing) })
      },

      setMonoFontSmoothing: (smoothing) => {
        applyFontsToDocument({
          uiFontFamily: get().uiFontFamily,
          monoFontFamily: get().monoFontFamily,
          uiSmoothing: get().uiFontSmoothing,
          monoSmoothing: normalizeSmoothing(smoothing),
        })
        set({ monoFontSmoothing: normalizeSmoothing(smoothing) })
      },

      // Detection only records the session candidates — it deliberately never
      // touches <html>: what is rendered is decided solely by the persisted
      // chosen families, so a detection arriving late cannot repaint text.
      // One atomic action for the whole discovery result (fed once per launch
      // by `useSystemFonts`): a single `set` means a single store notification.
      setDetectedFonts: (ui, mono) => {
        set({ detectedUIFamily: normalizeFamily(ui), detectedMonoFamily: normalizeFamily(mono) })
      },
    }),
    {
      name: STORAGE_KEY,
      // v2 added the two smoothing fields — additive: a v1 payload without
      // them shallow-merges over the '' defaults, so there is no value
      // migration to run.
      version: 2,
      migrate: (persistedState, _version) => persistedState,
      // Persist the chosen families and smoothing modes only: the session
      // detections and the actions never hit storage.
      partialize: (state) => ({
        uiFontFamily: state.uiFontFamily,
        monoFontFamily: state.monoFontFamily,
        uiFontSmoothing: state.uiFontSmoothing,
        monoFontSmoothing: state.monoFontSmoothing,
      }),
    },
  ),
)
