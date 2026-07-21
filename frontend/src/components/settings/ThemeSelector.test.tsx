// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// jsdom in this environment does not expose `window.localStorage`, which
// zustand's `persist` middleware captures at store-creation time (via
// createJSONStorage(() => window.localStorage)). Install an in-memory
// polyfill before any store module is imported so themeStore works.
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  win.IS_REACT_ACT_ENVIRONMENT = true
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

import { ThemeSelector } from './ThemeSelector'
import { useThemeStore } from '@/stores/themeStore'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  useThemeStore.setState({ theme: 'dark' })
  document.documentElement.removeAttribute('data-theme')
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

function allButtons(): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll('button'))
}

function findButtonByLabel(label: string): HTMLButtonElement {
  const btn = allButtons().find((b) => b.textContent?.includes(label))
  if (!btn) throw new Error(`Button "${label}" not found`)
  return btn
}

describe('ThemeSelector', () => {
  it('renders Dark and Light options', () => {
    act(() => {
      root.render(<ThemeSelector />)
    })
    const labels = allButtons().map((b) => b.textContent)
    expect(labels).toContain('Dark')
    expect(labels).toContain('Light')
  })

  it('clicking Light switches the store and applies data-theme', () => {
    act(() => {
      root.render(<ThemeSelector />)
    })
    act(() => {
      findButtonByLabel('Light').click()
    })
    expect(useThemeStore.getState().theme).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('clicking Dark switches back to dark', () => {
    useThemeStore.setState({ theme: 'light' })
    act(() => {
      root.render(<ThemeSelector />)
    })
    act(() => {
      findButtonByLabel('Dark').click()
    })
    expect(useThemeStore.getState().theme).toBe('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })
})
