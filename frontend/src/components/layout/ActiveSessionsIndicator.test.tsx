// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// activeSessionsStore imports listAllSessions at module scope; keep the RPC
// layer inert here (same mock as stores/activeSessionsStore.test.ts). The
// debounced refresh scheduled on mount never fires inside these tests;
// opening the dropdown calls refreshNow() — beforeEach pins that RPC to a
// never-settling promise so it cannot clobber the seeded store snapshot.
vi.mock('@/api/sessions', () => ({ listAllSessions: vi.fn() }))

// The dropdown's pending sweep must not touch the Wails backend; the
// project-switch hook is mocked so navigation tests can assert (and control)
// the switch instead of running the real queue.
vi.mock('@/api/chat', () => ({ getPendingActions: vi.fn(async () => null) }))
const { switchProjectWithStateMock } = vi.hoisted(() => ({ switchProjectWithStateMock: vi.fn() }))
vi.mock('@/hooks/useProjectSwitchState', () => ({ useProjectSwitchState: () => switchProjectWithStateMock }))

import { ActiveSessionsIndicator } from './ActiveSessionsIndicator'
import { ActiveSessionsBadge } from './ActiveSessionsBadge'
import { SidebarHeader } from './SidebarHeader'
import { NO_BADGE_FLAGS, type BadgeFlags } from '@/lib/activeSessions'
import { cancelPendingRefresh, useActiveSessionsStore } from '@/stores/activeSessionsStore'
import { useChatStore } from '@/stores/chatStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { listAllSessions } from '@/api/sessions'
import { getPendingActions, type PendingActionsResponse } from '@/api/chat'
import { TooltipProvider, TOOLTIP_DELAY_MS } from '@/components/ui/tooltip'
import type { ProjectInfo, SessionInfo } from '@/types/models'

// --- Fixtures ---------------------------------------------------------------

function makeSession(overrides: Partial<SessionInfo> = {}): SessionInfo {
  const ts = new Date(0).toISOString()
  return {
    id: 's1',
    project_id: 'p1',
    name: 'Session',
    created_at: ts,
    last_active_at: ts,
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: '',
    family: '',
    has_unfinished_task: false,
    unfinished_task_status: '',
    ...overrides,
  }
}

function makeProjects(): ProjectInfo[] {
  const ts = new Date(0).toISOString()
  return [
    { id: 'no-proj', name: 'No Project', workspace_path: '', is_external: false, is_no_project: true, research_root: '', is_research: false, created_at: ts, last_active_at: ts },
    { id: 'p1', name: 'Real', workspace_path: '/tmp/r', is_external: false, is_no_project: false, research_root: '', is_research: false, created_at: ts, last_active_at: ts },
  ]
}

function makeFlags(overrides: Partial<BadgeFlags> = {}): BadgeFlags {
  return { error: false, attention: false, active: false, paused: false, anyLive: false, ...overrides }
}

// --- Render helpers ----------------------------------------------------------

const roots: Root[] = []

/** selectSession is overridden in the store for every test so selection never
 *  reaches the real persistence RPC (saveProjectActiveSession). */
const selectSessionMock = vi.fn()

function render(ui: React.ReactNode): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  roots.push(root)
  act(() => {
    root.render(ui)
  })
  return container
}

function radarButton(container: HTMLElement): HTMLButtonElement {
  return container.querySelector('button[aria-label="Active sessions"]') as HTMLButtonElement
}

/** The dot spans are the children of the badge's aria-hidden cluster span. */
function dots(within: HTMLElement): HTMLElement[] {
  return [...within.querySelectorAll<HTMLElement>('span[aria-hidden="true"] > span')]
}

beforeEach(() => {
  useActiveSessionsStore.setState({ sessions: null, pendingOverride: {}, refreshing: false })
  useChatStore.setState({ taskActive: {}, paused: {}, messages: {}, messageOrder: {} })
  useProjectStore.setState({ projects: null, activeProjectId: null, lastRealProjectId: null })
  useSessionStore.setState({ sessions: null, activeSessionId: null, selectSession: selectSessionMock })
  // refreshNow() fires the moment the dropdown opens — pin that RPC to a
  // never-settling promise unless a test explicitly opts in, so the seeded
  // snapshot survives the open.
  vi.mocked(listAllSessions).mockImplementation(() => new Promise<SessionInfo[]>(() => {}))
  vi.mocked(getPendingActions).mockReset()
  vi.mocked(getPendingActions).mockResolvedValue(null)
  switchProjectWithStateMock.mockReset()
  switchProjectWithStateMock.mockResolvedValue(undefined)
  selectSessionMock.mockClear()
  // jsdom lacks ResizeObserver (EllipsisHint measures overflow with it) and
  // cancelAnimationFrame (floating-ui's cleanup path) — stub both for every
  // test; rAF runs callbacks synchronously so Radix post-open updates flush
  // inside act().
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback): number => {
    cb(0)
    return 0
  })
  vi.stubGlobal('cancelAnimationFrame', () => {})
})

afterEach(() => {
  act(() => {
    roots.forEach((root) => root.unmount())
  })
  roots.length = 0
  cancelPendingRefresh()
  // Unstub only AFTER unmounting — floating-ui's effect cleanup still calls
  // cancelAnimationFrame, which jsdom does not provide natively.
  vi.unstubAllGlobals()
})

// --- Pure badge --------------------------------------------------------------

describe('ActiveSessionsBadge', () => {
  it('renders nothing when no flag is set', () => {
    const container = render(<ActiveSessionsBadge flags={NO_BADGE_FLAGS} />)
    expect(container.innerHTML).toBe('')
  })

  it('renders one dot per true flag in fixed red→yellow→green→gray order', () => {
    const container = render(
      <ActiveSessionsBadge flags={makeFlags({ error: true, attention: true, active: true, paused: true, anyLive: true })} />,
    )
    const classes = dots(container).map((d) => d.className)
    expect(classes).toHaveLength(4)
    expect(classes[0]).toContain('bg-destructive')
    expect(classes[1]).toContain('bg-warning')
    expect(classes[2]).toContain('bg-success')
    expect(classes[3]).toContain('bg-muted-foreground')
  })

  it('renders only the flags that are true', () => {
    const container = render(<ActiveSessionsBadge flags={makeFlags({ error: true, active: true, anyLive: true })} />)
    const classes = dots(container).map((d) => d.className)
    expect(classes).toHaveLength(2)
    expect(classes[0]).toContain('bg-destructive')
    expect(classes[1]).toContain('bg-success')
  })

  it('styles every dot with design tokens and shifts each after the first left for overlap', () => {
    const container = render(
      <ActiveSessionsBadge flags={makeFlags({ error: true, attention: true, active: true, paused: true, anyLive: true })} />,
    )
    const ds = dots(container)
    for (const d of ds) {
      expect(d.className).toContain('size-2')
      expect(d.className).toContain('rounded-full')
      expect(d.className).toContain('ring-1')
      expect(d.className).toContain('ring-background')
    }
    expect(ds[0]?.className).not.toContain('-ml-1')
    for (const d of ds.slice(1)) {
      expect(d.className).toContain('-ml-1')
    }
  })
})

// --- Store-wired indicator ---------------------------------------------------

describe('ActiveSessionsIndicator', () => {
  it('is disabled with no dots before the session list loads', () => {
    const container = render(<ActiveSessionsIndicator />)
    const button = radarButton(container)
    expect(button.disabled).toBe(true)
    expect(dots(button)).toHaveLength(0)
  })

  it('is disabled with no dots when every session is idle', () => {
    useActiveSessionsStore.setState({ sessions: [makeSession(), makeSession({ id: 's2' })] })
    const container = render(<ActiveSessionsIndicator />)
    const button = radarButton(container)
    expect(button.disabled).toBe(true)
    expect(dots(button)).toHaveLength(0)
  })

  it('ignores archived sessions even with an unfinished failed task', () => {
    useActiveSessionsStore.setState({ sessions: [makeSession({ archived: true, unfinished_task_status: 'failed' })] })
    const container = render(<ActiveSessionsIndicator />)
    const button = radarButton(container)
    expect(button.disabled).toBe(true)
    expect(dots(button)).toHaveLength(0)
  })

  it('enables with a single green dot for an in-progress session', () => {
    useActiveSessionsStore.setState({ sessions: [makeSession({ unfinished_task_status: 'in_progress' })] })
    const container = render(<ActiveSessionsIndicator />)
    const button = radarButton(container)
    expect(button.disabled).toBe(false)
    const classes = dots(button).map((d) => d.className)
    expect(classes).toHaveLength(1)
    expect(classes[0]).toContain('bg-success')
  })

  it('shows a red dot for a failed session', () => {
    useActiveSessionsStore.setState({ sessions: [makeSession({ unfinished_task_status: 'failed' })] })
    const container = render(<ActiveSessionsIndicator />)
    const classes = dots(radarButton(container)).map((d) => d.className)
    expect(classes).toHaveLength(1)
    expect(classes[0]).toContain('bg-destructive')
  })

  it('shows a yellow dot for a session blocked on HITL via the pending override', () => {
    useActiveSessionsStore.setState({
      sessions: [makeSession({ unfinished_task_status: '' })],
      pendingOverride: { s1: true },
    })
    const container = render(<ActiveSessionsIndicator />)
    const button = radarButton(container)
    expect(button.disabled).toBe(false)
    const classes = dots(button).map((d) => d.className)
    expect(classes).toHaveLength(1)
    expect(classes[0]).toContain('bg-warning')
  })

  it('shows a gray dot for a paused session', () => {
    useActiveSessionsStore.setState({ sessions: [makeSession({ unfinished_task_status: 'paused' })] })
    const container = render(<ActiveSessionsIndicator />)
    const classes = dots(radarButton(container)).map((d) => d.className)
    expect(classes).toHaveLength(1)
    expect(classes[0]).toContain('bg-muted-foreground')
  })

  it('shows a green dot for a live task known only to chatStore', () => {
    useActiveSessionsStore.setState({ sessions: [makeSession({ unfinished_task_status: '' })] })
    useChatStore.setState({ taskActive: { s1: true } })
    const container = render(<ActiveSessionsIndicator />)
    const classes = dots(radarButton(container)).map((d) => d.className)
    expect(classes).toHaveLength(1)
    expect(classes[0]).toContain('bg-success')
  })

  it('aggregates all four flags into one ordered cluster', () => {
    useActiveSessionsStore.setState({
      sessions: [
        makeSession({ id: 's1', unfinished_task_status: 'failed' }),
        makeSession({ id: 's2', unfinished_task_status: 'in_progress' }),
        makeSession({ id: 's3', unfinished_task_status: 'paused' }),
        makeSession({ id: 's4', unfinished_task_status: '' }),
      ],
      pendingOverride: { s4: true },
    })
    const container = render(<ActiveSessionsIndicator />)
    const button = radarButton(container)
    expect(button.disabled).toBe(false)
    const classes = dots(button).map((d) => d.className)
    expect(classes).toHaveLength(4)
    expect(classes[0]).toContain('bg-destructive')
    expect(classes[1]).toContain('bg-warning')
    expect(classes[2]).toContain('bg-success')
    expect(classes[3]).toContain('bg-muted-foreground')
  })
})

// --- Placement in the sidebar header ------------------------------------------

describe('SidebarHeader placement', () => {
  it('renders the indicator immediately left of the CHAT/CODE toggle in chat mode, width-guarded', () => {
    useProjectStore.setState({ projects: makeProjects(), activeProjectId: 'no-proj' })
    const container = render(<SidebarHeader onToggleCollapse={vi.fn()} />)
    const header = container.firstElementChild as HTMLElement
    expect(header.classList.contains('@container')).toBe(true)

    const radar = radarButton(container)
    expect(radar.className).toContain('relative')
    // Width guard: below 228px of header width the button leaves layout so
    // the header stays intact at the 180px sidebar minimum.
    expect(radar.className).toContain('hidden')
    expect(radar.className).toContain('@min-[228px]:inline-flex')

    const toggle = radar.nextElementSibling as HTMLElement
    expect(toggle.classList.contains('bg-muted/60')).toBe(true)
    expect(toggle.textContent).toContain('CHAT')
    expect(toggle.textContent).toContain('CODE')
  })

  it('renders the indicator immediately left of the toggle in code mode too', () => {
    useProjectStore.setState({ projects: makeProjects(), activeProjectId: 'p1', lastRealProjectId: 'p1' })
    const container = render(<SidebarHeader onToggleCollapse={vi.fn()} />)
    const radar = radarButton(container)
    const toggle = radar.nextElementSibling as HTMLElement
    expect(toggle.classList.contains('bg-muted/60')).toBe(true)
    expect(toggle.textContent).toContain('CODE')
  })

  it('still renders the indicator while projects have not loaded (no toggle yet)', () => {
    const container = render(<SidebarHeader onToggleCollapse={vi.fn()} />)
    expect(radarButton(container)).not.toBeNull()
  })
})

// --- Dropdown: live sessions list ---------------------------------------------

/** Flush microtasks so portaled dropdown content mounts and async handlers
 *  settle (no real timers — safe under vi.useFakeTimers too). */
function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()

function pendingToolConfirm(): PendingActionsResponse {
  return {
    tool_confirms: [{ confirm_id: 'c1', tool: 'bash', args: '{}' }],
    step_limits: [],
    plan_approvals: [],
    ask_user: [],
    goal_proposals: [],
  }
}

describe('ActiveSessionsIndicator dropdown', () => {
  afterEach(() => {
    // Global stubs are installed/cleared at file level; the fake timers used
    // by the tooltip test are always returned to real here.
    vi.useRealTimers()
  })

  function renderIndicator(): HTMLElement {
    return render(
      <TooltipProvider>
        <ActiveSessionsIndicator />
      </TooltipProvider>,
    )
  }

  /** Radix's trigger toggles on `pointerdown` (left button), not click. */
  async function openMenu(container: HTMLElement): Promise<void> {
    act(() => {
      radarButton(container).dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    })
    await flush()
  }

  const menuItems = (): HTMLElement[] => [...document.body.querySelectorAll<HTMLElement>('[role="menuitem"]')]

  const rowName = (item: HTMLElement): string =>
    item.querySelector<HTMLElement>('[data-slot="tooltip-trigger"]')?.textContent ?? ''

  it('opens on pointerdown, refreshes the snapshot and lists only live sessions', async () => {
    useProjectStore.setState({ projects: makeProjects(), activeProjectId: 'p1' })
    useActiveSessionsStore.setState({
      sessions: [
        makeSession({ id: 'live', name: 'Live one', unfinished_task_status: 'in_progress', last_active_at: ago(120) }),
        makeSession({ id: 'idle', name: 'Idle one' }),
        makeSession({ id: 'archived', name: 'Archived', archived: true, unfinished_task_status: 'failed' }),
      ],
    })
    const container = renderIndicator()
    await openMenu(container)

    // The open path calls refreshNow() — exactly one immediate RPC (the
    // debounced mount refresh never fires inside the test).
    expect(vi.mocked(listAllSessions)).toHaveBeenCalledTimes(1)

    const items = menuItems()
    expect(items).toHaveLength(1)
    expect(items[0]!.textContent).toContain('Live one')
    // Relative time exactly as the regular session list renders it.
    expect(items[0]!.textContent).toContain('2h')
    // CODE mode icon — the owning project is a real project.
    expect(items[0]!.querySelector('svg[aria-label="Code session"]')).not.toBeNull()
    // Status dot for an in-progress session is green.
    const dot = items[0]!.querySelector('span[title="Task running"]')
    expect(dot).not.toBeNull()
    expect(dot!.className).toContain('bg-success')
  })

  it('marks a CHAT (No Project) session with the chat mode icon', async () => {
    useProjectStore.setState({ projects: makeProjects(), activeProjectId: 'no-proj' })
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 'chat-s', project_id: 'no-proj', unfinished_task_status: 'in_progress' })],
    })
    const container = renderIndicator()
    await openMenu(container)
    expect(menuItems()[0]!.querySelector('svg[aria-label="Chat session"]')).not.toBeNull()
  })

  it('shows the project name with a bullet before the title for CODE sessions only', async () => {
    useProjectStore.setState({ projects: makeProjects(), activeProjectId: 'p1' })
    useActiveSessionsStore.setState({
      sessions: [
        makeSession({ id: 'code-s', name: 'Code title', project_id: 'p1', unfinished_task_status: 'in_progress' }),
        makeSession({ id: 'chat-s', name: 'Chat title', project_id: 'no-proj', unfinished_task_status: 'in_progress' }),
      ],
    })
    const container = renderIndicator()
    await openMenu(container)

    const items = menuItems()
    const codeRow = items.find((it) => it.textContent!.includes('Code title'))!
    const chatRow = items.find((it) => it.textContent!.includes('Chat title'))!

    // CODE: project name ("Real") precedes the session title, separated by `•`.
    expect(codeRow.textContent).toContain('Real')
    expect(codeRow.textContent).toContain('•')
    const realIdx = codeRow.textContent!.indexOf('Real')
    const bulletIdx = codeRow.textContent!.indexOf('•')
    const titleIdx = codeRow.textContent!.indexOf('Code title')
    expect(realIdx).toBeGreaterThanOrEqual(0)
    expect(bulletIdx).toBeGreaterThan(realIdx)
    expect(titleIdx).toBeGreaterThan(bulletIdx)

    // CHAT (No Project): no project label and no bullet.
    expect(chatRow.textContent).not.toContain('•')
    expect(chatRow.textContent).not.toContain('Real')
  })

  it('sizes the dropdown dynamically by content, capped at 4/3 of the previous width', async () => {
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 's1', unfinished_task_status: 'in_progress' })],
    })
    const container = renderIndicator()
    await openMenu(container)
    const content = document.body.querySelector<HTMLElement>('[data-slot="dropdown-menu-content"]')
    expect(content).not.toBeNull()
    const classes = content!.className.split(/\s+/)
    expect(classes).toContain('min-w-80')
    expect(classes).toContain('max-w-[26.67rem]')
    expect(classes).not.toContain('w-80')
  })

  it('sorts pending/failed rows first, then by most recent activity', async () => {
    useActiveSessionsStore.setState({
      sessions: [
        makeSession({ id: 'running-old', name: 'Running old', unfinished_task_status: 'in_progress', last_active_at: ago(180) }),
        makeSession({ id: 'failed-recent', name: 'Failed recent', unfinished_task_status: 'failed', last_active_at: ago(120) }),
        makeSession({ id: 'pending-older', name: 'Pending older', last_active_at: ago(60) }),
        makeSession({ id: 'paused-newest', name: 'Paused newest', unfinished_task_status: 'paused', last_active_at: ago(30) }),
      ],
      pendingOverride: { 'pending-older': true },
    })
    const container = renderIndicator()
    await openMenu(container)
    expect(menuItems().map(rowName)).toEqual(['Pending older', 'Failed recent', 'Paused newest', 'Running old'])
  })

  it('marks the active session font-medium with no Check icon and no management buttons', async () => {
    useSessionStore.setState({ activeSessionId: 'live' })
    useActiveSessionsStore.setState({
      sessions: [
        makeSession({ id: 'live', name: 'Current', unfinished_task_status: 'in_progress' }),
        makeSession({ id: 'other', name: 'Other', unfinished_task_status: 'in_progress' }),
      ],
    })
    const container = renderIndicator()
    await openMenu(container)
    const items = menuItems()
    const currentName = items[0]!.querySelector<HTMLElement>('[data-slot="tooltip-trigger"]')!
    const otherName = items[1]!.querySelector<HTMLElement>('[data-slot="tooltip-trigger"]')!
    expect(currentName.className).toContain('font-medium')
    expect(otherName.className).not.toContain('font-medium')
    // Navigation-only surface: no check mark, no rename/pin/fork/archive/delete.
    expect(items[0]!.querySelector('svg.lucide-check')).toBeNull()
    expect(items[0]!.querySelectorAll('button')).toHaveLength(0)
  })

  it('truncates the title and reveals the full name in a tooltip when it overflows', async () => {
    vi.useFakeTimers()
    // jsdom has no layout — force every element to report overflow so
    // EllipsisHint's mount measurement flips its overflow gate on.
    const scrollDesc = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollWidth')!
    const clientDesc = Object.getOwnPropertyDescriptor(Element.prototype, 'clientWidth')!
    Object.defineProperty(Element.prototype, 'scrollWidth', { configurable: true, get: () => 400 })
    Object.defineProperty(Element.prototype, 'clientWidth', { configurable: true, get: () => 100 })
    try {
      const longName = 'A very long session title that cannot possibly fit the row'
      useActiveSessionsStore.setState({
        sessions: [makeSession({ id: 's1', name: longName, unfinished_task_status: 'in_progress' })],
      })
      const container = renderIndicator()
      await openMenu(container)

      const span = document.body.querySelector<HTMLElement>('[data-slot="tooltip-trigger"]')
      expect(span).not.toBeNull()
      expect(span!.className).toContain('truncate')

      await act(async () => {
        span!.dispatchEvent(new PointerEvent('pointerenter', { bubbles: true, pointerType: 'mouse' }))
        span!.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, pointerType: 'mouse' }))
        vi.advanceTimersByTime(TOOLTIP_DELAY_MS + 50)
      })
      // Radix portals the tooltip to document.body with the full name.
      expect(document.body.textContent).toContain(longName)
    } finally {
      Object.defineProperty(Element.prototype, 'scrollWidth', scrollDesc)
      Object.defineProperty(Element.prototype, 'clientWidth', clientDesc)
    }
  })

  it('scrolls long lists with max-h + overflow + custom scrollbar (no virtualization)', async () => {
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 's1', unfinished_task_status: 'in_progress' })],
    })
    const container = renderIndicator()
    await openMenu(container)
    const content = document.body.querySelector<HTMLElement>('[data-slot="dropdown-menu-content"]')
    expect(content).not.toBeNull()
    expect(content!.className).toContain('max-h-')
    expect(content!.className).toContain('overflow-y-auto')
    expect(content!.className).toContain('custom-scrollbar')
  })

  it('shows the empty fallback while open when nothing is live anymore', async () => {
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 's1', unfinished_task_status: 'in_progress' })],
    })
    const container = renderIndicator()
    await openMenu(container)
    expect(menuItems()).toHaveLength(1)

    await act(async () => {
      useActiveSessionsStore.setState({
        sessions: [makeSession({ id: 's1', unfinished_task_status: '' })],
      })
    })
    const content = document.body.querySelector<HTMLElement>('[data-slot="dropdown-menu-content"]')
    expect(content).not.toBeNull()
    expect(content!.textContent).toContain('No active sessions')
  })

  it('upgrades an unknown-pending session from green to yellow on mount (restart path)', async () => {
    useActiveSessionsStore.setState({
      sessions: [
        makeSession({ id: 's1', name: 'Blocked but DB says running', unfinished_task_status: 'in_progress' }),
        makeSession({ id: 's2', name: 'Already known pending', unfinished_task_status: 'in_progress' }),
        makeSession({ id: 'idle', name: 'Idle' }),
      ],
      pendingOverride: { s2: true },
    })
    vi.mocked(getPendingActions).mockImplementation(async (id: string) => (id === 's1' ? pendingToolConfirm() : null))

    // The mount sweep runs before the dropdown is ever opened: chatStore
    // starts empty, the DB still says in_progress, and only the backend knows
    // s1 is actually blocked on a prompt.
    const container = renderIndicator()

    // Only the unknown-pending live session is queried: s2 is already known
    // (override), the idle session is not live.
    expect(vi.mocked(getPendingActions)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(getPendingActions)).toHaveBeenCalledWith('s1')

    await act(async () => {
      await vi.waitFor(() => {
        expect(useActiveSessionsStore.getState().pendingOverride.s1).toBe(true)
      })
    })
    // After the sweep s1 is pending too — both rows collapse into a single
    // yellow dot (the user's response is the next step for both).
    const classes = dots(radarButton(container)).map((d) => d.className)
    expect(classes).toHaveLength(1)
    expect(classes[0]).toContain('bg-warning')
  })

  it('navigates within the current project without switching and closes the menu', async () => {
    useProjectStore.setState({ projects: makeProjects(), activeProjectId: 'p1' })
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 's1', name: 'Same project', unfinished_task_status: 'in_progress' })],
    })
    const container = renderIndicator()
    await openMenu(container)
    const item = menuItems()[0]!

    await act(async () => {
      item.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(switchProjectWithStateMock).not.toHaveBeenCalled()
    expect(selectSessionMock).toHaveBeenCalledWith('s1', 'p1')
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })

  it('switches the project before selecting a session from another project', async () => {
    useProjectStore.setState({ projects: makeProjects(), activeProjectId: 'no-proj' })
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 's1', name: 'Other project', unfinished_task_status: 'in_progress' })],
    })
    const container = renderIndicator()
    await openMenu(container)
    const item = menuItems()[0]!

    await act(async () => {
      item.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(switchProjectWithStateMock).toHaveBeenCalledWith('p1')
    expect(selectSessionMock).toHaveBeenCalledWith('s1', 'p1')
    // The switch must complete before the session is selected.
    const switchOrder = switchProjectWithStateMock.mock.invocationCallOrder[0]
    const selectOrder = selectSessionMock.mock.invocationCallOrder[0]
    expect(switchOrder).toBeDefined()
    expect(selectOrder).toBeDefined()
    expect(switchOrder!).toBeLessThan(selectOrder!)
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })

  it('keeps the menu open and selects nothing when the project switch fails', async () => {
    useProjectStore.setState({ projects: makeProjects(), activeProjectId: 'no-proj' })
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 's1', name: 'Broken switch', unfinished_task_status: 'in_progress' })],
    })
    switchProjectWithStateMock.mockRejectedValue(new Error('boom'))
    const container = renderIndicator()
    await openMenu(container)
    const item = menuItems()[0]!

    await act(async () => {
      item.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(selectSessionMock).not.toHaveBeenCalled()
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
  })
})
