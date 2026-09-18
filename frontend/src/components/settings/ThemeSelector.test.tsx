// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ThemeSelector } from './ThemeSelector'
import { useThemeStore } from '@/stores/themeStore'
import type { ThemeImportOutcome, ThemeInfo } from '@/api/themes'

// The failure-path tests below make theme import/delete reject on purpose;
// the real logger would print "Failed to import theme: …" /
// "Failed to delete theme: …" for each. That output is the expected behavior
// under test, not a regression — mock the logger to keep the output clean.
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

// Radix popper positioning (autoUpdate) observes the trigger/content with
// ResizeObserver, which jsdom does not provide.
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

// --- Mocks -------------------------------------------------------------

const themeApi = vi.hoisted(() => ({
  list: [] as ThemeInfo[],
  outcomes: null as ThemeImportOutcome[] | null,
  deleted: [] as string[],
}))

vi.mock('@/api/themes', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/themes')>()
  return {
    ...actual,
    listThemes: vi.fn(async () => themeApi.list),
    pickAndImportThemes: vi.fn(async () => themeApi.outcomes),
    deleteTheme: vi.fn(async (id: string) => {
      themeApi.deleted.push(id)
      themeApi.list = themeApi.list.filter((t) => t.id !== id)
    }),
  }
})

// ItemAction renders Radix Tooltips, which require the app-root
// TooltipProvider; render plain buttons instead (the same stopPropagation +
// onClick contract the real overlay provides inside a menu row).
vi.mock('@/components/layout/ItemAction', () => ({
  ItemAction: ({
    label,
    onClick,
    children,
  }: {
    label: string
    onClick: () => void
    children: React.ReactNode
  }) => (
    <button
      type="button"
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation()
        onClick()
      }}
    >
      {children}
    </button>
  ),
  ItemActions: ({ children }: { children: React.ReactNode }) => <span>{children}</span>,
}))

const emitSpy = vi.hoisted(() => vi.fn())
vi.mock('@/api/runtime', () => ({
  emit: emitSpy,
}))

import { pickAndImportThemes, deleteTheme } from '@/api/themes'

// --- Harness -----------------------------------------------------------

let container: HTMLDivElement
let root: Root

const NORD_CSS = ':root{--color-background:#2e3440;--color-foreground:#d8dee9}'
const NORD: ThemeInfo = { id: 'nord', name: 'Nord', type: 'dark', css: NORD_CSS }
const PAPER: ThemeInfo = { id: 'paper', name: 'Paper', type: 'light', css: ':root{--color-background:#faf8f2;--color-foreground:#3a3a38}' }

beforeEach(() => {
  themeApi.list = []
  themeApi.outcomes = null
  themeApi.deleted = []
  emitSpy.mockClear()
  vi.mocked(pickAndImportThemes).mockClear()
  vi.mocked(deleteTheme).mockClear()
  act(() => {
    useThemeStore.setState({ themeId: 'default-dark', themeCss: '', customThemes: [] })
  })
  document.documentElement.removeAttribute('data-theme')
    document.documentElement.removeAttribute('data-custom-theme')
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

function renderSelector() {
  act(() => {
    root.render(<ThemeSelector />)
  })
}

function trigger(): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>('button[aria-label="Theme"]')
  expect(el).not.toBeNull()
  return el!
}

/**
 * Open the dropdown. Radix's DropdownMenuTrigger toggles on `pointerdown`
 * (left button, no ctrl), not on `click` — mirroring native menu behavior.
 */
async function openDropdown(): Promise<HTMLButtonElement> {
  const btn = trigger()
  await act(async () => {
    btn.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
  return btn
}

function menu(): HTMLDivElement {
  const el = document.body.querySelector('[role="menu"]')
  expect(el).not.toBeNull()
  return el as HTMLDivElement
}

function menuItem(label: string): HTMLElement {
  const option = Array.from(menu().querySelectorAll<HTMLElement>('[role="menuitem"]')).find((o) =>
    o.textContent?.includes(label),
  )
  if (!option) throw new Error(`Menu item "${label}" not found`)
  return option
}

function importButton(): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>('button[aria-label="Import theme"]')
  expect(el).not.toBeNull()
  return el!
}

async function openWithCustoms(themes: ThemeInfo[], activeId = 'default-dark') {
  themeApi.list = themes
  act(() => {
    useThemeStore.setState({ customThemes: themes, themeId: activeId })
  })
  renderSelector()
  await openDropdown()
}

// --- Tests -------------------------------------------------------------

describe('ThemeSelector combobox', () => {
  it('shows exactly the two builtin items with type icons', async () => {
    renderSelector()
    await openDropdown()
    const items = Array.from(menu().querySelectorAll('[role="menuitem"]'))
    expect(items).toHaveLength(2)
    expect(menuItem('Default Dark')).toBeTruthy()
    expect(menuItem('Default Light')).toBeTruthy()
    // Type icons: Moon on Dark, Sun on Light (svg with class lucide-moon/sun).
    expect(menuItem('Default Dark').querySelector('.lucide-moon')).not.toBeNull()
    expect(menuItem('Default Light').querySelector('.lucide-sun')).not.toBeNull()
    // Builtins expose no delete action.
    expect(menuItem('Default Dark').querySelector('button[aria-label="Delete theme"]')).toBeNull()
    expect(menuItem('Default Light').querySelector('button[aria-label="Delete theme"]')).toBeNull()
  })

  it('trigger shows the active theme type icon and name', () => {
    renderSelector()
    expect(trigger().textContent).toContain('Default Dark')
    expect(trigger().querySelector('.lucide-moon')).not.toBeNull()
    act(() => {
      useThemeStore.setState({ themeId: 'default-light' })
    })
    expect(trigger().textContent).toContain('Default Light')
    expect(trigger().querySelector('.lucide-sun')).not.toBeNull()
  })

  it('selecting Default Light switches the store and applies data-theme', async () => {
    renderSelector()
    await openDropdown()
    act(() => {
      menuItem('Default Light').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useThemeStore.getState().themeId).toBe('default-light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('selecting a custom theme activates it with its CSS', async () => {
    await openWithCustoms([NORD])
    act(() => {
      menuItem('Nord').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    const state = useThemeStore.getState()
    expect(state.themeId).toBe('nord')
    expect(state.themeCss).toBe(NORD_CSS)
    // v3: the kind attribute carries the TYPE, the slug lives on
    // data-custom-theme, and the injected CSS is scoped to that attribute.
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(document.documentElement.getAttribute('data-custom-theme')).toBe('nord')
    expect(document.getElementById('c0wrk-custom-theme')?.textContent)
      .toContain(':root:root[data-custom-theme="nord"]{--color-background:#2e3440')
  })
})

describe('ThemeSelector delete', () => {
  it('one click on the hover trash deletes a custom theme and refreshes the list', async () => {
    await openWithCustoms([NORD, PAPER])
    await act(async () => {
      const trash = menuItem('Nord').querySelector<HTMLButtonElement>(
        'button[aria-label="Delete theme"]',
      )
      expect(trash).not.toBeNull()
      trash!.click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(deleteTheme).toHaveBeenCalledWith('nord')
    // The catalog is refetched and the deleted theme is gone from the store.
    expect(useThemeStore.getState().customThemes.map((t) => t.id)).toEqual(['paper'])
  })

  it('deleting the active theme falls back to Default Dark', async () => {
    await openWithCustoms([NORD], 'nord')
    expect(useThemeStore.getState().themeId).toBe('nord')
    await act(async () => {
      menuItem('Nord').querySelector<HTMLButtonElement>('button[aria-label="Delete theme"]')!.click()
      await new Promise((r) => setTimeout(r, 0))
    })
    const state = useThemeStore.getState()
    expect(state.themeId).toBe('default-dark')
    expect(state.themeCss).toBe('')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('surfaces a runtime_error toast when deletion fails', async () => {
    vi.mocked(deleteTheme).mockRejectedValueOnce(new Error('boom'))
    await openWithCustoms([NORD])
    await act(async () => {
      menuItem('Nord').querySelector<HTMLButtonElement>('button[aria-label="Delete theme"]')!.click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(emitSpy).toHaveBeenCalledWith(
      'runtime_error',
      expect.objectContaining({ message: 'Failed to delete theme' }),
    )
  })
})

describe('ThemeSelector import', () => {
  it('a successful single import activates the imported theme and refreshes the list', async () => {
    themeApi.list = [NORD]
    themeApi.outcomes = [{ file: '/tmp/nord.css', theme: NORD }]
    renderSelector()
    await act(async () => {
      importButton().click()
      await new Promise((r) => setTimeout(r, 0))
    })
    const state = useThemeStore.getState()
    expect(state.themeId).toBe('nord')
    expect(state.themeCss).toBe(NORD_CSS)
    expect(state.customThemes.map((t) => t.id)).toEqual(['nord'])
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(document.documentElement.getAttribute('data-custom-theme')).toBe('nord')
    // No failure toast — every file in the batch installed.
    expect(emitSpy).not.toHaveBeenCalledWith('runtime_error', expect.anything())
  })

  it('a multi import activates the last successful theme', async () => {
    themeApi.list = [NORD, PAPER]
    themeApi.outcomes = [
      { file: '/tmp/nord.css', theme: NORD },
      { file: '/tmp/paper.css', theme: PAPER },
    ]
    renderSelector()
    await act(async () => {
      importButton().click()
      await new Promise((r) => setTimeout(r, 0))
    })
    const state = useThemeStore.getState()
    expect(state.themeId).toBe('paper') // last successful pick wins
    expect(state.themeCss).toBe(PAPER.css)
    expect(state.customThemes.map((t) => t.id)).toEqual(['nord', 'paper'])
  })

  it('a partially failed batch still installs the valid files and toasts the skipped ones', async () => {
    themeApi.list = [NORD]
    themeApi.outcomes = [
      { file: '/tmp/nord.css', theme: NORD },
      { file: '/tmp/broken.css', error: 'invalid theme CSS: @import is not allowed' },
    ]
    renderSelector()
    await act(async () => {
      importButton().click()
      await new Promise((r) => setTimeout(r, 0))
    })
    const state = useThemeStore.getState()
    // The valid file installed and activated.
    expect(state.themeId).toBe('nord')
    expect(state.customThemes.map((t) => t.id)).toEqual(['nord'])
    // The skipped file is reported via the runtime_error toast.
    expect(emitSpy).toHaveBeenCalledWith(
      'runtime_error',
      expect.objectContaining({ message: expect.stringContaining('1 file was skipped') }),
    )
  })

  it('a fully failed batch installs nothing and toasts the skipped files', async () => {
    themeApi.outcomes = [
      { file: '/tmp/a.css', error: 'invalid theme CSS' },
      { file: '/tmp/b.css', error: 'invalid theme CSS' },
    ]
    renderSelector()
    await act(async () => {
      importButton().click()
      await new Promise((r) => setTimeout(r, 0))
    })
    const state = useThemeStore.getState()
    expect(state.themeId).toBe('default-dark') // nothing activated
    expect(state.customThemes).toEqual([])
    expect(emitSpy).toHaveBeenCalledWith(
      'runtime_error',
      expect.objectContaining({ message: expect.stringContaining('2 files were skipped') }),
    )
  })

  it('picker cancel changes nothing', async () => {
    renderSelector()
    await act(async () => {
      importButton().click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(useThemeStore.getState().themeId).toBe('default-dark')
    expect(useThemeStore.getState().customThemes).toEqual([])
  })

  it('surfaces a runtime_error toast when the import fails', async () => {
    vi.mocked(pickAndImportThemes).mockRejectedValueOnce(new Error('boom'))
    renderSelector()
    await act(async () => {
      importButton().click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(emitSpy).toHaveBeenCalledWith(
      'runtime_error',
      expect.objectContaining({ message: 'Failed to import theme' }),
    )
  })
})
