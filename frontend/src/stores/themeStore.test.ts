// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'

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

import { useThemeStore, applyThemeToDocument } from '@/stores/themeStore'

describe('themeStore', () => {
  beforeEach(() => {
    // Reset to default and clear any persisted state between tests.
    useThemeStore.setState({ theme: 'dark' })
    localStorage.clear()
    document.documentElement.removeAttribute('data-theme')
  })

  it('defaults to dark', () => {
    expect(useThemeStore.getState().theme).toBe('dark')
  })

  it('setTheme updates the store state', () => {
    const { setTheme } = useThemeStore.getState()
    setTheme('light')
    expect(useThemeStore.getState().theme).toBe('light')
    setTheme('dark')
    expect(useThemeStore.getState().theme).toBe('dark')
  })

  it('setTheme applies the data-theme attribute to <html>', () => {
    const { setTheme } = useThemeStore.getState()
    setTheme('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    setTheme('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('applyThemeToDocument writes the attribute directly', () => {
    applyThemeToDocument('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    applyThemeToDocument('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })
})
