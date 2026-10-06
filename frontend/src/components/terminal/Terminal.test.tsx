// @vitest-environment jsdom
//
// Regression guard for the terminal theme-update effect.
//
// xterm 6 exposes `terminal.options` as an object that ALSO carries the
// constructor-only `cols`/`rows`, with per-property setters that throw
// `Option "cols" can only be set in the constructor` when those are assigned.
// The old palette effect did `term.options = { ...term.options, theme }`,
// which re-assigned cols/rows, threw from the mount/palette effect, and —
// because there was no boundary around the terminal pane — blanked the WHOLE
// chat input with "Input error" on every terminal open. The fake below
// reproduces xterm's options semantics so any regression to the spread form
// fails loudly here.
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest'
import { createElement } from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { FakeTerminal, FitAddonMock, terminalInstances } = vi.hoisted(() => {
  const instances: FakeTerminalLike[] = []
  interface FakeTerminalLike {
    _options: Record<string, unknown>
    options: Record<string, unknown>
    loadAddon: () => void
    open: () => void
    focus: () => void
    blur: () => void
    dispose: () => void
    writeln: () => void
    write: () => void
    onData: () => { dispose: () => void }
    rows: number
    cols: number
  }
  class FakeTerminal implements FakeTerminalLike {
    _options: Record<string, unknown>
    rows = 24
    cols = 80
    constructor(opts: Record<string, unknown> = {}) {
      // xterm's public options include the constructor-only cols/rows.
      this._options = { ...opts, cols: 80, rows: 24 }
      instances.push(this)
    }
    get options(): Record<string, unknown> {
      return this._options
    }
    set options(value: Record<string, unknown>) {
      for (const key of Object.keys(value)) {
        if (key === 'cols' || key === 'rows') {
          throw new Error(`Option "${key}" can only be set in the constructor`)
        }
        this._options[key] = value[key]
      }
    }
    loadAddon(): void {}
    open(): void {}
    focus(): void {}
    blur(): void {}
    dispose(): void {}
    writeln(): void {}
    write(): void {}
    onData(): { dispose: () => void } {
      return { dispose: () => {} }
    }
  }
  class FitAddonMock {
    activate(): void {}
    dispose(): void {}
    fit(): void {}
    proposeDimensions(): { cols: number; rows: number } {
      return { cols: 80, rows: 24 }
    }
  }
  return { FakeTerminal, FitAddonMock, terminalInstances: instances }
})

vi.mock('@xterm/xterm', () => ({ Terminal: FakeTerminal }))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: FitAddonMock }))
vi.mock('@/api/terminal', () => ({
  startTerminal: vi.fn().mockResolvedValue(undefined),
  startTerminalInDir: vi.fn().mockResolvedValue(undefined),
  terminalInput: vi.fn().mockResolvedValue(undefined),
  terminalResize: vi.fn().mockResolvedValue(undefined),
  stopTerminal: vi.fn().mockResolvedValue(undefined),
  getTerminalHistory: vi.fn().mockResolvedValue([]),
}))
vi.mock('@/api/runtime', () => ({
  onSessionEvent: vi.fn(() => () => {}),
  reportDroppedEvent: vi.fn(),
  subscribe: vi.fn(() => () => {}),
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { Terminal } from '@/components/terminal/Terminal'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { useThemeStore } from '@/stores/themeStore'
import { useUiScaleStore } from '@/stores/uiScaleStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useFontStore } from '@/stores/fontStore'
import { FONT_MONO_STACK } from '@/lib/fonts'

beforeAll(() => {
  class RO {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  globalThis.ResizeObserver = RO as unknown as typeof ResizeObserver
})

let container: HTMLDivElement
let root: Root
let boundaryTripped = false

beforeEach(() => {
  terminalInstances.length = 0
  boundaryTripped = false
  useThemeStore.setState({ themeId: 'default-dark', themeCss: '', themeType: 'dark', customThemes: [] })
  useUiScaleStore.setState({ scale: 100 })
  useInputModeStore.setState({ mode: 'terminal', pendingTerminalDir: null })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
})

function renderTerminal() {
  act(() => {
    root.render(
      createElement(ErrorBoundary, {
        fallback: () => {
          boundaryTripped = true
          return createElement('div', null, 'BOUNDARY')
        },
        children: createElement(Terminal, { sessionId: 'sess-a', visible: true, isActive: true }),
      }),
    )
  })
}

describe('Terminal theme effect', () => {
  it('assigns only options.theme and never re-sets constructor-only cols/rows', () => {
    renderTerminal()
    expect(boundaryTripped).toBe(false)
    expect(terminalInstances).toHaveLength(1)
    const term = terminalInstances[0]!
    // The palette effect ran on mount and set the theme.
    expect(term._options.theme).toBeDefined()

    // A theme change must update the live terminal without throwing.
    act(() => {
      useThemeStore.setState({ themeId: 'default-light', themeCss: '', themeType: 'light', customThemes: [] })
    })
    expect(boundaryTripped).toBe(false)
    expect(term._options.theme).toBeDefined()
  })
})

describe('Terminal mono-font wiring (fontStore)', () => {
  // Isolate from persisted font choices of other tests (the store rehydrates
  // `c0wrk-fonts` from localStorage at module init).
  beforeEach(() => {
    useFontStore.setState({ uiFontFamily: null, monoFontFamily: null })
  })

  it('renders the default mono stack when no family is chosen', () => {
    renderTerminal()
    expect(boundaryTripped).toBe(false)
    // monoFontFamily null → the bare stock stack, exactly as before the
    // font-store wiring (the xterm constructor is px/px by API and needs a
    // literal stack, not the CSS var).
    expect(terminalInstances[0]!._options.fontFamily).toBe(FONT_MONO_STACK)
  })

  it('renders a chosen mono family composed with the default stack', () => {
    act(() => {
      useFontStore.getState().setMonoFontFamily('JetBrains Mono')
    })
    renderTerminal()
    expect(boundaryTripped).toBe(false)
    // The chosen family as ONE double-quoted token (multi-word names must
    // not split), prepended to the stock stack so its fallbacks survive.
    expect(terminalInstances[0]!._options.fontFamily).toBe(`"JetBrains Mono", ${FONT_MONO_STACK}`)
  })

  it('applies a Settings font change to the LIVE terminal without remounting', () => {
    renderTerminal()
    expect(terminalInstances).toHaveLength(1)
    expect(terminalInstances[0]!._options.fontFamily).toBe(FONT_MONO_STACK)

    // Session terminals stay mounted for the app lifetime, so a font picked
    // in Settings MUST reach the already-mounted instance through the
    // live-apply effect — deferring it to the next mount would leave every
    // open terminal on the old font (this asserts exactly that contract).
    act(() => {
      useFontStore.getState().setMonoFontFamily('Fira Code')
    })
    expect(boundaryTripped).toBe(false)
    expect(terminalInstances).toHaveLength(1)
    expect(terminalInstances[0]!._options.fontFamily).toBe(`"Fira Code", ${FONT_MONO_STACK}`)

    // Switching back to the default (null) restores the stock stack on the
    // same instance.
    act(() => {
      useFontStore.getState().setMonoFontFamily(null)
    })
    expect(boundaryTripped).toBe(false)
    expect(terminalInstances).toHaveLength(1)
    expect(terminalInstances[0]!._options.fontFamily).toBe(FONT_MONO_STACK)
  })
})
