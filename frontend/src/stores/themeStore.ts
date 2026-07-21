import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export type Theme = 'dark' | 'light'

interface ThemeState {
  theme: Theme
}

interface ThemeActions {
  setTheme: (theme: Theme) => void
}

/** Storage key mirrors the persisted-store convention (c0wrk-*). */
const STORAGE_KEY = 'c0wrk-theme'
/** Attribute written to <html>; the CSS overrides key off [data-theme="light"]. */
const DATA_THEME_ATTR = 'data-theme'

/**
 * Writes the data-theme attribute onto <html> and is a no-op when the
 * document is unavailable (e.g. during tests). Safe to call repeatedly.
 */
export function applyThemeToDocument(theme: Theme): void {
  if (typeof document === 'undefined') return
  document.documentElement.setAttribute(DATA_THEME_ATTR, theme)
}

export const useThemeStore = create<ThemeState & ThemeActions>()(
  persist(
    (set) => ({
      theme: 'dark',

      setTheme: (theme) => {
        applyThemeToDocument(theme)
        set({ theme })
      },
    }),
    {
      name: STORAGE_KEY,
      version: 1,
      // Bump version and implement migration when adding/removing/renaming persisted fields.
      migrate: (persistedState, _version) => persistedState,
      partialize: (state) => ({ theme: state.theme }),
    },
  ),
)
