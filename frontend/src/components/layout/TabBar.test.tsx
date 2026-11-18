// @vitest-environment jsdom
//
// TabBar — the workspace tab strip UI.
//
// The bar is presentational: activation flows through the TabController prop
// (faked here — the engine has its own suite), while add/close hit the real
// tabStore and titles/statuses derive live from the real project/session/
// chat stores by id.

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { TabBar } from './TabBar'
import { TooltipProvider } from '@/components/ui/tooltip'
import { createTab, useTabStore } from '@/stores/tabStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import { useActiveSessionsStore } from '@/stores/activeSessionsStore'
import type { TabController } from '@/hooks/useTabController'
import type { ProjectInfo, SessionInfo } from '@/types/models'

// --- fixtures ---

const NO_PROJECT: ProjectInfo = {
  id: 'no-project-id',
  name: 'No Project',
  workspace_path: '',
  is_external: false,
  is_no_project: true,
  created_at: '2026-01-01T00:00:00Z',
  last_active_at: '2026-01-01T00:00:00Z',
}

const PROJ: ProjectInfo = {
  id: 'proj-1',
  name: 'Demo',
  workspace_path: '/tmp/demo',
  is_external: false,
  is_no_project: false,
  created_at: '2026-01-01T00:00:00Z',
  last_active_at: '2026-01-01T00:00:00Z',
}

const SESS: SessionInfo = {
  id: 'sess-1',
  project_id: 'proj-1',
  name: 'Fix the bug',
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
}

function fakeController(): TabController {
  return {
    // Mimics the engine's optimistic flip: activate moves the active pointer
    // BEFORE resolving (a failure would resolve WITHOUT flipping — the
    // rollback the real engine performs).
    activate: vi.fn((tabId: string) => {
      useTabStore.getState().activateTab(tabId)
      return Promise.resolve()
    }),
    // Mimics engine.createAndActivate's observable store effect: the fresh
    // tab is appended and auto-activated (the full materialization is the
    // engine suite's business).
    createAndActivate: vi.fn((seed?: { projectId?: string | null; sessionId?: string | null }) => {
      const projects = useProjectStore.getState()
      const sessions = useSessionStore.getState()
      useTabStore.getState().addTab({
        projectId: seed?.projectId !== undefined ? seed.projectId : projects.activeProjectId,
        sessionId: seed?.sessionId !== undefined ? seed.sessionId : sessions.activeSessionId,
      })
      return Promise.resolve()
    }),
    writeBack: vi.fn(),
  } satisfies TabController
}

/** A controller whose activations FAIL the way the real engine's do: resolve
 *  without flipping (the rollback left the requested tab unmaterialized). */
function failingController(): TabController {
  return {
    activate: vi.fn((_tabId: string) => Promise.resolve()),
    createAndActivate: vi.fn(() => Promise.resolve()),
    writeBack: vi.fn(),
  } satisfies TabController
}

// --- harness ---

let container: HTMLDivElement
let root: Root

function renderBar(controller = fakeController()): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<TabBar controller={controller} />)
  })
}

function tabRows(): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('[role="tab"]'))
}

function rowByLabel(label: string): HTMLElement {
  const row = tabRows().find((r) => r.textContent?.includes(label))
  if (!row) throw new Error(`tab row "${label}" not found`)
  return row
}

function buttonByLabel(label: string): HTMLButtonElement {
  const btn = container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)
  if (!btn) throw new Error(`button "${label}" not found`)
  return btn
}

function flush(): Promise<void> {
  return act(async () => {})
}

beforeEach(() => {
  const first = createTab()
  useTabStore.setState({ tabs: [first], activeTabId: first.id })
  useProjectStore.setState({
    projects: [NO_PROJECT, PROJ],
    activeProjectId: PROJ.id,
    lastRealProjectId: PROJ.id,
  })
  useSessionStore.setState({ sessions: [SESS], activeSessionId: SESS.id })
  useChatStore.setState({ taskActive: {}, paused: {}, unfinishedTaskStatus: {} })
  useActiveSessionsStore.setState({ pendingOverride: {} })
})

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  container?.remove()
  document.body.innerHTML = ''
})

// --- rendered bar ---

describe('TabBar', () => {
  it('carries no Settings affordance — the strip stays tab-scoped (ADR-093)', () => {
    renderBar()
    // ADR-093: the gear lives in the sidebar header in BOTH flag states, the
    // strip keeps only tab-scoped affordances («+», close). While the sidebar
    // is collapsed there is NO entry point at all — Sidebar.test.tsx locks
    // that half of the parity; this locks the strip's half.
    expect(container.querySelector('button[aria-label="Settings"]')).toBeNull()
    expect(container.querySelector('[title="Settings"]')).toBeNull()
  })

  it('renders an ARIA tablist with live titles and roving tabindex', () => {
    const chatTab = createTab()
    const projTab = createTab({ projectId: PROJ.id, sessionId: SESS.id })
    useTabStore.setState({ tabs: [chatTab, projTab], activeTabId: projTab.id })
    renderBar()

    const list = container.querySelector('[role="tablist"]')
    expect(list?.getAttribute('aria-label')).toBe('Workspace tabs')

    const rows = tabRows()
    expect(rows).toHaveLength(2)
    // Live titles resolved by id against the stores (CHAT fallback for the
    // null-project tab; project: session for the CODE tab).
    expect(rows[0]?.textContent).toContain('New Tab')
    expect(rows[1]?.textContent).toContain('Demo: Fix the bug')
    // aria-selected + roving tabindex follow the active tab.
    expect(rows[0]?.getAttribute('aria-selected')).toBe('false')
    expect(rows[0]?.getAttribute('tabindex')).toBe('-1')
    expect(rows[1]?.getAttribute('aria-selected')).toBe('true')
    expect(rows[1]?.getAttribute('tabindex')).toBe('0')
  })

  it('activates through the controller on click', () => {
    const t1 = createTab()
    const t2 = createTab({ projectId: PROJ.id })
    useTabStore.setState({ tabs: [t1, t2], activeTabId: t1.id })
    const controller = fakeController()
    renderBar(controller)

    act(() => {
      rowByLabel('Demo').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(controller.activate).toHaveBeenCalledWith(t2.id)
  })

  it('closes an inactive tab directly, without activation', () => {
    const t1 = createTab()
    const t2 = createTab({ projectId: PROJ.id })
    useTabStore.setState({ tabs: [t1, t2], activeTabId: t1.id })
    const controller = fakeController()
    renderBar(controller)

    act(() => {
      buttonByLabel('Close Demo').click()
    })
    expect(useTabStore.getState().tabs.map((t) => t.id)).toEqual([t1.id])
    expect(controller.activate).not.toHaveBeenCalled()
  })

  it('closes the ACTIVE tab through the engine first, then removes it', async () => {
    const t1 = createTab()
    const t2 = createTab({ projectId: PROJ.id })
    useTabStore.setState({ tabs: [t1, t2], activeTabId: t1.id })
    const controller = fakeController()
    renderBar(controller)

    act(() => {
      buttonByLabel('Close New Tab').click()
    })
    // The neighbor went through the activation path BEFORE removal.
    expect(controller.activate).toHaveBeenCalledWith(t2.id)
    expect(useTabStore.getState().tabs.map((t) => t.id)).toEqual([t1, t2].map((t) => t.id))
    await flush()
    expect(useTabStore.getState().tabs.map((t) => t.id)).toEqual([t2.id])
    expect(useTabStore.getState().activeTabId).toBe(t2.id)
  })

  it('keeps the ACTIVE tab open when the neighbor activation fails (rollback = still active)', async () => {
    const t1 = createTab()
    const t2 = createTab({ projectId: PROJ.id })
    useTabStore.setState({ tabs: [t1, t2], activeTabId: t1.id })
    // A failed activation resolves WITHOUT flipping (the engine rolled
    // activeTabId back onto the closed tab) — the observable post-failure
    // state the real engine produces.
    const controller = failingController()
    renderBar(controller)

    act(() => {
      buttonByLabel('Close New Tab').click()
    })
    await flush()
    // The failed activation left t1 active (rollback) — closing onto the
    // unmaterialized neighbor would let the next write-back clobber its
    // remembered context, so the tab stays open instead.
    expect(useTabStore.getState().tabs.map((t) => t.id)).toEqual([t1.id, t2.id])
    expect(useTabStore.getState().activeTabId).toBe(t1.id)
  })

  it('closes on middle-click', () => {
    const t1 = createTab()
    const t2 = createTab({ projectId: PROJ.id })
    useTabStore.setState({ tabs: [t1, t2], activeTabId: t1.id })
    renderBar()

    act(() => {
      rowByLabel('Demo').dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 1 }))
    })
    expect(useTabStore.getState().tabs.map((t) => t.id)).toEqual([t1.id])
  })

  it('replaces the last tab on close and converges it to the live workspace', () => {
    const only = createTab()
    useTabStore.setState({ tabs: [only], activeTabId: only.id })
    const controller = fakeController()
    renderBar(controller)

    act(() => {
      buttonByLabel('Close New Tab').click()
    })
    // Invariant 1: the layer is never empty — a fresh default tab replaced it.
    const state = useTabStore.getState()
    expect(state.tabs).toHaveLength(1)
    expect(state.tabs[0]?.id).not.toBe(only.id)
    expect(state.activeTabId).toBe(state.tabs[0]?.id)
    // The fresh active tab was converged immediately (no stale-context lag).
    expect(controller.writeBack).toHaveBeenCalledTimes(1)
  })

  it('«+» creates the tab through the engine, seeded live (no direct addTab)', () => {
    const t1 = createTab()
    useTabStore.setState({ tabs: [t1], activeTabId: t1.id })
    const controller = fakeController()
    renderBar(controller)

    act(() => {
      buttonByLabel('New tab').click()
    })
    const state = useTabStore.getState()
    // The fresh tab exists and is active (the fake mimics the engine's
    // observable store effect).
    expect(state.tabs).toHaveLength(2)
    expect(state.activeTabId).not.toBe(t1.id)
    // Creation rode the engine's chain link — the same primitive the banner
    // click uses — with NO explicit seed: the fresh tab inherits the live
    // workspace read when the link runs, so an in-flight activation can
    // never be overtaken by a half-created tab.
    expect(controller.createAndActivate).toHaveBeenCalledTimes(1)
    expect(controller.createAndActivate).toHaveBeenCalledWith()
    expect(controller.activate).not.toHaveBeenCalled()
    expect(controller.writeBack).not.toHaveBeenCalled()
  })

  it('paints the session status dot from the single shared derivation', () => {
    const projTab = createTab({ projectId: PROJ.id, sessionId: SESS.id })
    const bareTab = createTab()
    useTabStore.setState({ tabs: [projTab, bareTab], activeTabId: projTab.id })
    renderBar()

    // Idle: no dot anywhere.
    expect(rowByLabel('Demo').querySelector('[title="Task running"]')).toBeNull()

    // Active (live chatStore flag).
    act(() => {
      useChatStore.setState({ taskActive: { [SESS.id]: true } })
    })
    expect(rowByLabel('Demo').querySelector('.bg-success')).not.toBeNull()
    expect(rowByLabel('New Tab').querySelector('.bg-success')).toBeNull()

    // Failed (DB snapshot fallback) beats a stale live running flag — the
    // deriveSessionStatus priority, verified through the UI surface.
    act(() => {
      useChatStore.setState({ taskActive: { [SESS.id]: true } })
      useSessionStore.setState({
        sessions: [{ ...SESS, unfinished_task_status: 'failed' }],
      })
    })
    expect(rowByLabel('Demo').querySelector('.bg-destructive')).not.toBeNull()
  })

  // --- cross-project label fallback (inactive tabs keep their full title) ---

  it('keeps the full title on an inactive tab of another project via the global snapshot', () => {
    const PROJ2: ProjectInfo = { ...PROJ, id: 'proj-2', name: 'Other' }
    const SESS2: SessionInfo = { ...SESS, id: 'sess-2', project_id: 'proj-2', name: 'Other task' }
    const inactiveTab = createTab({ projectId: PROJ.id, sessionId: SESS.id })
    const activeTab = createTab({ projectId: PROJ2.id, sessionId: SESS2.id })
    // The switch to proj-2 replaced the per-project list: only the ACTIVE
    // project's sessions are loaded (exactly what a real switch produces).
    useProjectStore.setState({
      projects: [NO_PROJECT, PROJ, PROJ2],
      activeProjectId: PROJ2.id,
      lastRealProjectId: PROJ2.id,
    })
    useSessionStore.setState({ sessions: [SESS2], activeSessionId: SESS2.id })
    // The global snapshot (kept across switches) still knows both sessions.
    useActiveSessionsStore.setState({ sessions: [SESS, SESS2], pendingOverride: {} })
    useTabStore.setState({ tabs: [inactiveTab, activeTab], activeTabId: activeTab.id })
    renderBar()

    expect(rowByLabel('Other: Other task')).toBeDefined()
    // Regression: without the snapshot fallback the inactive tab of the OTHER
    // project lost its session segment and degraded to the bare 'Demo'.
    expect(rowByLabel('Demo: Fix the bug')).toBeDefined()
  })

  it('keeps the CHAT tab session name while a real project is active', () => {
    const CHAT_SESS: SessionInfo = {
      ...SESS,
      id: 'sess-chat',
      project_id: NO_PROJECT.id,
      name: 'Quick question',
    }
    const chatTab = createTab({ projectId: NO_PROJECT.id, sessionId: CHAT_SESS.id })
    const projTab = createTab({ projectId: PROJ.id, sessionId: SESS.id })
    useActiveSessionsStore.setState({ sessions: [CHAT_SESS, SESS], pendingOverride: {} })
    useTabStore.setState({ tabs: [chatTab, projTab], activeTabId: projTab.id })
    renderBar()

    // sessionStore holds only the CODE project's list; the CHAT session
    // resolves from the snapshot — the inactive CHAT tab is not demoted to
    // the bare 'CHAT'.
    expect(rowByLabel('CHAT: Quick question')).toBeDefined()
    expect(rowByLabel('Demo: Fix the bug')).toBeDefined()
  })

  it('prefers the live per-project name over a stale snapshot copy', () => {
    const tab = createTab({ projectId: PROJ.id, sessionId: SESS.id })
    useActiveSessionsStore.setState({ sessions: [{ ...SESS, name: 'Stale copy' }], pendingOverride: {} })
    useTabStore.setState({ tabs: [tab], activeTabId: tab.id })
    renderBar()

    // The live store hit wins over the snapshot.
    expect(rowByLabel('Demo: Fix the bug')).toBeDefined()
    act(() => {
      useSessionStore.setState({ sessions: [{ ...SESS, name: 'Renamed' }] })
    })
    expect(rowByLabel('Demo: Renamed')).toBeDefined()
  })
})

// --- keyboard interaction on the strip (APG tabs, manual activation) ---

describe('TabBar keyboard', () => {
  /** Dispatch a bubbling, cancelable keydown; false when preventDefaulted. */
  function press(el: Element, key: string): boolean {
    let delivered = true
    act(() => {
      delivered = el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
    })
    return delivered
  }

  function setup(controller = fakeController()) {
    const t1 = createTab()
    const t2 = createTab({ projectId: PROJ.id })
    useTabStore.setState({ tabs: [t1, t2], activeTabId: t1.id })
    renderBar(controller)
    return { controller, t1, t2 }
  }

  it('carries data-tab-id so keyboard activation targets the right tab', () => {
    const { t1, t2 } = setup()
    expect(rowByLabel('New Tab').dataset.tabId).toBe(t1.id)
    expect(rowByLabel('Demo').dataset.tabId).toBe(t2.id)
  })

  it('Enter and Space activate the focused row, default suppressed', () => {
    const { controller, t2 } = setup()
    const row = rowByLabel('Demo')
    act(() => {
      row.focus()
    })
    expect(document.activeElement).toBe(row)

    expect(press(row, 'Enter')).toBe(false)
    expect(press(row, ' ')).toBe(false)
    expect(controller.activate).toHaveBeenNthCalledWith(1, t2.id)
    expect(controller.activate).toHaveBeenNthCalledWith(2, t2.id)
  })

  it('arrows move focus between rows without switching tabs', () => {
    const { controller, t1 } = setup()
    const rows = tabRows()
    act(() => {
      rows[0]!.focus()
    })

    press(rows[0]!, 'ArrowRight')
    expect(document.activeElement).toBe(rows[1])
    press(rows[1]!, 'ArrowLeft')
    expect(document.activeElement).toBe(rows[0])
    // Manual activation: focus-only arrows never switch.
    expect(controller.activate).not.toHaveBeenCalled()
    expect(useTabStore.getState().activeTabId).toBe(t1.id)
  })

  it('arrow navigation wraps around both ends', () => {
    setup()
    const rows = tabRows()
    act(() => {
      rows[0]!.focus()
    })
    press(rows[0]!, 'ArrowLeft')
    expect(document.activeElement).toBe(rows[1])
    press(rows[1]!, 'ArrowRight')
    expect(document.activeElement).toBe(rows[0])
  })

  it('Home/End jump to the first/last row', () => {
    setup()
    const rows = tabRows()
    act(() => {
      rows[0]!.focus()
    })
    press(rows[0]!, 'End')
    expect(document.activeElement).toBe(rows[1])
    press(rows[1]!, 'Home')
    expect(document.activeElement).toBe(rows[0])
  })

  it('keys on the nested close button keep native semantics', () => {
    const { controller } = setup()
    const closeButton = buttonByLabel('Close Demo')
    act(() => {
      closeButton.focus()
    })
    // Not intercepted: no preventDefault, no tab switch, no focus move.
    expect(press(closeButton, 'Enter')).toBe(true)
    expect(press(closeButton, 'ArrowRight')).toBe(true)
    expect(document.activeElement).toBe(closeButton)
    expect(controller.activate).not.toHaveBeenCalled()
  })

  it('unrelated keys are ignored entirely', () => {
    const { controller } = setup()
    const row = rowByLabel('New Tab')
    act(() => {
      row.focus()
    })
    expect(press(row, 'a')).toBe(true)
    expect(document.activeElement).toBe(row)
    expect(controller.activate).not.toHaveBeenCalled()
  })
})

// --- strip scroll/overflow layout (chevron pair + pinned «+») ---
//
// jsdom lays out nothing, so the strip's geometry is stubbed per element with
// Object.defineProperty (the same idiom useTabStripOverflow.test.tsx uses):
// scrollLeft/clientWidth/scrollWidth on the scroller are LIVE getters over
// the `metrics` object, each [role="tab"] row gets a fixed
// offsetLeft/offsetWidth, and scrollTo/scrollIntoView are spies. The scroller
// is stubbed after mount, so the strip "becomes" overflowing exactly the way
// a real one does — the hook re-measures through its scroll listener when a
// scroll event arrives, and the layout flips without a remount.

describe('TabBar strip overflow layout', () => {
  const TAB_WIDTH = 120
  const FITTING_VIEWPORT = 600 // five 120px tabs fit exactly
  const CLIPPED_VIEWPORT = 300 // two and a half tabs visible

  let metrics: { scrollLeft: number; clientWidth: number; scrollWidth: number }
  let scrollToSpy: Mock
  let scroller: HTMLDivElement

  /** Render the bar under the app-root TooltipProvider (the chevron hints'
      Radix channel) with five fixed-width tabs, then stub the strip geometry:
      row i occupies [i·TAB_WIDTH, (i+1)·TAB_WIDTH). Re-measures once through
      the hook's scroll listener so the stubbed geometry is what renders. */
  function renderStrip(viewport: number): string[] {
    const tabs = Array.from({ length: 5 }, () => createTab({ projectId: PROJ.id, sessionId: SESS.id }))
    useTabStore.setState({ tabs, activeTabId: tabs[0]!.id })
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => {
      root.render(
        <TooltipProvider>
          <TabBar controller={fakeController()} />
        </TooltipProvider>,
      )
    })
    metrics = { scrollLeft: 0, clientWidth: viewport, scrollWidth: tabs.length * TAB_WIDTH }
    scroller = container.querySelector<HTMLDivElement>('.no-scrollbar')!
    if (!scroller) throw new Error('scroller (.no-scrollbar) not found')
    Object.defineProperty(scroller, 'scrollLeft', { configurable: true, get: () => metrics.scrollLeft })
    Object.defineProperty(scroller, 'clientWidth', { configurable: true, get: () => metrics.clientWidth })
    Object.defineProperty(scroller, 'scrollWidth', { configurable: true, get: () => metrics.scrollWidth })
    scrollToSpy = vi.fn()
    Object.defineProperty(scroller, 'scrollTo', { configurable: true, value: scrollToSpy })
    tabRows().forEach((row, index) => {
      Object.defineProperty(row, 'offsetLeft', { configurable: true, value: index * TAB_WIDTH })
      Object.defineProperty(row, 'offsetWidth', { configurable: true, value: TAB_WIDTH })
    })
    emulateScroll(metrics.scrollLeft)
    return tabs.map((tab) => tab.id)
  }

  /** "Scroll" the strip: assign scrollLeft, then re-measure through the
      hook's passive scroll listener — the same path a real scroll drives. */
  function emulateScroll(left: number): void {
    metrics.scrollLeft = left
    act(() => {
      scroller.dispatchEvent(new Event('scroll'))
    })
  }

  it('fitting strip: no chevrons and the «+» stays in-flow inside the scroller', () => {
    renderStrip(FITTING_VIEWPORT)
    expect(container.querySelector('button[aria-label="Scroll tabs left"]')).toBeNull()
    expect(container.querySelector('button[aria-label="Scroll tabs right"]')).toBeNull()
    // In-flow: the «+» is the scroller's last child, right after the tablist.
    const plus = buttonByLabel('New tab')
    expect(container.querySelectorAll('button[aria-label="New tab"]')).toHaveLength(1)
    expect(scroller.contains(plus)).toBe(true)
    expect(scroller.lastElementChild).toBe(plus)
    expect(plus.previousElementSibling?.getAttribute('role')).toBe('tablist')
  })

  it('overflowing strip: both chevrons render, the «+» is pinned outside the scroller, «<» starts disabled', () => {
    renderStrip(CLIPPED_VIEWPORT)
    // At scrollLeft 0 nothing is hidden to the left, but 300px of strip
    // remain past the right edge.
    expect(buttonByLabel('Scroll tabs left').disabled).toBe(true)
    expect(buttonByLabel('Scroll tabs right').disabled).toBe(false)
    // Exactly one «+», OUT of the scroller: the bar's rightmost child, right
    // of the right chevron — and the scroller's sole child is the tablist.
    const plus = buttonByLabel('New tab')
    expect(container.querySelectorAll('button[aria-label="New tab"]')).toHaveLength(1)
    expect(scroller.contains(plus)).toBe(false)
    const bar = scroller.parentElement!
    expect(bar.lastElementChild).toBe(plus)
    expect(plus.previousElementSibling).toBe(buttonByLabel('Scroll tabs right'))
    expect(scroller.children).toHaveLength(1)
    expect(scroller.firstElementChild?.getAttribute('role')).toBe('tablist')
  })

  it('«>» scrolls to fully reveal the tab clipped past the right edge', () => {
    renderStrip(CLIPPED_VIEWPORT)
    // Viewport [0, 300) clips the third tab [240, 360): the target aligns its
    // right edge (360) with the viewport's right edge → scrollLeft 60.
    act(() => {
      buttonByLabel('Scroll tabs right').click()
    })
    expect(scrollToSpy).toHaveBeenCalledTimes(1)
    expect(scrollToSpy).toHaveBeenCalledWith({ left: 60, behavior: 'instant' })
  })

  it('«<» scrolls to fully reveal the tab clipped at the left edge', () => {
    renderStrip(CLIPPED_VIEWPORT)
    // Parked at the right extreme [300, 600) the third tab [240, 360) is
    // clipped at the LEFT edge; the target aligns its left edge → 240 —
    // the mirror image of the «>» target.
    emulateScroll(300)
    act(() => {
      buttonByLabel('Scroll tabs left').click()
    })
    expect(scrollToSpy).toHaveBeenCalledTimes(1)
    expect(scrollToSpy).toHaveBeenCalledWith({ left: 240, behavior: 'instant' })
  })

  it('each chevron disables at its own extreme', () => {
    renderStrip(CLIPPED_VIEWPORT)
    const max = metrics.scrollWidth - metrics.clientWidth
    emulateScroll(max)
    expect(buttonByLabel('Scroll tabs right').disabled).toBe(true)
    expect(buttonByLabel('Scroll tabs left').disabled).toBe(false)
    emulateScroll(0)
    expect(buttonByLabel('Scroll tabs left').disabled).toBe(true)
    expect(buttonByLabel('Scroll tabs right').disabled).toBe(false)
  })

  it('activating a tab scrolls that row into view', () => {
    const ids = renderStrip(CLIPPED_VIEWPORT)
    // jsdom ships no scrollIntoView; spy per row AFTER the mount effect has
    // already consumed the guarded no-op for the initial active tab.
    const spies = tabRows().map((row) => {
      const spy = vi.fn()
      Object.defineProperty(row, 'scrollIntoView', { configurable: true, value: spy })
      return spy
    })
    const third = ids[2]!
    act(() => {
      useTabStore.setState({ activeTabId: third })
    })
    expect(spies[2]).toHaveBeenCalledTimes(1)
    expect(spies[2]).toHaveBeenCalledWith({ block: 'nearest', inline: 'nearest' })
    spies.forEach((spy, index) => {
      if (index !== 2) expect(spy).not.toHaveBeenCalled()
    })
  })

  it('the scroller hides the native scrollbar without the custom-scrollbar token', () => {
    renderStrip(CLIPPED_VIEWPORT)
    expect(scroller.classList.contains('no-scrollbar')).toBe(true)
    expect(scroller.classList.contains('custom-scrollbar')).toBe(false)
  })
})
