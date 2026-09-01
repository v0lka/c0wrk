// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Module mocks ------------------------------------------------------------
// The terminal API is mocked so tests assert the start/stop call sequence —
// the lifecycle contract that keeps per-session terminals alive.

const api = vi.hoisted(() => ({
  startTerminal: vi.fn(async () => {}),
  startTerminalInDir: vi.fn(async () => {}),
  terminalInput: vi.fn(async () => {}),
  terminalResize: vi.fn(async () => {}),
  stopTerminal: vi.fn(async () => {}),
  getTerminalHistory: vi.fn(async () => [] as string[]),
}))
vi.mock('@/api/terminal', () => ({
  startTerminal: api.startTerminal,
  startTerminalInDir: api.startTerminalInDir,
  terminalInput: api.terminalInput,
  terminalResize: api.terminalResize,
  stopTerminal: api.stopTerminal,
  getTerminalHistory: api.getTerminalHistory,
}))

const runtime = vi.hoisted(() => ({
  onSessionEvent: vi.fn((_sessionId: string, _event: string, _callback: () => void) => () => {}),
  reportDroppedEvent: vi.fn(),
}))
vi.mock('@/api/runtime', () => runtime)

// xterm.js needs real DOM measurements; a stub keeps the tests focused on
// lifecycle logic instead of rendering internals.
vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    options: Record<string, unknown> = {}
    onData = vi.fn()
    open = vi.fn()
    loadAddon = vi.fn()
    write = vi.fn()
    writeln = vi.fn()
    focus = vi.fn()
    blur = vi.fn()
    dispose = vi.fn()
  },
}))
vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit = vi.fn()
  },
}))

import { TerminalPanel } from './TerminalPanel'
import { useTerminalRegistryStore } from '@/stores/terminalRegistryStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { SessionInfo } from '@/types/models'
import type { ReactElement } from 'react'

// --- Harness -----------------------------------------------------------------

let container: HTMLDivElement
let root: Root

function session(id: string): SessionInfo {
  return {
    id,
    project_id: 'p1',
    name: id,
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: 'm',
    family: 'f',
    has_unfinished_task: false,
    unfinished_task_status: '',
  }
}

/** Render (or re-render) an element and flush pending microtasks. */
async function render(el: ReactElement): Promise<void> {
  await act(async () => {
    root.render(el)
    await Promise.resolve()
  })
}

/** Instance wrappers: the absolute-positioned divs that hold one Terminal
 *  each (the loading overlay also uses `absolute inset-0` but carries z-10). */
function instanceWrappers(): HTMLElement[] {
  return Array.from(
    container.querySelectorAll<HTMLElement>('div.absolute.inset-0:not(.z-10)'),
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  // jsdom does not provide ResizeObserver; Terminal observes its container.
  const g = globalThis as Record<string, unknown>
  g.ResizeObserver = class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  useTerminalRegistryStore.setState({ instances: [], readySessions: new Set<string>() })
  useSessionStore.setState({ sessions: null, activeSessionId: null })
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

// --- Tests -------------------------------------------------------------------

describe('TerminalPanel per-session terminal lifetime', () => {
  it('starts a terminal for the active session when first shown', async () => {
    await render(<TerminalPanel sessionId="s1" visible />)

    expect(api.startTerminal).toHaveBeenCalledTimes(1)
    expect(api.startTerminal).toHaveBeenCalledWith('s1')
    // Start resolved → onReady fired → loading overlay gone.
    expect(container.querySelector('.z-10')).toBeNull()
  })

  it('keeps the previous terminal mounted (hidden) and starts the new one on switch', async () => {
    await render(<TerminalPanel sessionId="s1" visible />)
    await render(<TerminalPanel sessionId="s2" visible />)

    // Both instances stay mounted; the inactive one is hidden via CSS.
    const wrappers = instanceWrappers()
    expect(wrappers).toHaveLength(2)
    expect(wrappers[0]!.classList.contains('hidden')).toBe(true)
    expect(wrappers[1]!.classList.contains('hidden')).toBe(false)

    // Each session got exactly one start; nothing was stopped.
    expect(api.startTerminal).toHaveBeenCalledWith('s1')
    expect(api.startTerminal).toHaveBeenCalledWith('s2')
    expect(api.startTerminal).toHaveBeenCalledTimes(2)
    expect(api.stopTerminal).not.toHaveBeenCalled()
  })

  it('does not restart a terminal when switching back to its session', async () => {
    await render(<TerminalPanel sessionId="s1" visible />)
    await render(<TerminalPanel sessionId="s2" visible />)
    await render(<TerminalPanel sessionId="s1" visible />)

    expect(api.startTerminal).toHaveBeenCalledTimes(2)
    expect(api.stopTerminal).not.toHaveBeenCalled()
    const wrappers = instanceWrappers()
    expect(wrappers[0]!.classList.contains('hidden')).toBe(false)
    expect(wrappers[1]!.classList.contains('hidden')).toBe(true)
  })

  it('keeps instances when the session list no longer contains them (project switch reload)', async () => {
    // Regression: the sessionStore list is scoped to the ACTIVE project.
    // Switching projects reloads it with only the new project's sessions —
    // that must NOT unmount other projects' live terminals.
    useSessionStore.setState({ sessions: [session('s1'), session('s2')] })
    await render(<TerminalPanel sessionId="s1" visible />)
    await render(<TerminalPanel sessionId="s2" visible />)
    expect(instanceWrappers()).toHaveLength(2)

    // List reload scoped to another project: s1 is gone from the list.
    await act(async () => {
      useSessionStore.getState().setSessions([session('s2')])
      await Promise.resolve()
    })

    expect(instanceWrappers()).toHaveLength(2)
  })

  it('prunes an instance only via the explicit deletion path', async () => {
    await render(<TerminalPanel sessionId="s1" visible />)
    await render(<TerminalPanel sessionId="s2" visible />)
    expect(instanceWrappers()).toHaveLength(2)

    // Session s1 is deleted — useSessionActions.handleDelete drops the
    // instance after the backend already stopped its PTY.
    await act(async () => {
      useTerminalRegistryStore.getState().removeInstances(['s1'])
      await Promise.resolve()
    })

    const wrappers = instanceWrappers()
    expect(wrappers).toHaveLength(1)
    expect(wrappers[0]!.classList.contains('hidden')).toBe(false)
  })

  it('resurrects a dead shell (terminal_exited) when its session is reactivated', async () => {
    await render(<TerminalPanel sessionId="s1" visible />)
    await render(<TerminalPanel sessionId="s2" visible />)
    expect(api.startTerminal).toHaveBeenCalledTimes(2)

    // The s1 shell exits on its own → backend emits terminal_exited.
    const exitCallbacks = runtime.onSessionEvent.mock.calls
      .filter(([sid, event]) => sid === 's1' && event === 'terminal_exited')
      .map(([, , callback]) => callback)
    expect(exitCallbacks).toHaveLength(1)
    act(() => {
      exitCallbacks[0]!()
    })

    // Switching back to s1 makes the instance visible → lazy restart.
    await render(<TerminalPanel sessionId="s1" visible />)

    expect(api.startTerminal).toHaveBeenCalledTimes(3)
    expect(api.startTerminal).toHaveBeenLastCalledWith('s1')
    expect(api.stopTerminal).not.toHaveBeenCalled()
  })
})
