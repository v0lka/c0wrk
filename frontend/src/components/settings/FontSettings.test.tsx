// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// jsdom here exposes no window.localStorage; zustand's persist middleware in
// fontStore captures it at store-creation time, so polyfill before the store
// module is imported (same setup as uiScaleStore.test.ts / fontStore.test).
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

// --- Mock the backend boundary so tests never touch the Wails runtime ---
const { listFontFamiliesMock } = vi.hoisted(() => ({
  listFontFamiliesMock: vi.fn<(monospace: boolean) => Promise<string[]>>(),
}))
vi.mock('@/api/fonts', () => ({ listFontFamilies: listFontFamiliesMock }))

// Radix popper positioning (autoUpdate) observes the trigger/content with
// ResizeObserver, which jsdom does not provide (same stub as
// StringCombobox.test.tsx).
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

import { FontSettings } from './FontSettings'
import { useFontStore, FONT_SANS_CSS_VAR, FONT_MONO_CSS_VAR } from '@/stores/fontStore'
import { FONT_SANS_STACK, FONT_MONO_STACK } from '@/lib/fonts'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  listFontFamiliesMock.mockReset()
  listFontFamiliesMock.mockResolvedValue([])
  useFontStore.setState({
    uiFontFamily: null,
    monoFontFamily: null,
    detectedUIFamily: null,
    detectedMonoFamily: null,
  })
  document.documentElement.style.removeProperty(FONT_SANS_CSS_VAR)
  document.documentElement.style.removeProperty(FONT_MONO_CSS_VAR)
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

function render(): Promise<void> {
  return act(async () => {
    root.render(<FontSettings />)
    // Settle the fire-and-forget listFontFamilies promise INSIDE act: its
    // setFamilies update must never land after the test body returns.
    await Promise.resolve()
    await Promise.resolve()
  })
}

function uiInput(): HTMLInputElement {
  const el = container.querySelector<HTMLInputElement>('input[aria-label="Interface font"]')
  expect(el).not.toBeNull()
  return el!
}

function monoInput(): HTMLInputElement {
  const el = container.querySelector<HTMLInputElement>('input[aria-label="Monospace font"]')
  expect(el).not.toBeNull()
  return el!
}

/** The single standalone action button ("Use system") — the StringCombobox
 *  chevron triggers carry an "<ariaLabel> options" name and are excluded.
 *  Asserts the exactly-one invariant on every use. */
function actionButton(): HTMLButtonElement {
  const buttons = Array.from(container.querySelectorAll('button')).filter(
    (b) => !b.getAttribute('aria-label')?.endsWith(' options'),
  )
  expect(buttons).toHaveLength(1)
  return buttons[0]!
}

async function openMenu(ariaLabel: string): Promise<void> {
  const btn = container.querySelector<HTMLButtonElement>(`button[aria-label="${ariaLabel} options"]`)
  expect(btn).not.toBeNull()
  // Radix's DropdownMenuTrigger toggles on `pointerdown`, not `click`.
  await act(async () => {
    btn!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
}

function closeMenu(): void {
  act(() => {
    menu().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  })
}

function menu(): HTMLDivElement {
  const el = document.body.querySelector('[role="menu"]')
  expect(el).not.toBeNull()
  return el as HTMLDivElement
}

function menuItems(): string[] {
  return Array.from(menu().querySelectorAll<HTMLElement>('[role="menuitem"]')).map((o) => o.textContent ?? '')
}

function menuItem(label: string): HTMLElement {
  const option = Array.from(menu().querySelectorAll<HTMLElement>('[role="menuitem"]')).find((o) =>
    o.textContent?.includes(label),
  )
  if (!option) throw new Error(`Menu item "${label}" not found`)
  return option
}

function pick(label: string): void {
  act(() => {
    menuItem(label).dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** Set the input value the way a real browser does (React 19 controlled input):
 *  native prototype setter + `input` event, so the synthetic onChange fires. */
function type(ariaLabel: string, text: string): void {
  const el = container.querySelector<HTMLInputElement>(`input[aria-label="${ariaLabel}"]`)!
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
  if (!setter) throw new Error('native input setter not found')
  act(() => {
    setter.call(el, text)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

function press(ariaLabel: string, key: string): void {
  act(() => {
    container
      .querySelector<HTMLInputElement>(`input[aria-label="${ariaLabel}"]`)!
      .dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
  })
}

function sansVar(): string {
  return document.documentElement.style.getPropertyValue(FONT_SANS_CSS_VAR)
}

function monoVar(): string {
  return document.documentElement.style.getPropertyValue(FONT_MONO_CSS_VAR)
}

describe('FontSettings structure', () => {
  it('renders exactly two combobox fields and one action button', async () => {
    await render()
    expect(container.textContent).toContain('Fonts')
    expect(uiInput()).not.toBeNull()
    expect(monoInput()).not.toBeNull()
    // The two chevron triggers are part of the comboboxes; exactly one
    // standalone action button remains (asserted inside actionButton()).
    expect(actionButton().textContent).toBe('Use system')
  })

  it('always renders — no detection and no families leave a working block', async () => {
    // The default state on KDE/Windows/macOS (and before the backend
    // answers): the comboboxes are free-text fields, so the block stays.
    await render()
    expect(uiInput().value).toBe('Default')
    expect(monoInput().value).toBe('Default')
    expect(actionButton().disabled).toBe(true)
    expect(container.textContent).toContain('System fonts not detected on this desktop.')
    expect(container.textContent).toContain('Font changes on the desktop apply after restarting c0wrk.')
  })
})

describe('FontSettings options', () => {
  it('offers Default, the detected family, the current custom value, then families — deduped', async () => {
    listFontFamiliesMock.mockResolvedValue(['Inter', 'JetBrains Mono', 'DejaVu Sans'])
    useFontStore.setState({ detectedUIFamily: 'Inter', uiFontFamily: 'Comic Sans' })
    await render()

    await openMenu('Interface font')
    expect(menuItems()).toEqual(['Default', 'Inter', 'Comic Sans', 'JetBrains Mono', 'DejaVu Sans'])
    // The current custom value is present and carries the selection mark.
    const selected = menu().querySelector<HTMLElement>('[data-selected="true"]')
    expect(selected?.textContent).toBe('Comic Sans')
  })

  it('the monospace menu lists only fontconfig-mono families; the interface menu lists all', async () => {
    // Two independent enumerations: the interface picker fetches every
    // installed family (monospace=false), the monospace picker fetches only
    // the families fontconfig tags as mono (`fc-list :mono`). A shared list
    // here would silently re-offer proportional families in the mono menu.
    listFontFamiliesMock.mockImplementation((monospace: boolean) =>
      Promise.resolve(monospace ? ['JetBrains Mono', 'DejaVu Sans Mono'] : ['Inter', 'Cantarell']),
    )
    useFontStore.setState({ detectedMonoFamily: 'JetBrains Mono' })
    await render()

    await openMenu('Monospace font')
    expect(menuItems()).toEqual(['Default', 'JetBrains Mono', 'DejaVu Sans Mono'])
    closeMenu()
    await openMenu('Interface font')
    expect(menuItems()).toEqual(['Default', 'Inter', 'Cantarell'])
  })
})

describe('FontSettings selection', () => {
  it('picking an installed family applies it to <html> immediately', async () => {
    listFontFamiliesMock.mockResolvedValue(['Inter', 'JetBrains Mono'])
    await render()

    await openMenu('Interface font')
    pick('Inter')
    expect(useFontStore.getState().uiFontFamily).toBe('Inter')
    expect(sansVar()).toBe(`"Inter", ${FONT_SANS_STACK}`)
    expect(uiInput().value).toBe('Inter')
  })

  it('picking Default resets to the default stack (removes the CSS var)', async () => {
    useFontStore.getState().setUIFontFamily('Inter')
    await render()

    await openMenu('Interface font')
    pick('Default')
    expect(useFontStore.getState().uiFontFamily).toBeNull()
    expect(sansVar()).toBe('')
    expect(uiInput().value).toBe('Default')
  })

  it('the monospace combobox applies the mono var without touching the UI family', async () => {
    listFontFamiliesMock.mockResolvedValue(['JetBrains Mono'])
    await render()

    await openMenu('Monospace font')
    pick('JetBrains Mono')
    expect(useFontStore.getState().monoFontFamily).toBe('JetBrains Mono')
    expect(monoVar()).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
    expect(useFontStore.getState().uiFontFamily).toBeNull()
    expect(sansVar()).toBe('')
  })

  it('commits a typed custom value with normalization, applying it immediately', async () => {
    await render()

    type('Interface font', '"My Font"')
    press('Interface font', 'Enter')
    expect(useFontStore.getState().uiFontFamily).toBe('My Font')
    expect(sansVar()).toBe(`"My Font", ${FONT_SANS_STACK}`)
  })

  it('reverts a typed value that normalizes to nothing (store untouched)', async () => {
    useFontStore.getState().setUIFontFamily('Inter')
    await render()

    type('Interface font', '   ')
    press('Interface font', 'Enter')
    expect(useFontStore.getState().uiFontFamily).toBe('Inter')
    expect(uiInput().value).toBe('Inter')
  })
})

describe('FontSettings Use system', () => {
  it('stays disabled without a detection even when families are listed', async () => {
    listFontFamiliesMock.mockResolvedValue(['Inter'])
    await render()

    expect(actionButton().disabled).toBe(true)
  })

  it('applies both detected families and names them in the caption', async () => {
    useFontStore.setState({ detectedUIFamily: 'Inter', detectedMonoFamily: 'JetBrains Mono' })
    await render()

    expect(container.textContent).toContain(
      'Detected system fonts — interface: Inter, monospace: JetBrains Mono.',
    )
    act(() => {
      actionButton().click()
    })
    expect(useFontStore.getState().uiFontFamily).toBe('Inter')
    expect(useFontStore.getState().monoFontFamily).toBe('JetBrains Mono')
    expect(sansVar()).toBe(`"Inter", ${FONT_SANS_STACK}`)
    expect(monoVar()).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
    expect(uiInput().value).toBe('Inter')
    expect(monoInput().value).toBe('JetBrains Mono')
  })

  it('applies only the family that was found (mono not detected)', async () => {
    useFontStore.setState({ detectedUIFamily: 'Inter', monoFontFamily: 'Comic Sans' })
    await render()

    act(() => {
      actionButton().click()
    })
    expect(useFontStore.getState().uiFontFamily).toBe('Inter')
    // «Что смогли, то и нашли»: the undetected mono choice stays as-is.
    expect(useFontStore.getState().monoFontFamily).toBe('Comic Sans')
    expect(monoVar()).toBe(`"Comic Sans", ${FONT_MONO_STACK}`)
  })
})

describe('FontSettings family enumeration failure', () => {
  it('degrades fail-soft: the block keeps working with the reduced option set', async () => {
    listFontFamiliesMock.mockRejectedValue(new Error('fontconfig unavailable'))
    useFontStore.setState({ detectedUIFamily: 'Inter' })
    await render()

    // The block renders and the menu offers Default + the detection (the
    // enumeration failure is already logged at the api/fonts boundary).
    await openMenu('Interface font')
    expect(menuItems()).toEqual(['Default', 'Inter'])
    expect(actionButton().disabled).toBe(false)
  })
})

describe('FontSettings font preview', () => {
  it('renders each option label in its own typeface (Default in the stock stack)', async () => {
    listFontFamiliesMock.mockResolvedValue(['Inter', 'JetBrains Mono'])
    await render()

    const labelStyle = (label: string): string => {
      const span = menuItem(label).querySelector<HTMLElement>('span')
      expect(span).not.toBeNull()
      return span!.style.fontFamily
    }
    await openMenu('Interface font')
    expect(labelStyle('Default')).toBe(FONT_SANS_STACK)
    expect(labelStyle('Inter')).toBe(`"Inter", ${FONT_SANS_STACK}`)
    closeMenu()

    await openMenu('Monospace font')
    expect(labelStyle('Default')).toBe(FONT_MONO_STACK)
    expect(labelStyle('JetBrains Mono')).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
  })

  it('renders the current value in the closed field in its own typeface', async () => {
    useFontStore.setState({ monoFontFamily: 'JetBrains Mono' })
    await render()

    expect(monoInput().style.fontFamily).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
    // null family → the stock sans stack, not an empty/serif fallback.
    expect(uiInput().style.fontFamily).toBe(FONT_SANS_STACK)
  })
})
