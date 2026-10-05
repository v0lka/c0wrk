import { create } from 'zustand'
import { persist } from 'zustand/middleware'

/**
 * CSS custom property carrying the followed system UI font family on <html>.
 * The name is consumed by the app's base font-family chain; the value is
 * always a double-quoted family name, so names with spaces (e.g.
 * "Noto Sans") stay a single CSS token.
 */
export const SYSTEM_FONT_CSS_VAR = '--default-font-family'

/** Storage key mirrors the persisted-store convention (c0wrk-*). */
const STORAGE_KEY = 'c0wrk-follow-system-font'

interface SystemFontState {
  /** Whether the app follows the desktop environment's UI font. Opt-in
   *  (default off): existing users keep c0wrk's own typeface until they flip
   *  the switch. This is the ONLY persisted field. */
  followSystemFont: boolean
  /** The system font family detected this session (null until a successful
   *  fetch or when the desktop reports none). Session-scoped by design — a
   *  stale persisted family would survive a system-side font change. */
  systemFontFamily: string | null
}

interface SystemFontActions {
  setFollowSystemFont: (follow: boolean) => void
  setSystemFontFamily: (family: string | null) => void
}

/**
 * Writes the followed font family onto <html> as the
 * {@link SYSTEM_FONT_CSS_VAR} custom property, or removes the property when
 * following is off or no family is known. A no-op when the document is
 * unavailable (e.g. during tests). Safe to call repeatedly.
 *
 * Only the family is carried — never a size or weight: c0wrk owns its type
 * scale (14px base + the UI Scale setting), and the mono stack (`font-mono`,
 * the `--font-mono` token) and the icon font (`--font-icon`, SauceCodePro NF)
 * stay untouched.
 */
export function applySystemFontToDocument(follow: boolean, family: string | null): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  if (follow && family !== null) {
    // Double quotes keep multi-word family names one CSS token; the names
    // come from the backend's gsettings parser, which already strips the
    // surrounding quotes GNOME adds, and cannot contain a double quote.
    root.style.setProperty(SYSTEM_FONT_CSS_VAR, `"${family}"`)
  } else {
    root.style.removeProperty(SYSTEM_FONT_CSS_VAR)
  }
}

export const useSystemFontStore = create<SystemFontState & SystemFontActions>()(
  persist(
    (set, get) => ({
      followSystemFont: false,
      systemFontFamily: null,

      setFollowSystemFont: (follow) => {
        // Apply before persisting so the effect on <html> is immediate; the
        // already-known family (from this session's fetch) rides along.
        applySystemFontToDocument(follow, get().systemFontFamily)
        set({ followSystemFont: follow })
      },

      setSystemFontFamily: (family) => {
        // Only takes effect on <html> while following is enabled; the store
        // still records the family either way, so a later toggle-on needs no
        // re-fetch.
        applySystemFontToDocument(get().followSystemFont, family)
        set({ systemFontFamily: family })
      },
    }),
    {
      name: STORAGE_KEY,
      version: 1,
      // Bump version and implement migration when adding/removing/renaming persisted fields.
      migrate: (persistedState, _version) => persistedState,
      // Persist the opt-in flag only: the detected family is session state.
      partialize: (state) => ({ followSystemFont: state.followSystemFont }),
    },
  ),
)
