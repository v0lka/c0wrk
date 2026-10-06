import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { FONT_SANS_STACK, FONT_MONO_STACK, composeFontFamily } from '@/lib/fonts'

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
   *  `--font-sans` stack from index.css, left untouched on <html>. The ONLY
   *  persisted fields of this store are this and {@link monoFontFamily}. */
  uiFontFamily: string | null
  /** The user's monospaced font family. `null` = default index.css stack. */
  monoFontFamily: string | null
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
  setDetectedFonts: (ui: string | null, mono: string | null) => void
}

/**
 * Canonicalizes a family name before it is stored or applied. `null` stays
 * `null` (the "default" sentinel); otherwise quotes and backslashes are
 * stripped (the value is interpolated into a double-quoted CSS token, and a
 * family name legitimately never carries either — the backend's gsettings
 * parser already delivers quote-free names), surrounding whitespace is
 * trimmed, and a survivor that is empty collapses back to `null`.
 */
function normalizeFamily(family: string | null): string | null {
  if (family === null) return null
  const cleaned = family.replace(/["\\]/g, '').trim()
  return cleaned === '' ? null : cleaned
}

/**
 * Writes the font overrides onto <html> as inline {@link FONT_SANS_CSS_VAR} /
 * {@link FONT_MONO_CSS_VAR} custom properties — each the double-quoted
 * family prepended to the stock stack from `lib/fonts.ts` — or removes the
 * property when the family is `null` (the default stack from index.css then
 * applies untouched). A no-op when the document is unavailable (e.g. during
 * tests). Safe to call repeatedly.
 *
 * Only the family is carried — never a size or weight: c0wrk owns its type
 * scale (14px base + the UI Scale setting), and the icon font (`--font-icon`,
 * SauceCodePro NF) stays untouched.
 */
export function applyFontsToDocument(uiFontFamily: string | null, monoFontFamily: string | null): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  if (uiFontFamily !== null) {
    root.style.setProperty(FONT_SANS_CSS_VAR, composeFontFamily(uiFontFamily, FONT_SANS_STACK))
  } else {
    root.style.removeProperty(FONT_SANS_CSS_VAR)
  }
  if (monoFontFamily !== null) {
    root.style.setProperty(FONT_MONO_CSS_VAR, composeFontFamily(monoFontFamily, FONT_MONO_STACK))
  } else {
    root.style.removeProperty(FONT_MONO_CSS_VAR)
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
      detectedUIFamily: null,
      detectedMonoFamily: null,

      setUIFontFamily: (family) => {
        const normalized = normalizeFamily(family)
        // Apply before persisting so the effect on <html> is immediate —
        // there is no apply/save step anywhere in the UI.
        applyFontsToDocument(normalized, get().monoFontFamily)
        set({ uiFontFamily: normalized })
      },

      setMonoFontFamily: (family) => {
        const normalized = normalizeFamily(family)
        applyFontsToDocument(get().uiFontFamily, normalized)
        set({ monoFontFamily: normalized })
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
      version: 1,
      // Bump version and implement migration when adding/removing/renaming persisted fields.
      migrate: (persistedState, _version) => persistedState,
      // Persist the chosen families only: the session detections and the
      // actions never hit storage.
      partialize: (state) => ({ uiFontFamily: state.uiFontFamily, monoFontFamily: state.monoFontFamily }),
    },
  ),
)
