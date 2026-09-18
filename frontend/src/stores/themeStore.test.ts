// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// jsdom in this environment does not expose `window.localStorage`, which
// zustand's `persist` middleware captures at store-creation time (via
// createJSONStorage(() => window.localStorage)). Install an in-memory
// polyfill before any store module is imported so themeStore works.
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
  useThemeStore,
  applyThemeToDocument,
  selectActiveThemeType,
  selectActiveThemeId,
  BUILTIN_THEMES,
  type ThemeStore,
} from '@/stores/themeStore'
import type { ThemeInfo } from '@/api/themes'

// Mock the RPC layer: loadThemes() must not touch window.go in tests.
vi.mock('@/api/themes', () => ({
  listThemes: vi.fn().mockResolvedValue([]),
}))

const customLight: ThemeInfo = { id: 'nord', name: 'Nord', type: 'light' }
const customDark: ThemeInfo = { id: 'gruvbox', name: 'Gruvbox', type: 'dark' }
const NORD_CSS = ':root{--color-background:#2e3440;--color-foreground:#d8dee9;color-scheme:light}'

/** Re-run the persist rehydration against whatever localStorage currently
 *  holds — the same path zustand runs at store creation. Used to exercise
 *  the v1→v2 migrate() with a legacy persisted payload. */
function rehydrate(): void {
  const api = useThemeStore.persist
  const opts = api.getOptions()
  const stored = localStorage.getItem(opts.name as string)
  const persisted = stored ? (JSON.parse(stored) as { state?: unknown; version?: number }) : undefined
  if (!persisted) return
  void api.rehydrate()
}

describe('themeStore', () => {
  beforeEach(() => {
    // Reset to defaults and clear any persisted state between tests.
    useThemeStore.setState({
      themeId: 'default-dark',
      themeCss: '',
      themeType: 'dark',
      customThemes: [],
    })
    localStorage.clear()
    document.documentElement.removeAttribute('data-theme')
    document.documentElement.removeAttribute('data-custom-theme')
    document.getElementById('c0wrk-custom-theme')?.remove()
  })

  afterEach(() => {
    document.getElementById('c0wrk-custom-theme')?.remove()
  })

  // ── v1 → v2 migration ─────────────────────────────────────────────

  it('migrates a v1 {theme:"light"} payload to themeId default-light', () => {
    localStorage.setItem(
      'c0wrk-theme',
      JSON.stringify({ state: { theme: 'light' }, version: 1 }),
    )
    rehydrate()
    expect(useThemeStore.getState().themeId).toBe('default-light')
    expect(useThemeStore.getState().themeCss).toBe('')
    // main.tsx applies the rehydrated state before first paint — the same
    // chain must land on data-theme="light" for a migrated light user.
    const s = useThemeStore.getState()
    applyThemeToDocument(s.themeId, s.themeCss, s.themeType)
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('migrates a v1 {theme:"dark"} payload to themeId default-dark', () => {
    localStorage.setItem(
      'c0wrk-theme',
      JSON.stringify({ state: { theme: 'dark' }, version: 1 }),
    )
    rehydrate()
    expect(useThemeStore.getState().themeId).toBe('default-dark')
  })

  it('migrates a v1 payload with unknown theme value to default-dark', () => {
    localStorage.setItem(
      'c0wrk-theme',
      JSON.stringify({ state: { theme: 'gruvbox' }, version: 1 }),
    )
    rehydrate()
    expect(useThemeStore.getState().themeId).toBe('default-dark')
  })

  // ── data-theme application ────────────────────────────────────────

  it('defaults to default-dark', () => {
    expect(useThemeStore.getState().themeId).toBe('default-dark')
  })

  it('setTheme updates the store state and applies data-theme for built-ins', () => {
    const { setTheme } = useThemeStore.getState()
    setTheme('default-light')
    expect(useThemeStore.getState().themeId).toBe('default-light')
    expect(useThemeStore.getState().themeCss).toBe('')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    setTheme('default-dark')
    expect(useThemeStore.getState().themeId).toBe('default-dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('setTheme applies a custom theme: type on data-theme, id on data-custom-theme, scoped CSS', () => {
    const { setTheme } = useThemeStore.getState()
    setTheme('nord', NORD_CSS, 'light')
    expect(useThemeStore.getState().themeId).toBe('nord')
    expect(useThemeStore.getState().themeCss).toBe(NORD_CSS)
    expect(useThemeStore.getState().themeType).toBe('light')
    // The KIND attribute carries the type, never the custom slug.
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    expect(document.documentElement.getAttribute('data-custom-theme')).toBe('nord')

    const styles = document.querySelectorAll('style#c0wrk-custom-theme')
    expect(styles.length).toBe(1)
    const style = styles[0] as HTMLStyleElement
    // The doubled :root:root keeps the scoped selector above the unlayered
    // :root[data-theme="light"] override regardless of document order.
    expect(style.textContent).toContain(':root:root[data-custom-theme="nord"]{--color-background:#2e3440')
    expect(style.parentElement).toBe(document.head)
  })

  it('setTheme derives the type from color-scheme when not given (v2 activation shape)', () => {
    const { setTheme } = useThemeStore.getState()
    setTheme('nord', NORD_CSS)
    expect(useThemeStore.getState().themeType).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('a custom slug can never alias the builtin data-theme keys', () => {
    const { setTheme } = useThemeStore.getState()
    setTheme('light', ':root{--color-background:#fff;--color-foreground:#000}')
    // The kind attribute stays a type token even for a slug named 'light'.
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(document.documentElement.getAttribute('data-custom-theme')).toBe('light')
  })

  it('switching back to a built-in removes the injected style element', () => {
    const { setTheme } = useThemeStore.getState()
    setTheme('nord', NORD_CSS)
    expect(document.getElementById('c0wrk-custom-theme')).toBeTruthy()
    setTheme('default-dark')
    expect(document.getElementById('c0wrk-custom-theme')).toBeNull()
    expect(useThemeStore.getState().themeCss).toBe('')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(document.documentElement.hasAttribute('data-custom-theme')).toBe(false)
  })

  it('re-applying a custom theme reuses the single style element', () => {
    const { setTheme } = useThemeStore.getState()
    setTheme('nord', NORD_CSS)
    setTheme('gruvbox', ':root{--color-background:#282828;--color-foreground:#ebdbb2}')
    const styles = document.querySelectorAll('style#c0wrk-custom-theme')
    expect(styles.length).toBe(1)
    expect(styles[0]?.textContent).toContain(':root:root[data-custom-theme="gruvbox"]{--color-background:#282828')
  })

  it('a light custom theme outranks the One Light override regardless of document order', () => {
    // Regression: the scoped selector must carry specificity (0,3,0) —
    // :root:root[data-custom-theme] — because index.css ships an UNLAYERED
    // :root[data-theme="light"] override (0,2,0) and the pre-paint script
    // injects this <style> BEFORE the app stylesheet <link> exists. Equal
    // specificity would resolve by document order and a light custom theme
    // would render as One Light. jsdom does not compute the cascade, so the
    // guard pins the selector shape that wins by construction.
    const { setTheme } = useThemeStore.getState()
    setTheme('nord', NORD_CSS, 'light')
    const css = document.getElementById('c0wrk-custom-theme')?.textContent ?? ''
    // The doubled form is the whole point: if this regresses to a single
    // :root[, the contains below fails and the specificity guard is gone.
    expect(css).toContain(':root:root[data-custom-theme="nord"]{--color-background:#2e3440')
  })

  it('re-applying a custom theme re-homes the style element to the end of head', () => {
    // Regression: the pre-paint script creates #c0wrk-custom-theme during
    // HTML parse — before the app stylesheet <link> is parsed. When
    // applyThemeToDocument reuses that early element it must move it to the
    // END of <head>, so the injected CSS follows the app stylesheet in
    // document order (the tie-breaker for equal-specificity rules).
    const early = document.createElement('style')
    early.id = 'c0wrk-custom-theme'
    document.head.appendChild(early) // sits before the link the app will add
    const link = document.createElement('link')
    link.rel = 'stylesheet'
    document.head.appendChild(link)
    expect(document.head.lastElementChild).toBe(link)

    const { setTheme } = useThemeStore.getState()
    setTheme('nord', NORD_CSS, 'light')

    const styles = document.querySelectorAll('style#c0wrk-custom-theme')
    expect(styles.length).toBe(1)
    expect(styles[0]).toBe(early) // reused, not duplicated
    expect(document.head.lastElementChild).toBe(early) // moved after the link
    expect(link.compareDocumentPosition(early) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })

  it('applyThemeToDocument is a no-op without a document', () => {
    const doc = globalThis.document
    // @ts-expect-error simulate a non-DOM environment (SSR/tests)
    delete globalThis.document
    expect(() => applyThemeToDocument('default-light', '', 'light')).not.toThrow()
    globalThis.document = doc
  })

  // ── applyThemes catalog updates ───────────────────────────────────

  it('applyThemes updates customThemes and keeps the active builtin theme', () => {
    const { applyThemes } = useThemeStore.getState()
    applyThemes([customLight, customDark])
    expect(useThemeStore.getState().customThemes.map((t) => t.id)).toEqual(['nord', 'gruvbox'])
    expect(useThemeStore.getState().themeId).toBe('default-dark')
  })

  it('applyThemes resets to default-dark when the active custom theme disappears', () => {
    const { setTheme, applyThemes } = useThemeStore.getState()
    setTheme('nord', NORD_CSS)
    applyThemes([customDark]) // nord deleted on disk
    expect(useThemeStore.getState().themeId).toBe('default-dark')
    expect(useThemeStore.getState().themeCss).toBe('')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(document.getElementById('c0wrk-custom-theme')).toBeNull()
  })

  it('applyThemes keeps an active custom theme that is still present', () => {
    const { setTheme, applyThemes } = useThemeStore.getState()
    setTheme('nord', NORD_CSS)
    applyThemes([customLight, customDark])
    expect(useThemeStore.getState().themeId).toBe('nord')
    expect(useThemeStore.getState().themeCss).toBe(NORD_CSS)
    expect(useThemeStore.getState().themeType).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    expect(document.documentElement.getAttribute('data-custom-theme')).toBe('nord')
  })

  it('loadThemes fetches the catalog through listThemes', async () => {
    const { listThemes } = await import('@/api/themes')
    const mock = vi.mocked(listThemes)
    mock.mockResolvedValueOnce([customLight])
    await useThemeStore.getState().loadThemes()
    expect(mock).toHaveBeenCalledTimes(1)
    expect(useThemeStore.getState().customThemes).toEqual([customLight])
  })

  it('loadThemes swallows backend errors', async () => {
    const { listThemes } = await import('@/api/themes')
    const mock = vi.mocked(listThemes)
    mock.mockRejectedValueOnce(new Error('backend not ready'))
    await expect(useThemeStore.getState().loadThemes()).resolves.toBeUndefined()
    expect(useThemeStore.getState().themeId).toBe('default-dark')
  })

  // ── selectActiveThemeType ─────────────────────────────────────────

  it('selectActiveThemeType resolves builtin and custom descriptors', () => {
    useThemeStore.setState({ themeId: 'default-light', customThemes: [] })
    expect(selectActiveThemeType(useThemeStore.getState())).toBe('light')
    useThemeStore.setState({ themeId: 'nord', themeType: 'light', customThemes: [customLight, customDark] })
    expect(selectActiveThemeType(useThemeStore.getState())).toBe('light')
    useThemeStore.setState({ themeId: 'gruvbox', themeType: 'dark', customThemes: [customLight, customDark] })
    expect(selectActiveThemeType(useThemeStore.getState())).toBe('dark')
  })

  it('selectActiveThemeType falls back to dark for an unknown active id', () => {
    useThemeStore.setState({ themeId: 'missing', customThemes: [] })
    expect(selectActiveThemeType(useThemeStore.getState())).toBe('dark')
  })

  it('selectActiveThemeType returns a referentially stable primitive', () => {
    useThemeStore.setState({ themeId: 'nord', customThemes: [customLight] })
    const s = useThemeStore.getState() as ThemeStore
    expect(selectActiveThemeType(s)).toBe(selectActiveThemeType(s))
    // Same descriptor shape, different object identity → same primitive.
    const s2 = { ...s, customThemes: [{ ...customLight }] }
    expect(selectActiveThemeType(s2)).toBe(selectActiveThemeType(s))
  })

  // ── selectActiveThemeId ───────────────────────────────────────────

  // Regression (finding 44): the terminal/editor palettes re-resolve keyed on
  // the identity selector. Switching between two same-type custom themes must
  // change its value (the type selector's value does NOT change there), or
  // the baked palettes would keep the first theme's colours.
  it('selectActiveThemeId changes on a same-type custom-theme switch while the type stays put', () => {
    const { setTheme } = useThemeStore.getState()
    setTheme('gruvbox', ':root{--color-background:#282828;--color-foreground:#ebdbb2}')
    expect(selectActiveThemeId(useThemeStore.getState())).toBe('gruvbox')
    expect(selectActiveThemeType(useThemeStore.getState())).toBe('dark')

    // A second DARK custom theme replaces the first: the type selector is
    // Object.is-equal (dark→dark) but the identity selector moves.
    setTheme('tokyo-night', ':root{--color-background:#1a1b26;--color-foreground:#c0caf5}')
    expect(selectActiveThemeType(useThemeStore.getState())).toBe('dark')
    expect(selectActiveThemeId(useThemeStore.getState())).toBe('tokyo-night')
    expect(selectActiveThemeId(useThemeStore.getState())).not.toBe('gruvbox')
  })

  it('selectActiveThemeId returns a referentially stable primitive', () => {
    useThemeStore.setState({ themeId: 'nord', customThemes: [customLight] })
    const s = useThemeStore.getState() as ThemeStore
    expect(selectActiveThemeId(s)).toBe(selectActiveThemeId(s))
    expect(selectActiveThemeId(s)).toBe('nord')
  })

  it('exposes the two builtin theme descriptors', () => {
    expect(BUILTIN_THEMES.map((t) => t.id)).toEqual(['default-dark', 'default-light'])
  })
})
