// @vitest-environment jsdom
//
// SessionSelector grouped-by-branch mode (uiStore.sessionListGrouped):
//
// - the header List⇄FolderTree toggle (same glyph as the git panel's tree
//   view) sits on the search-input line, BEFORE the input, and flips the mode
//   without dismissing the menu;
// - grouped mode renders Worktrees → branch rows (GitBranch icon + chevron +
//   branch name) → member sessions, then Local → sessions; section headers
//   appear only when BOTH sections are non-empty (a lone Local section is the
//   flat view) and share the branch combo's uppercase section styling;
// - a branch-header selection (click or Enter) collapses/expands its group
//   (ephemeral state) and NEVER closes the dropdown; the Worktrees/Local section
//   headers collapse the same way;
// - the popup is a fixed wide `w-[28rem]`;
// - grouped mode separates the Worktrees and Local sections with the branch
//   combo's hairline divider (only when both sections render headers);
// - each managed branch row carries a "New session in this worktree" item
//   (the + action): it eagerly creates the session and presets its ADR-091
//   workspace choice to that branch (createBranch=false — the FIRST SEND
//   adopts the branch's existing managed tree per ADR-082), clears the
//   active selection and closes the
//   menu; it never toggles the branch's collapse state, and nothing but
//   managed branch rows (flat mode, Local/Archived sections) has the action;
// - an active search force-expands every group and section and hides empty ones;
// - the Archived section renders in BOTH modes and follows the display mode:
//   grouped mode shows archived managed sessions as collapsible branch rows
//   (chevron + GitBranch + branch + freshest-member time) carrying their own
//   THREE-action hover overlay — Pin/Unpin group (stays open), Unarchive all
//   (no confirmation), Delete all (plural confirmation) — plus archived local
//   sessions flat; archived branch rows toggle independently of the live rows
//   of the same branch; flat mode shows every archived session as a plain row
//   (no branch groups, per-session actions only);
// - every one of these controls is a REGISTERED Radix menu item — arrow keys
//   traverse them and Enter/Space activate them (a group collapsed with the
//   mouse stays reopenable from the keyboard; Radix prevents Tab inside the
//   menu, so unregistered controls would be unreachable).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import type { ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { withVirtualTimers } from '@/test/virtualTime'

const { apiMocks } = vi.hoisted(() => ({
  apiMocks: {
    createSession: vi.fn(),
    renameSession: vi.fn(),
    archiveSession: vi.fn(),
    deleteSession: vi.fn(),
    forkSession: vi.fn(),
    pinSession: vi.fn(),
  },
}))
vi.mock('@/api/sessions', () => apiMocks)

// The real logger writes expected error noise for failure branches; the
// component under test does not assert on logging.
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

type Binding = {
  kind: 'local' | 'managed_worktree'
  workspace_path: string
  worktree_name?: string
  branch?: string
}
type SessionRow = {
  id: string
  project_id: string
  name: string
  archived: boolean
  pinned: boolean
  last_active_at: string
  workspace_binding?: Binding
}

const { sessionMocks } = vi.hoisted(() => ({
  sessionMocks: {
    sessions: [] as unknown[] | null,
    activeSessionId: null as string | null,
    selectSession: vi.fn(),
    addSession: vi.fn(),
    removeSession: vi.fn(),
    updateSession: vi.fn(),
  },
}))
vi.mock('@/stores/sessionStore', () => {
  // useSessionActions' handlers read the store imperatively (busy lookups,
  // performDelete) — expose the same fixture object as the canonical state.
  const useSessionStore = (selector: (s: typeof sessionMocks) => unknown) => selector(sessionMocks)
  ;(useSessionStore as unknown as { getState: () => typeof sessionMocks }).getState = () => sessionMocks
  return { useSessionStore }
})

// Selector-aware chat store stub so every row's indicator resolves to 'idle'
// without touching live task state.
const { chatMocks } = vi.hoisted(() => ({
  chatMocks: {
    taskActive: {} as Record<string, boolean>,
    paused: {} as Record<string, boolean>,
    unfinishedTaskStatus: {} as Record<string, string | undefined>,
    messageOrder: {} as Record<string, string[]>,
    messages: {} as Record<string, unknown>,
  },
}))
vi.mock('@/stores/chatStore', () => ({
  useChatStore: Object.assign(
    (selector: (s: typeof chatMocks) => unknown) => selector(chatMocks),
    // The store's action surface used by the creation gestures AND the
    // delete finalization (ADR-091 seeding + per-entry cleanup): an
    // incomplete action set would throw inside the delete flow.
    {
      getState: () => ({
        ensureEmptyTranscript: vi.fn(),
        clearScrollPosition: vi.fn(),
        clearStepContextFill: vi.fn(),
      }),
    },
  ),
}))

// Delete failures surface through the app-wide runtime_error toast
// (useSessionActions); jsdom has no Wails runtime, so the emit is mocked.
const { runtimeMocks } = vi.hoisted(() => ({
  runtimeMocks: { emit: vi.fn() },
}))
vi.mock('@/api/runtime', () => runtimeMocks)

import { SessionSelector } from './SessionSelector'
import { useSessionWorkspaceStore } from '@/stores/sessionWorkspaceStore'
import { useUIStore } from '@/stores/uiStore'
import { usePinnedGroupsStore } from '@/stores/pinnedGroupsStore'
import { useProjectStore } from '@/stores/projectStore'
import type { ProjectInfo } from '@/types/models'

const NOW = Date.now()

function managedBinding(branch: string): Binding {
  return { kind: 'managed_worktree', workspace_path: `/tmp/wt-${branch}`, branch }
}

function makeSession(overrides: Partial<SessionRow> & { id: string; name: string }): SessionRow {
  return {
    project_id: 'proj-1',
    archived: false,
    pinned: false,
    last_active_at: new Date(NOW - 60_000).toISOString(),
    ...overrides,
  }
}

/** CODE project for the real projectStore — the branch-button draft needs it. */
function makeProject(): ProjectInfo {
  return {
    id: 'proj-1',
    name: 'Proj',
    workspace_path: '/w/proj',
    is_external: false,
    is_no_project: false,
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
  }
}

// Radix dropdown positioning observes the trigger with ResizeObserver, which
// jsdom does not provide.
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

let activeRoot: Root | null = null

async function render(el: ReactNode): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(el)
  })
  return container
}

/** Drain pending microtasks so async handlers and store updates settle. */
const flush = () =>
  act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })

async function openMenu(container: HTMLElement): Promise<HTMLElement> {
  const trigger = container.querySelector('button')!
  // The dispatch sits INSIDE the virtual-time window: Radix attaches its
  // post-open document-level work from a setTimeout(0), which must land on
  // the fake clock and fire during the advance — deterministically, with no
  // real time spent (timing-guard compliant).
  await withVirtualTimers(() => {
    trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
  }, 10)
  const menu = document.body.querySelector('[role="menu"]')
  expect(menu).not.toBeNull()
  return menu as HTMLElement
}

/** Branch-row collapse toggle — a registered menu item carrying the branch
 *  name and the GitBranch glyph (member session rows never carry that glyph). */
function branchToggle(menu: HTMLElement, branch: string): HTMLElement {
  const item = Array.from(menu.querySelectorAll('[role="menuitem"]')).find(
    (el) => (el.textContent ?? '').includes(branch) && el.querySelector('svg.lucide-git-branch'),
  )
  expect(item, `branch toggle for "${branch}"`).toBeTruthy()
  return item as HTMLElement
}

async function click(el: HTMLElement): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  await flush()
}

/** React 19 controlled input: native prototype setter + `input` event. */
async function typeInto(input: HTMLInputElement, text: string): Promise<void> {
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
  expect(setter).toBeTruthy()
  await act(async () => {
    setter!.call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await flush()
}

function menuText(menu: HTMLElement): string {
  return menu.textContent ?? ''
}

/**
 * A real bubbling keydown on `el` — the dispatch path Radix's React handlers
 * (content-level arrows, item Enter/Space) listen on. Roving-focus moves are
 * applied from a setTimeout, so the dispatch sits inside the virtual-time
 * window (deterministic, no real time spent — timing-guard compliant).
 */
async function pressKey(el: Element, key: string): Promise<void> {
  await withVirtualTimers(() => {
    el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
  }, 10)
}

beforeEach(() => {
  vi.clearAllMocks()
  sessionMocks.sessions = []
  sessionMocks.activeSessionId = null
  // The real (persisted) uiStore — start every test from the shipped default.
  useUIStore.setState({ sessionListGrouped: false })
  // CODE-mode project for the real projectStore — group pins are keyed by it.
  useProjectStore.setState({ activeProjectId: 'proj-1', projects: [makeProject()] })
  // The pin action toggles the REAL pinned-groups store — start unpinned.
  usePinnedGroupsStore.setState({ pinsByProject: {} })
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('SessionSelector — flat mode (default)', () => {
  it('renders sessions flat with no grouping chrome while sessionListGrouped is off', async () => {
    sessionMocks.sessions = [
      makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'm2', name: 'Feat A two', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'l1', name: 'Checkout work' }),
    ]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    const text = menuText(menu)
    // No section headers, no branch rows...
    expect(text).not.toContain('Worktrees')
    expect(text).not.toContain('feat-a')
    // ...but every session row is visible.
    expect(text).toContain('Feat A one')
    expect(text).toContain('Feat A two')
    expect(text).toContain('Checkout work')
    // The mode toggle is present and advertises the switch to grouped mode.
    const toggle = menu.querySelector<HTMLElement>('[role="menuitem"][title="Group sessions by repo branch"]')
    expect(toggle).not.toBeNull()
    expect(useUIStore.getState().sessionListGrouped).toBe(false)
    void container
  })

  it('shows a loading row instead of an empty menu body while sessions are not loaded (null sentinel)', async () => {
    // `sessions === null` is the store's "not yet loaded" signal (initial
    // value + resetForProjectSwitch) — the same one the sidebar's SessionList
    // reads. The menu shows a loading row; it must not read as an empty list.
    sessionMocks.sessions = null
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    const status = menu.querySelector('[role="status"]')
    expect(status).not.toBeNull()
    expect(status?.getAttribute('aria-label')).toBe('Loading sessions')
    expect(status?.textContent).toContain('Loading sessions')
    expect(menuText(menu)).not.toContain('No sessions')
    void container
  })

  it('does not show the loading row for a genuinely empty loaded list', async () => {
    sessionMocks.sessions = []
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    expect(menu.querySelector('[role="status"]')).toBeNull()
    void container
  })
})

describe('SessionSelector — mode toggle', () => {
  it('renders a large hover-styled tree toggle on the search line, before the input', async () => {
    // Five sessions so the search input renders.
    sessionMocks.sessions = [
      makeSession({ id: 'l1', name: 'One' }),
      makeSession({ id: 'l2', name: 'Two' }),
      makeSession({ id: 'l3', name: 'Three' }),
      makeSession({ id: 'l4', name: 'Four' }),
      makeSession({ id: 'l5', name: 'Five' }),
    ]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    const toggle = menu.querySelector<HTMLElement>('[role="menuitem"][title="Group sessions by repo branch"]')!
    expect(toggle).not.toBeNull()
    // Hover affordance (the user requirement) + a bigger hit area aligned
    // with the h-7 search input and the larger size-4 glyph.
    expect(toggle.className).toContain('hover:bg-muted')
    expect(toggle.className).toContain('h-7')
    expect(toggle.className).toContain('w-7')
    // The tree-view glyph, exactly like git → Changes' tree toggle.
    expect(toggle.querySelector('svg.lucide-folder-tree')).not.toBeNull()
    expect(toggle.querySelector('svg.lucide-git-branch')).toBeNull()

    // Same line as the search input, positioned BEFORE it.
    const input = menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!
    expect(input).not.toBeNull()
    expect(input.parentElement).toBe(toggle.parentElement)
    const rowChildren = Array.from(toggle.parentElement!.children)
    expect(rowChildren.indexOf(toggle)).toBeLessThan(rowChildren.indexOf(input))
  })

  it('still renders the toggle when the search input is hidden (< 5 sessions)', async () => {
    sessionMocks.sessions = [makeSession({ id: 'l1', name: 'Only one' })]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    expect(menu.querySelector('[role="menuitem"][title="Group sessions by repo branch"]')).not.toBeNull()
    expect(menu.querySelector('input')).toBeNull()
  })

  it('toggling switches to grouped mode without closing the menu', async () => {
    sessionMocks.sessions = [
      makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'l1', name: 'Checkout work' }),
    ]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    await click(menu.querySelector<HTMLElement>('[role="menuitem"][title="Group sessions by repo branch"]')!)

    // The menu is still open and now renders the branch group.
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    expect(useUIStore.getState().sessionListGrouped).toBe(true)
    expect(menuText(menu)).toContain('feat-a')
    // The toggle now advertises the way back.
    expect(menu.querySelector('[role="menuitem"][title="Show flat list"]')).not.toBeNull()
  })

  it('toggling back to flat restores the flat list without closing the menu', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = [
      makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'l1', name: 'Checkout work' }),
    ]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)
    expect(menuText(menu)).toContain('feat-a')

    await click(menu.querySelector<HTMLElement>('[role="menuitem"][title="Show flat list"]')!)

    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    expect(useUIStore.getState().sessionListGrouped).toBe(false)
    expect(menuText(menu)).not.toContain('feat-a')
    expect(menuText(menu)).toContain('Feat A one')
  })
})

describe('SessionSelector — grouped layout', () => {
  const groupedFixture = (): SessionRow[] => [
    makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a') }),
    makeSession({ id: 'm2', name: 'Feat A two', workspace_binding: managedBinding('feat-a') }),
    makeSession({ id: 'm3', name: 'Feat B one', workspace_binding: managedBinding('feat-b') }),
    makeSession({ id: 'l1', name: 'Checkout work' }),
  ]

  it('renders Worktrees header → branch rows → member sessions → Local header → local sessions', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = groupedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    const text = menuText(menu)
    // Section headers + branch names + all sessions present.
    expect(text).toContain('Worktrees')
    expect(text).toContain('feat-a')
    expect(text).toContain('feat-b')
    expect(text).toContain('Local')
    expect(text).toContain('Feat A one')
    expect(text).toContain('Feat A two')
    expect(text).toContain('Feat B one')
    expect(text).toContain('Checkout work')

    // Document order: Worktrees header before the branch rows before the Local
    // header before the local session rows.
    const order = ['Worktrees', 'feat-a', 'Feat A one', 'Local', 'Checkout work'].map((t) =>
      text.indexOf(t),
    )
    expect(order.every((i) => i >= 0)).toBe(true)
    expect([...order].sort((a, b) => a - b)).toEqual(order)

    // Branch rows carry the GitBranch icon (the fork action icon is a
    // different glyph, so this is specific to the group headers).
    for (const branch of ['feat-a', 'feat-b']) {
      expect(branchToggle(menu, branch).querySelector('svg')).not.toBeNull()
    }
  })

  it('a lone Local section renders as the flat view — no section headers', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = [
      makeSession({ id: 'l1', name: 'Checkout work', workspace_binding: { kind: 'local', workspace_path: '/tmp/proj' } }),
      makeSession({ id: 'l2', name: 'Older session' }), // no binding at all → Local
    ]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    const text = menuText(menu)
    expect(text).not.toContain('Worktrees')
    expect(text).not.toContain('Local')
    expect(text).toContain('Checkout work')
    expect(text).toContain('Older session')
    // No branch header buttons exist (every button with a git-branch icon
    // would be one).
    const branchHeaders = Array.from(menu.querySelectorAll('[role="menuitem"]')).filter((b) =>
      b.querySelector('svg.lucide-git-branch'),
    )
    expect(branchHeaders).toHaveLength(0)
  })

  it('section headers and branch toggles expose aria-expanded and name their live content region', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = [
      ...groupedFixture(),
      makeSession({ id: 'a1', name: 'Old work', archived: true }),
    ]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    /** Direct-child menu button with the exact label. */
    const headerIn = (m: HTMLElement, label: string) =>
      Array.from(m.children).find(
        (el) => el.getAttribute('role') === 'menuitem' && (el.textContent ?? '').trim() === label,
      ) as HTMLElement

    // The Worktrees header is expanded and controls a live region holding the
    // branch rows and their member sessions.
    const managedHeader = headerIn(menu, 'Worktrees')
    expect(managedHeader.getAttribute('aria-expanded')).toBe('true')
    const managedRegionId = managedHeader.getAttribute('aria-controls')!
    expect(managedRegionId).toBeTruthy()
    const managedRegion = document.getElementById(managedRegionId)
    expect(managedRegion?.textContent).toContain('feat-a')
    expect(managedRegion?.textContent).toContain('Feat A one')

    // A branch toggle controls exactly its own member sessions.
    const featAToggle = branchToggle(menu, 'feat-a')
    expect(featAToggle.getAttribute('aria-expanded')).toBe('true')
    const branchRegion = document.getElementById(featAToggle.getAttribute('aria-controls')!)
    expect(branchRegion?.textContent).toContain('Feat A one')
    expect(branchRegion?.textContent).not.toContain('Feat B one')

    // Collapsing flips the state and unmounts the region it named.
    await click(managedHeader)
    const menu2 = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(headerIn(menu2, 'Worktrees').getAttribute('aria-expanded')).toBe('false')
    expect(document.getElementById(managedRegionId)).toBeNull()

    // Archived starts collapsed — aria-expanded says so up front.
    const archivedHeader = Array.from(menu.querySelectorAll('[role="menuitem"]')).find((b) =>
      (b.textContent ?? '').startsWith('Archived'),
    ) as HTMLElement
    expect(archivedHeader.getAttribute('aria-expanded')).toBe('false')
  })
})

describe('SessionSelector — popup chrome (width & section separators)', () => {
  const bothSectionsFixture = (): SessionRow[] => [
    makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a') }),
    makeSession({ id: 'm2', name: 'Feat A two', workspace_binding: managedBinding('feat-a') }),
    makeSession({ id: 'l1', name: 'Checkout work' }),
  ]

  /** Index of a DIRECT menu child matching the predicate (-1 when absent). */
  function childIndex(menu: HTMLElement, pred: (el: Element) => boolean): number {
    return Array.from(menu.children).findIndex(pred)
  }

  const isSectionHeader = (el: Element, label: string) =>
    el.getAttribute('role') === 'menuitem' && (el.textContent ?? '').trim() === label
  const isSeparator = (el: Element) => el.getAttribute('role') === 'separator'

  it('opens a wider popup — fixed w-[28rem], the old w-96 is gone', async () => {
    sessionMocks.sessions = bothSectionsFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    expect(menu.className).toContain('w-[28rem]')
    expect(menu.className).not.toContain('w-96')
  })

  it('separates the Worktrees and Local sections with a hairline, like the branch combo', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = bothSectionsFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    const managedIdx = childIndex(menu, (el) => isSectionHeader(el, 'Worktrees'))
    const localIdx = childIndex(menu, (el) => isSectionHeader(el, 'Local'))
    expect(managedIdx).toBeGreaterThanOrEqual(0)
    expect(localIdx).toBeGreaterThan(managedIdx)
    expect(
      Array.from(menu.children)
        .slice(managedIdx, localIdx)
        .some(isSeparator),
    ).toBe(true)
  })

  it('keeps the Worktrees/Local separator while a section is collapsed — it belongs to the headers', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = bothSectionsFixture()
    const container = await render(<SessionSelector />)
    let menu = await openMenu(container)

    const managedIdx = childIndex(menu, (el) => isSectionHeader(el, 'Worktrees'))
    expect(managedIdx).toBeGreaterThanOrEqual(0)
    const managedBtn = menu.children[managedIdx] as HTMLElement
    await click(managedBtn)
    menu = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(menuText(menu)).not.toContain('Feat A one')

    const localIdx = childIndex(menu, (el) => isSectionHeader(el, 'Local'))
    expect(localIdx).toBeGreaterThan(managedIdx)
    expect(
      Array.from(menu.children)
        .slice(managedIdx, localIdx)
        .some(isSeparator),
    ).toBe(true)
  })

  it('adds no separators in the lone-Local flat view beyond the New Session divider', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = [
      makeSession({ id: 'l1', name: 'Checkout work' }),
      makeSession({ id: 'l2', name: 'Older session' }),
    ]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    const seps = Array.from(menu.children).filter(isSeparator)
    expect(seps).toHaveLength(1)
  })
})

describe('SessionSelector — branch collapse', () => {
  it('clicking a branch header collapses its group and keeps the menu open; clicking again re-expands', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = [
      makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'm2', name: 'Feat A two', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'l1', name: 'Checkout work' }),
    ]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // Collapse the branch group.
    await click(branchToggle(menu, 'feat-a'))
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    expect(menuText(menu)).not.toContain('Feat A one')
    expect(menuText(menu)).not.toContain('Feat A two')
    // Sibling content (local section) is untouched.
    expect(menuText(menu)).toContain('Checkout work')

    // Expand again.
    await click(branchToggle(menu, 'feat-a'))
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    expect(menuText(menu)).toContain('Feat A one')
    expect(menuText(menu)).toContain('Feat A two')
  })
})

describe('SessionSelector — section collapse', () => {
  /** Section header item — found by its exact label (Archived carries a count). */
  function sectionHeader(menu: HTMLElement, label: string): HTMLElement {
    const btn = Array.from(menu.querySelectorAll('[role="menuitem"]')).find((b) => {
      const t = (b.textContent ?? '').trim()
      return t === label || t.startsWith(`${label} (`)
    })
    expect(btn, `section header "${label}"`).toBeTruthy()
    return btn as HTMLElement
  }

  const bothSectionsFixture = (): SessionRow[] => [
    makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a') }),
    makeSession({ id: 'l1', name: 'Checkout work' }),
  ]

  it('renders the section headers in the branch combo style — uppercase, semibold, chevron, hover', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = bothSectionsFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    for (const label of ['Worktrees', 'Local']) {
      const btn = sectionHeader(menu, label)
      expect(btn.className).toContain('uppercase')
      expect(btn.className).toContain('font-semibold')
      expect(btn.className).toContain('text-xs')
      expect(btn.className).toContain('hover:bg-muted')
      expect(btn.querySelector('svg.lucide-chevron-down')).not.toBeNull()
    }
  })

  it('collapsing the Worktrees section hides its branch rows and keeps Local; re-expanding restores', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = bothSectionsFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    await click(sectionHeader(menu, 'Worktrees'))
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    expect(menuText(menu)).not.toContain('feat-a')
    expect(menuText(menu)).not.toContain('Feat A one')
    // The Local section is untouched.
    expect(menuText(menu)).toContain('Checkout work')

    await click(sectionHeader(menu, 'Worktrees'))
    expect(menuText(menu)).toContain('feat-a')
    expect(menuText(menu)).toContain('Feat A one')
  })

  it('collapsing the Local section hides local sessions and keeps Worktrees; re-expanding restores', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = bothSectionsFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    await click(sectionHeader(menu, 'Local'))
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    expect(menuText(menu)).not.toContain('Checkout work')
    // The Worktrees branch group is untouched.
    expect(menuText(menu)).toContain('Feat A one')

    await click(sectionHeader(menu, 'Local'))
    expect(menuText(menu)).toContain('Checkout work')
  })

  it('an active search force-expands a collapsed section; clearing restores the collapsed state', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = [
      makeSession({ id: 'm1', name: 'Alpha work', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'm2', name: 'Alpha two', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'l1', name: 'Checkout one' }),
      makeSession({ id: 'l2', name: 'Checkout two' }),
      makeSession({ id: 'l3', name: 'Checkout three' }),
    ]
    const container = await render(<SessionSelector />)
    let menu = await openMenu(container)

    // Collapse the whole Worktrees section: branch content disappears.
    await click(sectionHeader(menu, 'Worktrees'))
    expect(menuText(menu)).not.toContain('Alpha work')

    // Searching for a member inside the collapsed section force-expands it...
    const input = menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!
    await typeInto(input, 'alpha')
    menu = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(menuText(menu)).toContain('Alpha work')
    expect(menuText(menu)).toContain('feat-a')

    // ...and clearing restores the collapsed section.
    await typeInto(menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!, '')
    menu = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(menuText(menu)).not.toContain('Alpha work')
  })
})

describe('SessionSelector — search in grouped mode', () => {
  // Five sessions so the search box renders (showSearch at >= 5).
  const searchableFixture = (): SessionRow[] => [
    makeSession({ id: 'm1', name: 'Alpha work', workspace_binding: managedBinding('feat-a') }),
    makeSession({ id: 'm2', name: 'Beta work', workspace_binding: managedBinding('feat-b') }),
    makeSession({ id: 'l1', name: 'Checkout one' }),
    makeSession({ id: 'l2', name: 'Checkout two' }),
    makeSession({ id: 'l3', name: 'Checkout three' }),
  ]

  it('group delete during a search targets the FULL branch membership, and the dialog says so', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    // feat-a holds TWO sessions; the search below will leave one visible.
    sessionMocks.sessions = [
      makeSession({ id: 'm1', name: 'Alpha work', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'm2', name: 'Zeta work', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'l1', name: 'Checkout one' }),
      makeSession({ id: 'l2', name: 'Checkout two' }),
      makeSession({ id: 'l3', name: 'Checkout three' }),
    ]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // The search narrows feat-a to ONE visible row.
    await typeInto(menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!, 'Alpha')
    const narrowed = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(menuText(narrowed)).toContain('Alpha work')
    expect(menuText(narrowed)).not.toContain('Zeta work')

    const del = Array.from(
      branchToggle(narrowed, 'feat-a').querySelectorAll<HTMLElement>('[role="menuitem"]'),
    ).find((el) => (el.getAttribute('title') ?? '').startsWith('Delete sessions'))
    await click(del!)

    // The plural confirmation counts the branch's FULL membership — both
    // sessions — not the single row the search left visible.
    const dialog = document.querySelector('[role="dialog"]')
    expect(dialog).not.toBeNull()
    expect(dialog!.textContent).toContain('Branch "feat-a" groups 2 sessions')

    const confirm = Array.from(dialog!.querySelectorAll('button')).find((b) =>
      (b.textContent ?? '').trim().startsWith('Delete'),
    )
    await click(confirm!)
    expect(apiMocks.deleteSession).toHaveBeenCalledWith('m1')
    expect(apiMocks.deleteSession).toHaveBeenCalledWith('m2')
  })

  it('force-expands collapsed groups so matches stay reachable, and hides empty groups', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = searchableFixture()
    const container = await render(<SessionSelector />)
    let menu = await openMenu(container)

    // Collapse feat-a first: its member must disappear.
    await click(branchToggle(menu, 'feat-a'))
    expect(menuText(menu)).not.toContain('Alpha work')

    // Searching for the hidden member force-expands its group...
    const input = menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!
    expect(input).not.toBeNull()
    await typeInto(input, 'alpha')

    menu = document.body.querySelector('[role="menu"]') as HTMLElement
    const text = menuText(menu)
    expect(text).toContain('Alpha work')
    // ...while groups without matches are hidden entirely (branch row AND
    // member sessions) and non-matching local rows disappear too.
    expect(text).not.toContain('feat-b')
    expect(text).not.toContain('Beta work')
    expect(text).not.toContain('Checkout one')

    // Clearing the search restores the ephemeral collapsed state.
    await typeInto(menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!, '')
    menu = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(menuText(menu)).not.toContain('Alpha work')
    expect(menuText(menu)).toContain('Beta work')
  })
})

describe('SessionSelector — whitespace-only search', () => {
  // Five active sessions (search-input threshold) + two archived rows: a
  // blank search must filter NOTHING, so the grouped sections (trimmed
  // grouping model), the flat list and the Archived section (filterFn) all
  // agree. The untrimmed filter used to empty the Archived section and drop
  // it from the menu entirely.
  const whitespaceFixture = (): SessionRow[] => [
    makeSession({ id: 'm1', name: 'Alpha work', workspace_binding: managedBinding('feat-a') }),
    makeSession({ id: 'l1', name: 'Checkout one' }),
    makeSession({ id: 'l2', name: 'Checkout two' }),
    makeSession({ id: 'l3', name: 'Checkout three' }),
    makeSession({ id: 'a1', name: 'Old archived', archived: true }),
    makeSession({ id: 'a2', name: 'Elder archived', archived: true }),
  ]

  it.each([false, true])(
    'a blank search hides nothing in %s mode — Archived keeps its full count',
    async (grouped) => {
      useUIStore.setState({ sessionListGrouped: grouped })
      sessionMocks.sessions = whitespaceFixture()
      const container = await render(<SessionSelector />)
      let menu = await openMenu(container)

      const input = menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!
      expect(input).not.toBeNull()
      await typeInto(input, '   ')

      menu = document.body.querySelector('[role="menu"]') as HTMLElement
      expect(menu).not.toBeNull()
      const text = menuText(menu)
      expect(text).toContain('Alpha work')
      expect(text).toContain('Checkout one')
      expect(text).toContain('Checkout three')
      expect(text).toContain('Archived (2)')

      // The section is still interactive: expanding shows every archived row.
      await click(
        Array.from(menu.querySelectorAll('[role="menuitem"]')).find((b) => b.textContent?.includes('Archived'))! as HTMLElement,
      )
      expect(menuText(menu)).toContain('Old archived')
      expect(menuText(menu)).toContain('Elder archived')
    },
  )
})

describe('SessionSelector — search input focus', () => {
  // Radix's menu content runs its typeahead for EVERY printable character
  // typed anywhere inside the menu (no input-target exclusion in
  // @radix-ui/react-menu): the search field's first real keystroke used to
  // focus the first matching menu item and blur the field. The whitespace
  // and typeInto-based tests never saw it — they emit no keydown, or a
  // space that matches no item's text.
  const focusFixture = (): SessionRow[] => [
    makeSession({ id: 'l1', name: 'Alpha work' }),
    makeSession({ id: 'l2', name: 'Beta work' }),
    makeSession({ id: 'l3', name: 'Gamma work' }),
    makeSession({ id: 'l4', name: 'Delta work' }),
    makeSession({ id: 'l5', name: 'Echo work' }),
  ]

  it('keeps focus when the first keystroke matches a menu item ("New Session...")', async () => {
    sessionMocks.sessions = focusFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    const input = menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!
    // The dispatch must sit INSIDE the virtual-time window so Radix's
    // setTimeout(0) typeahead focus lands on the fake clock and the advance
    // deterministically fires it.
    await withVirtualTimers(() => {
      input.focus()
      // A REAL keydown — `typeInto` (native setter + input event) never
      // emits one, which is exactly what Radix's typeahead listens for.
      // 'n' matches only the footer "New Session..." item.
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'n', bubbles: true }))
    })

    expect(document.activeElement).toBe(input)
  })

  it('does not swallow keys the menu still needs — Escape closes the menu while typing', async () => {
    sessionMocks.sessions = focusFixture()
    const container = await render(<SessionSelector />)
    await openMenu(container)

    const input = document.body.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!
    await act(async () => {
      input.focus()
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    await flush()

    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })
})

describe('SessionSelector — grouped sessions still select', () => {
  it('clicking a session row inside a branch group selects it and closes the menu', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = [
      makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a') }),
      makeSession({ id: 'l1', name: 'Checkout work' }),
    ]
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    const row = Array.from(menu.querySelectorAll('[role="menuitem"]')).find((el) =>
      el.textContent?.includes('Feat A one'),
    )
    expect(row).toBeTruthy()
    await act(async () => {
      row!.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      row!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(sessionMocks.selectSession).toHaveBeenCalledWith('m1', 'proj-1')
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })
})

describe('SessionSelector — archived section (branch groups + flat local)', () => {
  /** Live content (one managed branch + one local row) plus an archive with
   *  BOTH kinds: two archived sessions on managed branch feat-a (2h / 5h
   *  stale — the row must show the fresher clock) and one archived local
   *  session. Archived header count: 3. */
  const archivedFixture = (): SessionRow[] => [
    makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a') }),
    makeSession({ id: 'l1', name: 'Checkout work' }),
    makeSession({
      id: 'am1',
      name: 'Archived feat older',
      archived: true,
      workspace_binding: managedBinding('feat-a'),
      last_active_at: new Date(NOW - 5 * 3600_000).toISOString(),
    }),
    makeSession({
      id: 'am2',
      name: 'Archived feat newer',
      archived: true,
      workspace_binding: managedBinding('feat-a'),
      last_active_at: new Date(NOW - 2 * 3600_000).toISOString(),
    }),
    makeSession({
      id: 'al1',
      name: 'Archived local one',
      archived: true,
      last_active_at: new Date(NOW - 90_000).toISOString(),
    }),
  ]

  /** The archived branch row for `branch` — matched by its collapse title, so
   *  the LIVE branch row of the same branch name (grouped mode) never wins. */
  function archivedBranchToggle(menu: HTMLElement, branch: string): HTMLElement {
    const item = Array.from(
      menu.querySelectorAll<HTMLElement>('[role="menuitem"][title$="archived branch sessions"]'),
    ).find((el) => (el.textContent ?? '').includes(branch))
    expect(item, `archived branch toggle for "${branch}"`).toBeTruthy()
    return item as HTMLElement
  }

  /** The Archived section header ("Archived (n)"). */
  function archivedHeader(menu: HTMLElement): HTMLElement {
    const el = Array.from(menu.querySelectorAll('[role="menuitem"]')).find((item) =>
      /^Archived \(\d+\)$/.test((item.textContent ?? '').trim()),
    )
    expect(el, 'Archived section header').toBeTruthy()
    return el as HTMLElement
  }

  it('grouped mode: archived managed sessions render as collapsible branch rows with the freshest time; archived local stays flat', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // Collapsed by default — the header counts BOTH archived kinds, no rows leak.
    expect(menuText(menu)).toContain('Archived (3)')
    expect(menuText(menu)).not.toContain('Archived feat')
    expect(menuText(menu)).not.toContain('Archived local one')

    await click(archivedHeader(menu))

    // Archived local is flat — visible immediately, no extra toggle.
    expect(menuText(menu)).toContain('Archived local one')

    // The archived branch row shows the FRESHEST member's time (2h, not the
    // 5h stale member's). Like the live branch groups, archived groups start
    // expanded — members render right away, indented.
    const row = archivedBranchToggle(menu, 'feat-a')
    expect(row.textContent).toContain('2h')
    expect(row.textContent).not.toContain('5h')
    expect(row.getAttribute('aria-expanded')).toBe('true')
    expect(menuText(menu)).toContain('Archived feat newer')
    expect(menuText(menu)).toContain('Archived feat older')
    const member = Array.from(menu.querySelectorAll('[role="menuitem"]')).find((el) =>
      (el.textContent ?? '').includes('Archived feat newer'),
    )
    expect(member?.className).toContain('pl-6')

    // The row toggle collapses its members; the menu never closed.
    await click(row)
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    expect(menuText(menu)).not.toContain('Archived feat newer')
  })

  it('the grouping toggle drives the Archived section live — branch groups appear and disappear without closing the menu', async () => {
    useUIStore.setState({ sessionListGrouped: false })
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)
    await click(archivedHeader(menu))

    // Flat start: rows visible, no archived branch row.
    expect(menuText(menu)).toContain('Archived feat newer')
    expect(menu.querySelectorAll('[role="menuitem"][title$="archived branch sessions"]')).toHaveLength(0)

    // Toggle grouped ON: the archived branch row appears (members start
    // expanded) and the menu stays open.
    await click(menu.querySelector<HTMLElement>('[role="menuitem"][title="Group sessions by repo branch"]')!)
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    const branchRow = archivedBranchToggle(menu, 'feat-a')
    expect(branchRow.getAttribute('aria-expanded')).toBe('true')
    expect(menuText(menu)).toContain('Archived feat newer')

    // Toggle back OFF: the branch chrome disappears, the rows remain.
    await click(menu.querySelector<HTMLElement>('[role="menuitem"][title="Show flat list"]')!)
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    expect(menu.querySelectorAll('[role="menuitem"][title$="archived branch sessions"]')).toHaveLength(0)
    expect(menuText(menu)).toContain('Archived feat newer')
    expect(menuText(menu)).toContain('Archived local one')
  })

  it('archived branch rows carry a THREE-action overlay — Pin/Unpin, Restore, Delete', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)
    await click(archivedHeader(menu))

    // The archived row registers exactly its three action items inside the
    // toggle — no "+" (a draft has no meaning in the archive), no Archive.
    const actions = Array.from(
      archivedBranchToggle(menu, 'feat-a').querySelectorAll<HTMLElement>('[role="menuitem"]'),
    ).map((el) => el.getAttribute('title'))
    expect(actions).toEqual([
      'Pin group (feat-a)',
      'Restore sessions (feat-a)',
      'Delete sessions (feat-a)',
    ])

    // Cross-check: the LIVE feat-a row of the same branch carries its own
    // four-item overlay — the assertion above is not vacuous.
    expect(branchToggle(menu, 'feat-a').querySelectorAll('[role="menuitem"]')).toHaveLength(4)
  })

  it('archived group Pin toggles the GROUP pin and keeps the menu open', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)
    await click(archivedHeader(menu))

    const pin = Array.from(
      archivedBranchToggle(menu, 'feat-a').querySelectorAll<HTMLElement>('[role="menuitem"]'),
    ).find((el) => (el.getAttribute('title') ?? '').startsWith('Pin group'))
    expect(pin).toBeTruthy()
    await click(pin!)

    // The GROUP pin flipped in the store; no per-member RPC fired.
    expect(usePinnedGroupsStore.getState().pinsByProject['proj-1']).toContain('feat-a')
    expect(apiMocks.pinSession).not.toHaveBeenCalled()

    // A state flip, not an action — the menu never closed.
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()

    // The archived row itself now carries the always-visible pin badge.
    const pinnedRowIcons = Array.from(
      archivedBranchToggle(menu, 'feat-a').querySelectorAll('svg.lucide-pin'),
    )
    expect(pinnedRowIcons.some((svg) => !svg.closest('span.opacity-0'))).toBe(true)
  })

  it('stale group pins are pruned against the UNFILTERED model — live or archived membership keeps a pin', async () => {
    // A pin on a branch whose last session is GONE, next to feat-a (which
    // holds BOTH live and archived groups in the fixture): only the stale
    // one may drop — and feat-a must survive even while a search could
    // temporarily hide it (the prune reads the unfiltered grouping).
    usePinnedGroupsStore.setState({ pinsByProject: { 'proj-1': ['ghost-branch', 'feat-a'] } })
    sessionMocks.sessions = archivedFixture()

    await render(<SessionSelector />)

    expect(usePinnedGroupsStore.getState().pinsByProject['proj-1']).toEqual(['feat-a'])
  })

  it('a not-yet-loaded list (null) never prunes — the project-switch transient is safe', async () => {
    usePinnedGroupsStore.setState({ pinsByProject: { 'proj-1': ['ghost-branch'] } })
    // `null` = the reset/switch transient: an empty model then would wipe
    // the destination project's pins.
    sessionMocks.sessions = null as unknown as unknown[]

    await render(<SessionSelector />)

    expect(usePinnedGroupsStore.getState().pinsByProject['proj-1']).toEqual(['ghost-branch'])
  })

  it('archived group Restore unarchives every member without a confirmation', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)
    await click(archivedHeader(menu))

    const restore = Array.from(
      archivedBranchToggle(menu, 'feat-a').querySelectorAll<HTMLElement>('[role="menuitem"]'),
    ).find((el) => (el.getAttribute('title') ?? '').startsWith('Restore sessions'))
    await click(restore!)

    // Restore is non-destructive: the archive RPC fires per archived member
    // (the toggle semantic un-archives), nothing else — and no dialog arms.
    expect(apiMocks.archiveSession).toHaveBeenCalledTimes(2)
    expect(apiMocks.archiveSession).toHaveBeenCalledWith('am1')
    expect(apiMocks.archiveSession).toHaveBeenCalledWith('am2')
    expect(apiMocks.archiveSession).not.toHaveBeenCalledWith('m1')
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })

  it('archived group Delete arms the plural confirmation naming the branch, then deletes every member', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)
    await click(archivedHeader(menu))

    const del = Array.from(
      archivedBranchToggle(menu, 'feat-a').querySelectorAll<HTMLElement>('[role="menuitem"]'),
    ).find((el) => (el.getAttribute('title') ?? '').startsWith('Delete sessions'))
    await click(del!)

    // Plural copy, branch-named — deleting always asks, archived or not.
    const dialog = document.querySelector('[role="dialog"]')
    expect(dialog).not.toBeNull()
    expect(dialog!.textContent).toContain('Delete sessions?')
    expect(dialog!.textContent).toContain('Branch "feat-a" groups 2 sessions')

    // Confirm applies the operation to EVERY member.
    const confirm = Array.from(dialog!.querySelectorAll('button')).find((b) =>
      (b.textContent ?? '').trim().startsWith('Delete'),
    )
    await click(confirm!)
    expect(apiMocks.deleteSession).toHaveBeenCalledTimes(2)
    expect(apiMocks.deleteSession).toHaveBeenCalledWith('am1')
    expect(apiMocks.deleteSession).toHaveBeenCalledWith('am2')
    expect(sessionMocks.removeSession).toHaveBeenCalledWith('am1')
    expect(sessionMocks.removeSession).toHaveBeenCalledWith('am2')
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })

  it('flat mode: the Archived section renders plain rows — no branch groups, no group actions', async () => {
    // The shipped default mode (sessionListGrouped stays false): the grouping
    // toggle must reach the Archived section too — archived managed sessions
    // lose their branch rows here and flatten into plain rows.
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    expect(menuText(menu)).toContain('Archived (3)')
    await click(archivedHeader(menu))

    // Both archived kinds surface as plain rows, no extra toggle.
    const text = menuText(menu)
    expect(text).toContain('Archived feat newer')
    expect(text).toContain('Archived feat older')
    expect(text).toContain('Archived local one')

    // NO branch chrome anywhere: no GitBranch glyph (the flat active list has
    // none either), no group-level actions — a flat archived row manages
    // itself through the per-session Unarchive.
    expect(menu.querySelectorAll('[role="menuitem"] svg.lucide-git-branch')).toHaveLength(0)
    const titles = Array.from(menu.querySelectorAll('[title]')).map((el) => el.getAttribute('title'))
    expect(titles).not.toContain('Pin group (feat-a)')
    expect(titles).not.toContain('Restore sessions (feat-a)')
    expect(titles).not.toContain('Delete sessions (feat-a)')

    // The three archived rows are un-indented (no branch nesting) and each
    // carries the per-session Unarchive action.
    const archivedRows = Array.from(menu.querySelectorAll('[role="menuitem"]')).filter((el) =>
      /^(Archived feat|Archived local)/.test((el.textContent ?? '').trim()),
    )
    expect(archivedRows).toHaveLength(3)
    for (const rowEl of archivedRows) expect(rowEl.className).not.toContain('pl-6')
    expect(menu.querySelectorAll('button[title="Unarchive"]')).toHaveLength(3)

    // No new-session buttons anywhere in flat mode — archived rows included.
    expect(menu.querySelectorAll('[role="menuitem"][title^="New session in this worktree"]')).toHaveLength(0)

    // The search filter reaches the flat archived rows (header count and rows
    // can never disagree).
    const input = menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!
    await typeInto(input, 'Archived feat')
    const narrowed = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(narrowed).not.toBeNull()
    expect(menuText(narrowed)).toContain('Archived (2)')
    expect(menuText(narrowed)).not.toContain('Archived local one')
  })

  it('toggling an archived branch collapses/expands its members and keeps the menu open', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)
    await click(archivedHeader(menu))

    await click(archivedBranchToggle(menu, 'feat-a'))
    expect(archivedBranchToggle(menu, 'feat-a').getAttribute('aria-expanded')).toBe('false')
    expect(menuText(menu)).not.toContain('Archived feat newer')
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()

    await click(archivedBranchToggle(menu, 'feat-a'))
    expect(archivedBranchToggle(menu, 'feat-a').getAttribute('aria-expanded')).toBe('true')
    expect(menuText(menu)).toContain('Archived feat newer')
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
  })

  it('archived and live branch groups of the SAME branch toggle independently', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)
    await click(archivedHeader(menu))

    // Collapse only the ARCHIVED feat-a group...
    await click(archivedBranchToggle(menu, 'feat-a'))
    expect(menuText(menu)).not.toContain('Archived feat newer')

    // ...the LIVE feat-a group of the same branch is untouched.
    expect(menuText(menu)).toContain('Feat A one')
    expect(branchToggle(menu, 'feat-a').getAttribute('aria-expanded')).toBe('true')
  })

  it('search filters both archived buckets, force-expands the section and updates the count; empty groups disappear', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    let menu = await openMenu(container)

    // A match inside the collapsed section: Archived force-expands, the
    // matching branch row + member show, the count follows. (Searching swaps
    // the menu DOM — re-query menu and input after every keystroke.)
    await typeInto(menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!, 'Archived feat newer')
    menu = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(archivedHeader(menu).textContent).toContain('Archived (1)')
    expect(archivedBranchToggle(menu, 'feat-a').getAttribute('aria-expanded')).toBe('true')
    expect(menuText(menu)).toContain('Archived feat newer')
    expect(menuText(menu)).not.toContain('Archived local one')

    // A local-bucket match only: the feat-a group loses every member and
    // disappears entirely.
    await typeInto(menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!, 'Archived local')
    menu = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(archivedHeader(menu).textContent).toContain('Archived (1)')
    expect(menu.querySelectorAll('[role="menuitem"][title$="archived branch sessions"]')).toHaveLength(0)
    expect(menuText(menu)).toContain('Archived local one')

    // A live-only match: nothing archived matches — the section hides whole.
    await typeInto(menu.querySelector<HTMLInputElement>('input[placeholder*="Search"]')!, 'Checkout')
    menu = document.body.querySelector('[role="menu"]') as HTMLElement
    const archivedHeaders = Array.from(menu.querySelectorAll('[role="menuitem"]')).filter((item) =>
      /^Archived \(\d+\)$/.test((item.textContent ?? '').trim()),
    )
    expect(archivedHeaders).toHaveLength(0)
    expect(menuText(menu)).not.toContain('Archived feat')
    expect(menuText(menu)).not.toContain('Archived local one')
  })

  it('keyboard: the archived branch row is a registered item — Enter toggles its group without closing the menu', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = archivedFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)
    await click(archivedHeader(menu))

    // Walk the roving collection down to the archived feat-a row (DOM order:
    // grouping toggle → Worktrees header → live feat-a row + its 4 actions +
    // its member row → Local header → local row → Archived header → archived
    // feat-a row).
    await pressKey(menu, 'ArrowDown')
    for (let i = 0; i < 11; i++) {
      await pressKey(document.activeElement!, 'ArrowDown')
    }
    expect(document.activeElement).toBe(archivedBranchToggle(menu, 'feat-a'))

    await pressKey(document.activeElement!, 'Enter')
    expect(menuText(menu)).not.toContain('Archived feat newer')
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()

    // A collapse re-render may hand focus back to the menu content — dispatch
    // the second Enter on the toggle itself: it re-expands.
    await pressKey(archivedBranchToggle(menu, 'feat-a'), 'Enter')
    expect(archivedBranchToggle(menu, 'feat-a').getAttribute('aria-expanded')).toBe('true')
    expect(menuText(menu)).toContain('Archived feat newer')
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
  })
})

describe('SessionSelector — branch new-session action', () => {
  /** Two managed branches (feat-a newest) + a local row + an archived row. */
  const newSessionFixture = (): SessionRow[] => [
    makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a'), last_active_at: new Date(NOW - 10_000).toISOString() }),
    makeSession({ id: 'm2', name: 'Feat B one', workspace_binding: managedBinding('feat-b') }),
    makeSession({ id: 'l1', name: 'Checkout work' }),
    makeSession({ id: 'a1', name: 'Old archived', archived: true }),
  ]

  /** The + item of one managed branch row — found by its full title. */
  function newSessionAction(menu: HTMLElement, branch: string): HTMLElement {
    const btn = menu.querySelector<HTMLElement>(
      `[role="menuitem"][title="New session in this worktree (${branch})"]`,
    )
    expect(btn, `new-session button for "${branch}"`).toBeTruthy()
    return btn as HTMLElement
  }

  beforeEach(() => {
    // The real stores behind the branch-button gesture (eager creation +
    // the fresh window's pending workspace choice).
    useProjectStore.setState({ activeProjectId: 'proj-1', projects: [makeProject()] })
    useSessionWorkspaceStore.setState({ choices: {}, consumingFor: null })
  })

  it('offers the button on every managed branch row — and nowhere else', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = newSessionFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    const buttons = Array.from(
      menu.querySelectorAll<HTMLElement>('[role="menuitem"][title^="New session in this worktree"]'),
    )
    // Exactly one per managed branch — the archived row, the local row and
    // every other row have none.
    expect(buttons.map((b) => b.title)).toEqual([
      'New session in this worktree (feat-a)',
      'New session in this worktree (feat-b)',
    ])
  })

  it('flat mode has no new-session buttons', async () => {
    sessionMocks.sessions = newSessionFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    expect(
      menu.querySelectorAll('[role="menuitem"][title^="New session in this worktree"]'),
    ).toHaveLength(0)
  })

  it('clicking eagerly creates the session, presets its branch choice and closes the menu', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = newSessionFixture()
    sessionMocks.activeSessionId = 'm1'
    apiMocks.createSession.mockResolvedValueOnce({ id: 's-new', project_id: 'proj-1' })
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    await click(newSessionAction(menu, 'feat-a'))

    // The menu closed...
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
    // ...the row was born immediately (ADR-091 eager creation — no draft
    // state, the session exists from the gesture onward), ...
    expect(apiMocks.createSession).toHaveBeenCalledOnce()
    expect(sessionMocks.addSession).toHaveBeenCalledWith(expect.objectContaining({ id: 's-new' }))
    // ...and the fresh window's pending choice pins the branch
    // (createBranch=false — the branch exists, the first send adopts its
    // managed tree per ADR-082).
    expect(useSessionWorkspaceStore.getState().choices['s-new']).toEqual({
      kind: 'branch',
      branch: 'feat-a',
      createBranch: false,
      startPoint: '',
    })
    // The new session became the visible active one.
    expect(sessionMocks.selectSession).toHaveBeenCalledWith('s-new', 'proj-1')
  })

  it('clicking never toggles the collapse state — even a collapsed branch arms its draft', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = newSessionFixture()
    const container = await render(<SessionSelector />)
    let menu = await openMenu(container)

    // Collapse feat-a first: its member disappears.
    await click(branchToggle(menu, 'feat-a'))
    menu = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(menuText(menu)).not.toContain('Feat A one')

    // The + button stays reachable on the collapsed row and records the
    // choice for the eagerly created session...
    apiMocks.createSession.mockResolvedValueOnce({ id: 's-new', project_id: 'proj-1' })
    await click(newSessionAction(menu, 'feat-a'))
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
    expect(useSessionWorkspaceStore.getState().choices['s-new']).toEqual({
      kind: 'branch',
      branch: 'feat-a',
      createBranch: false,
      startPoint: '',
    })

    // ...without touching the ephemeral collapse state.
    menu = await openMenu(container)
    expect(menuText(menu)).not.toContain('Feat A one')
  })
})

describe('SessionSelector — keyboard reachability of the new controls', () => {
  // 5 total sessions (the search input renders) with one managed branch, two
  // local rows and an archived row: the grouping toggle, the Worktrees header,
  // the branch toggle and its "+", the Local/Archived headers and the footer
  // action all render as registered items.
  const kbFixture = (): SessionRow[] => [
    makeSession({ id: 'm1', name: 'Feat A one', workspace_binding: managedBinding('feat-a') }),
    makeSession({ id: 'm2', name: 'Feat A two', workspace_binding: managedBinding('feat-a') }),
    makeSession({ id: 'l1', name: 'Checkout one' }),
    makeSession({ id: 'l2', name: 'Checkout two' }),
    makeSession({ id: 'a1', name: 'Old work', archived: true }),
  ]

  /** Section header item by its exact label. */
  function headerItem(m: HTMLElement, label: string): HTMLElement {
    const el = Array.from(m.querySelectorAll('[role="menuitem"]')).find(
      (item) => (item.textContent ?? '').trim() === label,
    )
    expect(el, `section header "${label}"`).toBeTruthy()
    return el as HTMLElement
  }

  it('ArrowDown enters the collection at the grouping toggle and walks DOM order — toggle → Worktrees header', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = kbFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // Pointer-open leaves focus on the menu content itself; the first
    // ArrowDown enters the roving collection at its FIRST item — the
    // grouping toggle (the search input is not an item and is skipped).
    await pressKey(menu, 'ArrowDown')
    // Grouped mode is on, so the toggle advertises the flat switch.
    const toggle = menu.querySelector<HTMLElement>('[role="menuitem"][title="Show flat list"]')!
    expect(document.activeElement).toBe(toggle)

    // Next in DOM order: the Worktrees section header — previously a plain
    // button the arrows skipped entirely (Tab is prevented inside the menu).
    await pressKey(toggle, 'ArrowDown')
    expect(document.activeElement).toBe(headerItem(menu, 'Worktrees'))
  })

  it('Enter on the grouping toggle flips the mode without closing the menu', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = kbFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    await pressKey(menu, 'ArrowDown')
    await pressKey(document.activeElement!, 'Enter')

    // The menu is still open and the display mode flipped to flat.
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    expect(useUIStore.getState().sessionListGrouped).toBe(false)
    expect(menuText(menu)).not.toContain('feat-a')
    expect(menuText(menu)).toContain('Checkout one')
  })

  it('Enter on the Worktrees header collapses and re-expands the section — the menu stays open', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = kbFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // toggle → Worktrees header, then Enter to collapse.
    await pressKey(menu, 'ArrowDown')
    await pressKey(document.activeElement!, 'ArrowDown')
    expect(document.activeElement).toBe(headerItem(menu, 'Worktrees'))
    await pressKey(document.activeElement!, 'Enter')

    expect(headerItem(menu, 'Worktrees').getAttribute('aria-expanded')).toBe('false')
    expect(menuText(menu)).not.toContain('Feat A one')
    // The Local section is untouched and the menu is still open.
    expect(menuText(menu)).toContain('Checkout one')
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()

    // Enter again — the section re-expands (focus persisted through the
    // header's in-place re-render).
    await pressKey(document.activeElement!, 'Enter')
    expect(menuText(menu)).toContain('Feat A one')
  })

  it('a branch group collapsed with the mouse is reachable and re-expandable from the keyboard', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = kbFixture()
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // Mouse: collapse feat-a.
    await click(branchToggle(menu, 'feat-a'))
    expect(menuText(menu)).not.toContain('Feat A one')

    // Keyboard: toggle → Worktrees header → feat-a toggle (the collapsed row
    // keeps its registered toggle and "+"), then Enter re-expands.
    await pressKey(menu, 'ArrowDown')
    await pressKey(document.activeElement!, 'ArrowDown')
    await pressKey(document.activeElement!, 'ArrowDown')
    expect(document.activeElement).toBe(branchToggle(menu, 'feat-a'))
    await pressKey(document.activeElement!, 'Enter')

    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    expect(menuText(menu)).toContain('Feat A one')
    expect(menuText(menu)).toContain('Feat A two')
  })

  it('Enter on a branch "+" eagerly creates the session with its branch choice', async () => {
    // The real stores behind the branch-button gesture.
    useProjectStore.setState({ activeProjectId: 'proj-1', projects: [makeProject()] })
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = kbFixture()
    apiMocks.createSession.mockResolvedValueOnce({ id: 's-new', project_id: 'proj-1' })
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // toggle → Worktrees header → feat-a toggle → feat-a "+".
    await pressKey(menu, 'ArrowDown')
    for (let i = 0; i < 3; i++) {
      await pressKey(document.activeElement!, 'ArrowDown')
    }
    expect(document.activeElement?.getAttribute('title')).toBe('New session in this worktree (feat-a)')
    await pressKey(document.activeElement!, 'Enter')

    // The "+" select is NOT prevented — Radix's default close fires (and the
    // onOpenChange path clears the search).
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
    expect(useSessionWorkspaceStore.getState().choices['s-new']).toEqual({
      kind: 'branch',
      branch: 'feat-a',
      createBranch: false,
      startPoint: '',
    })
    // The active selection was cleared (draft and selection are mutually
    // exclusive — same contract as the click path).
    expect(sessionMocks.selectSession).toHaveBeenCalledWith('s-new', 'proj-1')
  })
})

describe('SessionSelector — grouped order with an active session', () => {
  const iso = (minutesAgo: number) => new Date(NOW - minutesAgo * 60_000).toISOString()

  /**
   * Document order of the given labels across the menu's `[role="menuitem"]`
   * rows. A label matching a row's full text exactly (section headers) binds
   * there; otherwise it falls back to a substring match (session rows carry
   * extra text), first match wins. Missing labels are dropped from the
   * result — flat-mode assertions use that to prove branch rows are absent.
   */
  function itemOrder(menu: HTMLElement, labels: string[]): string[] {
    const texts = Array.from(menu.querySelectorAll('[role="menuitem"]')).map((el) =>
      (el.textContent ?? '').trim(),
    )
    return labels
      .map((label) => {
        const exact = texts.findIndex((t) => t === label)
        return { label, idx: exact >= 0 ? exact : texts.findIndex((t) => t.includes(label)) }
      })
      .filter((hit) => hit.idx >= 0)
      .sort((a, b) => a.idx - b.idx)
      .map((hit) => hit.label)
  }

  /**
   * Store order deliberately contradicts every grouped expectation: the
   * freshest session sits in the group that must render LAST (fresh-b), the
   * active session's group (act-b) holds a fresher sibling, and pin-b is the
   * stalest branch — kept afloat only by its GROUP pin.
   */
  const activeBranchFixture = (): SessionRow[] => [
    makeSession({ id: 'fresh1', name: 'Fresh one', workspace_binding: managedBinding('fresh-b'), last_active_at: iso(1) }),
    makeSession({ id: 'act', name: 'Active now', workspace_binding: managedBinding('act-b'), last_active_at: iso(30) }),
    makeSession({ id: 'act2', name: 'Act two', workspace_binding: managedBinding('act-b'), last_active_at: iso(2) }),
    makeSession({ id: 'p1', name: 'Pinned one', workspace_binding: managedBinding('pin-b'), last_active_at: iso(45) }),
  ]

  it('the active branch group floats above a fresher unpinned group but below a pinned group', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    usePinnedGroupsStore.setState({ pinsByProject: { 'proj-1': ['pin-b'] } })
    sessionMocks.sessions = activeBranchFixture()
    sessionMocks.activeSessionId = 'act'
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // GROUP-pin tier first; then contains-active beats freshness (act-b holds
    // the 30-min-old active session, fresh-b the freshest one overall).
    expect(itemOrder(menu, ['pin-b', 'act-b', 'fresh-b'])).toEqual(['pin-b', 'act-b', 'fresh-b'])
  })

  it('the active session renders first inside its branch group — ahead of a fresher sibling', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = activeBranchFixture()
    sessionMocks.activeSessionId = 'act'
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // Unpinned groups: contains-active first, then freshness. Inside act-b the
    // STALE active row leads its 2-min-old sibling; the other groups lead with
    // their freshest member.
    expect(
      itemOrder(menu, ['act-b', 'Active now', 'Act two', 'fresh-b', 'Fresh one', 'pin-b', 'Pinned one']),
    ).toEqual(['act-b', 'Active now', 'Act two', 'fresh-b', 'Fresh one', 'pin-b', 'Pinned one'])
  })

  it('an active local row leads Local while the Worktrees section still renders above it', async () => {
    useUIStore.setState({ sessionListGrouped: true })
    sessionMocks.sessions = [
      makeSession({ id: 'm1', name: 'Managed one', workspace_binding: managedBinding('feat-x'), last_active_at: iso(60) }),
      makeSession({ id: 'l1', name: 'Local fresher', last_active_at: iso(1) }),
      makeSession({ id: 'lact', name: 'Local active', last_active_at: iso(180) }),
    ]
    sessionMocks.activeSessionId = 'lact'
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // The 3-hour-old active local beats its 1-min-old sibling INSIDE Local,
    // but the section structure is untouched: Worktrees (branch row + member)
    // still precedes the Local header.
    expect(
      itemOrder(menu, ['Worktrees', 'feat-x', 'Managed one', 'Local', 'Local active', 'Local fresher']),
    ).toEqual(['Worktrees', 'feat-x', 'Managed one', 'Local', 'Local active', 'Local fresher'])
  })

  it('flat mode ignores the active session — pure store order, no branch chrome', async () => {
    // sessionListGrouped stays at the beforeEach default (false).
    sessionMocks.sessions = activeBranchFixture()
    sessionMocks.activeSessionId = 'act'
    const container = await render(<SessionSelector />)
    const menu = await openMenu(container)

    // The DOM follows the store array verbatim — the active row stays exactly
    // where the store put it (2nd), and no branch rows exist to match.
    expect(itemOrder(menu, ['Fresh one', 'Active now', 'Act two', 'Pinned one'])).toEqual([
      'Fresh one',
      'Active now',
      'Act two',
      'Pinned one',
    ])
    expect(itemOrder(menu, ['pin-b', 'act-b', 'fresh-b'])).toEqual([])
  })
})
