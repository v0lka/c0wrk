// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// jsdom in this environment does not expose `window.localStorage`, which
// zustand's persist middleware captures at store-creation time. Installed
// before any store module import (same setup as UIScaleSelector.test /
// systemFontStore.test).
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

import { SystemFontToggle } from './SystemFontToggle'
import { useSystemFontStore, SYSTEM_FONT_CSS_VAR } from '@/stores/systemFontStore'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  // Reset the store to defaults between tests: switch off, nothing detected.
  useSystemFontStore.setState({ followSystemFont: false, systemFontFamily: null })
  localStorage.clear()
  document.documentElement.style.removeProperty(SYSTEM_FONT_CSS_VAR)
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

function render(): void {
  act(() => {
    root.render(<SystemFontToggle />)
  })
}

/** The Toggle primitive's hidden checkbox. */
function toggleInput(): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>('input[type="checkbox"]')
  if (!input) throw new Error('toggle input not found')
  return input
}

function familyCaption(): string | null {
  return container.querySelector('[data-testid="system-font-family"]')?.textContent ?? null
}

function notDetectedCaption(): string | null {
  return container.querySelector('[data-testid="system-font-not-detected"]')?.textContent ?? null
}

function cssVar(): string {
  return document.documentElement.style.getPropertyValue(SYSTEM_FONT_CSS_VAR)
}

describe('SystemFontToggle visibility', () => {
  it('renders nothing when no system font is detected and the switch is off', () => {
    // The default state on KDE/Windows/macOS (and before the backend
    // answers): the whole block stays absent, not merely muted.
    render()
    expect(container.textContent).toBe('')
    expect(container.querySelector('input[type="checkbox"]')).toBeNull()
  })

  it('shows the toggle and the detected family name', () => {
    useSystemFontStore.setState({ systemFontFamily: 'Noto Sans' })
    render()
    expect(container.textContent).toContain('System Font')
    expect(toggleInput().checked).toBe(false)
    expect(familyCaption()).toBe('detected: Noto Sans')
    expect(notDetectedCaption()).toBeNull()
  })

  it('reflects the persisted switch state', () => {
    useSystemFontStore.setState({ followSystemFont: true, systemFontFamily: 'Noto Sans' })
    render()
    expect(toggleInput().checked).toBe(true)
  })

  it('shows a muted "not detected" note when the switch is on but no font is available', () => {
    // A flag left on from a session where a font WAS detected must not make
    // the setting silently disappear — the block stays, with the muted note.
    useSystemFontStore.setState({ followSystemFont: true, systemFontFamily: null })
    render()
    expect(toggleInput().checked).toBe(true)
    expect(notDetectedCaption()).toBe('not detected')
    expect(familyCaption()).toBeNull()
  })
})

describe('SystemFontToggle interaction', () => {
  it('flips the store flag and applies the CSS variable on <html> immediately', async () => {
    useSystemFontStore.setState({ systemFontFamily: 'Noto Sans' })
    render()
    // Off by default: nothing applied to the document.
    expect(cssVar()).toBe('')

    await act(async () => {
      toggleInput().click()
    })
    expect(useSystemFontStore.getState().followSystemFont).toBe(true)
    // setFollowSystemFont applies before persisting — no save step, and the
    // family rides along in double quotes (multi-word name stays one token).
    expect(cssVar()).toBe('"Noto Sans"')

    await act(async () => {
      toggleInput().click()
    })
    expect(useSystemFontStore.getState().followSystemFont).toBe(false)
    expect(cssVar()).toBe('')
  })

  it('toggling on without a detected family keeps the document untouched', async () => {
    useSystemFontStore.setState({ followSystemFont: true, systemFontFamily: null })
    render()
    await act(async () => {
      toggleInput().click()
    })
    expect(useSystemFontStore.getState().followSystemFont).toBe(false)
    expect(cssVar()).toBe('')
  })
})
