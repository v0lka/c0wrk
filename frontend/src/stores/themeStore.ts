import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { listThemes, type ThemeInfo } from '@/api/themes'

export type ThemeType = 'dark' | 'light'

/** Descriptor used to resolve a theme's dark/light kind. */
export interface ThemeDescriptor {
  id: string
  name: string
  type: ThemeType
}

/** Built-in themes whose palettes live in index.css (no CSS injection). */
export const BUILTIN_THEMES: readonly ThemeDescriptor[] = [
  { id: 'default-dark', name: 'Default Dark', type: 'dark' },
  { id: 'default-light', name: 'Default Light', type: 'light' },
] as const

const DEFAULT_THEME_ID = 'default-dark'

/** Storage key mirrors the persisted-store convention (c0wrk-*). */
const STORAGE_KEY = 'c0wrk-theme'
/** Kind attribute on <html>: always the dark/light TYPE (built-ins and
 *  custom themes alike) so native controls/scrollbars and the One Light
 *  override block resolve from the type, never from a custom slug. */
const DATA_THEME_ATTR = 'data-theme'
/** Identity attribute for the ACTIVE CUSTOM THEME id ('' removes it). Custom
 *  CSS is injected scoped to :root:root[data-custom-theme="<id>"] so no custom
 *  theme can ever collide with the built-in attribute namespaces, and the
 *  doubled :root keeps the specificity above the built-in light override. */
const DATA_CUSTOM_THEME_ATTR = 'data-custom-theme'
/** Id of the single <style> element that carries the active custom theme. */
export const CUSTOM_THEME_STYLE_ID = 'c0wrk-custom-theme'

/** Coerces a backend theme `type` string to the frontend dark/light union. */
export function themeTypeOf(t: { type: string }): ThemeType {
  return t.type === 'light' ? 'light' : 'dark'
}

/** Derives the dark/light kind for a persisted theme when the store predates
 *  the v3 `themeType` field: built-ins carry their type; a custom theme is
 *  sniffed from a `color-scheme:` literal in its cached CSS, else dark. */
export function deriveThemeType(themeId: string, css: string): ThemeType {
  const builtin = BUILTIN_THEMES.find((b) => b.id === themeId)
  if (builtin) return builtin.type
  const m = /color-scheme:\s*(dark|light)/i.exec(css)
  return m ? (m[1]!.toLowerCase() as ThemeType) : 'dark'
}

function isBuiltinId(id: string): boolean {
  return BUILTIN_THEMES.some((b) => b.id === id)
}

/**
 * Re-scopes a sanitized theme body to the active custom id: every `:root`
 * selector in the CSS becomes `:root:root[data-custom-theme="<id>"]`. The
 * backend sanitizer guarantees the document is a single canonical `:root { … }`
 * rule (plus the header comment), so the replace covers exactly that selector.
 *
 * The doubled `:root:root` is deliberate: the scoped selector must outrank
 * the UNLAYERED `:root[data-theme="light"]` One Light override in index.css
 * (specificity (0,2,0)) for light-type custom themes, regardless of where
 * the injected <style> sits relative to the app stylesheet — the pre-paint
 * script creates the element before the app <link> is parsed. (0,3,0) wins
 * over (0,2,0) at equal layer standing no matter the document order.
 */
export function scopeThemeCSS(themeId: string, css: string): string {
  return css.split(':root').join(`:root:root[${DATA_CUSTOM_THEME_ATTR}="${themeId}"]`)
}

function removeCustomThemeStyle(): void {
  document.getElementById(CUSTOM_THEME_STYLE_ID)?.remove()
}

/**
 * Applies a theme to the document.
 *
 * `data-theme` always carries the dark/light TYPE — the type drives native
 * form controls/scrollbars and matches the built-in One Light override block
 * without giving a custom slug a way to alias `light`/`dark`.
 *
 * Built-in themes remove the custom attributes/style. Custom themes write
 * `<html data-custom-theme="<id>">` and inject the theme CSS scoped to that
 * attribute into a single `<style id="c0wrk-custom-theme">` element at the
 * END of <head> (after the app stylesheet — equal-specificity rules resolve
 * by document order, and the scoped selector itself carries extra
 * specificity over the built-in light override; see scopeThemeCSS). A no-op
 * when the document is unavailable (e.g. during tests).
 */
export function applyThemeToDocument(themeId: string, css: string, type: ThemeType): void {
  if (typeof document === 'undefined') return
  const builtin = BUILTIN_THEMES.find((b) => b.id === themeId)
  if (builtin) {
    document.documentElement.setAttribute(DATA_THEME_ATTR, builtin.type)
    document.documentElement.removeAttribute(DATA_CUSTOM_THEME_ATTR)
    removeCustomThemeStyle()
    return
  }
  document.documentElement.setAttribute(DATA_THEME_ATTR, type)
  document.documentElement.setAttribute(DATA_CUSTOM_THEME_ATTR, themeId)
  let style = document.getElementById(CUSTOM_THEME_STYLE_ID)
  if (!style) {
    style = document.createElement('style')
    style.id = CUSTOM_THEME_STYLE_ID
    document.head.appendChild(style)
  } else {
    // The pre-paint script (public/prepaint-theme.js) creates this element
    // during HTML parse — before the app stylesheet <link> exists. Reuse
    // must also RE-HOME it to the end of <head> so the injected CSS comes
    // after the app stylesheet in document order (equal-specificity rules
    // resolve by order). appendChild moves an existing node, never clones.
    document.head.appendChild(style)
  }
  style.textContent = scopeThemeCSS(themeId, css)
}

interface ThemeState {
  /** Active theme id — a BUILTIN_THEMES id or a custom theme id. */
  themeId: string
  /** Cached CSS of the active custom theme ('' for built-ins). Persisted so
   *  the pre-paint apply can inject it without an RPC. */
  themeCss: string
  /** Dark/light kind of the active theme. Persisted since v3 so the
   *  pre-paint apply needs no catalog round-trip. */
  themeType: ThemeType
  /** Installed custom themes. Not persisted — refetched via loadThemes(). */
  customThemes: ThemeInfo[]
}

interface ThemeActions {
  /** Activate a theme and cache its CSS + type (custom themes only). */
  setTheme: (id: string, css?: string, type?: ThemeType) => void
  /** Replace the custom-theme catalog; resets to Default Dark when the
   *  active custom theme disappears from the list. */
  applyThemes: (list: ThemeInfo[]) => void
  /** Fetch the backend catalog into applyThemes. Safe to call repeatedly. */
  loadThemes: () => Promise<void>
}

export type ThemeStore = ThemeState & ThemeActions

/**
 * Selector helper for consumers that only need the dark/light kind of the
 * active theme (CodeMirror/mermaid/xterm theming). Returns a primitive, so
 * it is referentially stable across renders by construction.
 */
export function selectActiveThemeType(s: Pick<ThemeStore, 'themeId' | 'themeType' | 'customThemes'>): ThemeType {
  const builtin = BUILTIN_THEMES.find((b) => b.id === s.themeId)
  if (builtin) return builtin.type
  if (s.themeType) return s.themeType
  const custom = s.customThemes.find((t) => t.id === s.themeId)
  return custom ? themeTypeOf(custom) : 'dark'
}

/**
 * Selector helper for consumers that must re-resolve baked palettes when the
 * active theme changes IDENTITY (terminal/CodeMirror theming). A same-type
 * custom-theme switch (dark→dark) leaves selectActiveThemeType's value
 * unchanged but always changes the id — and the CSS custom properties those
 * palettes are resolved from DO change (the injected custom-theme CSS swaps),
 * so the re-resolution must be keyed on this primitive. Returns the raw
 * store string, so it is referentially stable across renders by construction.
 */
export function selectActiveThemeId(s: Pick<ThemeStore, 'themeId'>): string {
  return s.themeId
}

export const useThemeStore = create<ThemeStore>()(
  persist(
    (set, get) => ({
      themeId: DEFAULT_THEME_ID,
      themeCss: '',
      themeType: 'dark',
      customThemes: [],

      setTheme: (id, css, type) => {
        const current = get()
        // Built-ins have no CSS. A custom theme takes the explicit css
        // argument (the activator knows the theme body); re-selecting the
        // same custom theme without one keeps its cache. Switching to a
        // DIFFERENT custom theme without css cannot reuse the old theme's
        // body — cache stays empty until a css-carrying activation.
        let nextCss: string
        if (isBuiltinId(id)) nextCss = ''
        else if (css !== undefined) nextCss = css
        else if (current.themeId === id) nextCss = current.themeCss
        else nextCss = ''
        const builtin = BUILTIN_THEMES.find((b) => b.id === id)
        const nextType: ThemeType =
          builtin?.type ?? type ?? (current.themeId === id ? current.themeType : undefined) ??
          (nextCss ? deriveThemeType(id, nextCss) : 'dark')
        applyThemeToDocument(id, nextCss, nextType)
        set({ themeId: id, themeCss: nextCss, themeType: nextType })
      },

      applyThemes: (list) => {
        // Defensive: never let backend data shadow the built-in ids.
        const customs = list.filter((t) => !isBuiltinId(t.id))
        const current = get()
        const activeGone =
          !isBuiltinId(current.themeId) &&
          !customs.some((t) => t.id === current.themeId)
        if (activeGone) {
          // The active custom theme was deleted — fall back to Default Dark
          // and drop the stale CSS cache.
          applyThemeToDocument(DEFAULT_THEME_ID, '', 'dark')
          set({ customThemes: customs, themeId: DEFAULT_THEME_ID, themeCss: '', themeType: 'dark' })
          return
        }
        set({ customThemes: customs })
        // Re-apply the active custom theme now that its descriptor is known:
        // the pre-paint pass may have sniffed color-scheme from the CSS only.
        if (!isBuiltinId(current.themeId) && current.themeCss) {
          const live = customs.find((t) => t.id === current.themeId)
          const nextType = live ? themeTypeOf(live) : current.themeType
          applyThemeToDocument(current.themeId, current.themeCss, nextType)
          if (nextType !== current.themeType) set({ themeType: nextType })
        }
      },

      loadThemes: async () => {
        try {
          get().applyThemes(await listThemes())
        } catch {
          // Backend not ready / unavailable — the built-ins keep working and
          // the caller (App startup) does not surface theme-list errors.
          // listThemes already logged the underlying error.
        }
      },
    }),
    {
      name: STORAGE_KEY,
      version: 3,
      migrate: (persistedState, version) => {
        const legacy = (persistedState ?? {}) as {
          theme?: unknown
          themeId?: unknown
          themeCss?: unknown
        }
        if (version < 2) {
          // v1 stored { theme: 'dark' | 'light' }.
          const wasLight = legacy.theme === 'light'
          return {
            themeId: wasLight ? 'default-light' : DEFAULT_THEME_ID,
            themeCss: '',
            themeType: wasLight ? 'light' : 'dark',
          }
        }
        // v2 stored { themeId, themeCss } without themeType — derive it.
        const themeId = typeof legacy.themeId === 'string' ? legacy.themeId : DEFAULT_THEME_ID
        const themeCss = typeof legacy.themeCss === 'string' ? legacy.themeCss : ''
        return { themeId, themeCss, themeType: deriveThemeType(themeId, themeCss) }
      },
      partialize: (state) => ({
        themeId: state.themeId,
        themeCss: state.themeCss,
        themeType: state.themeType,
      }),
    },
  ),
)
