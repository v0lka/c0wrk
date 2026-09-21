// @vitest-environment jsdom
//
// Invariant under test: a project-select click is NEVER short-circuited
// locally — even when the clicked project is already the active one. The RPC
// must always be dispatched so the backend's idempotent switch path can
// re-emit `project:switched` and repair a frontend↔backend desync without an
// app restart (see ProjectSelector.handleSwitch and
// useProjectSwitchState.performSwitch's reconcile path).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import type { ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ProjectSelector } from './ProjectSelector'
import { useProjectStore } from '@/stores/projectStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useUIStore } from '@/stores/uiStore'
import type { ProjectInfo } from '@/types/models'

const { switchProjectWithStateMock, deleteProjectMock, renameProjectMock, dropProjectSnapshotMock } = vi.hoisted(() => ({
  switchProjectWithStateMock: vi.fn<(id: string) => Promise<void>>(),
  deleteProjectMock: vi.fn<(id: string) => Promise<void>>(),
  renameProjectMock: vi.fn<(id: string, name: string) => Promise<void>>(),
  dropProjectSnapshotMock: vi.fn<(id: string) => void>(),
}))

vi.mock('@/hooks/useProjectSwitchState', () => ({
  useProjectSwitchState: () => switchProjectWithStateMock,
}))

// The snapshot cache is a module-level singleton; mock it so the delete path
// can be asserted without touching real store state.
vi.mock('@/lib/projectSnapshotCache', () => ({
  drop: dropProjectSnapshotMock,
}))

vi.mock('@/api/projects', () => ({
  renameProject: renameProjectMock,
  deleteProject: deleteProjectMock,
}))

// The real logger writes expected error noise to the console for the failure
// branches; the component under test does not assert on logging.
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

// Keep the create dialog (and its own API imports) out of this render.
vi.mock('@/components/project/CreateProjectDialog', () => ({
  CreateProjectDialog: () => null,
}))

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
    children: ReactNode
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
  ItemActions: ({ children }: { children: ReactNode }) => <span>{children}</span>,
}))

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

function makeProject(id: string, name: string): ProjectInfo {
  return {
    id,
    name,
    workspace_path: `/tmp/${id}`,
    is_external: false,
    is_no_project: false,
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
  }
}

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

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function openMenu(container: HTMLElement): Promise<HTMLElement> {
  const trigger = container.querySelector<HTMLButtonElement>('button')!
  await act(async () => {
    trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
  const menu = document.body.querySelector('[role="menu"]')
  expect(menu).not.toBeNull()
  return menu as HTMLElement
}

function menuItems(menu: HTMLElement): HTMLElement[] {
  return Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))
}

async function selectItem(item: HTMLElement): Promise<void> {
  await act(async () => {
    item.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    item.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  await flush()
}

beforeEach(() => {
  vi.clearAllMocks()
  switchProjectWithStateMock.mockResolvedValue(undefined)
  useProjectStore.setState({
    projects: [makeProject('p1', 'Alpha'), makeProject('p2', 'Beta')],
    activeProjectId: 'p1',
    lastRealProjectId: 'p1',
  })
  // Clear the per-project tab maps so each test starts from a known state.
  useGitPanelStore.setState({ activeTabByProject: {} })
  useUIStore.setState({ workspaceTabByProject: {} })
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('ProjectSelector — switch invariant', () => {
  it('clicking the already-active project still calls switchProjectWithState', async () => {
    const container = await render(<ProjectSelector />)
    const menu = await openMenu(container)
    const active = menuItems(menu).find((i) => i.textContent?.includes('Alpha'))!

    await selectItem(active)

    // The active id is NOT filtered out locally — the RPC is always dispatched
    // so the backend can re-emit project:switched and repair desync.
    expect(switchProjectWithStateMock).toHaveBeenCalledTimes(1)
    expect(switchProjectWithStateMock).toHaveBeenCalledWith('p1')
  })

  it('clicking a different project calls switchProjectWithState with that id', async () => {
    const container = await render(<ProjectSelector />)
    const menu = await openMenu(container)
    const other = menuItems(menu).find((i) => i.textContent?.includes('Beta'))!

    await selectItem(other)

    expect(switchProjectWithStateMock).toHaveBeenCalledTimes(1)
    expect(switchProjectWithStateMock).toHaveBeenCalledWith('p2')
  })
})

describe('ProjectSelector — snapshot invalidation', () => {
  it('deleting a project drops its cached snapshot', async () => {
    const container = await render(<ProjectSelector />)
    const menu = await openMenu(container)
    const alphaRow = menuItems(menu).find((i) => i.textContent?.includes('Alpha'))!
    const deleteButton = alphaRow.querySelector<HTMLButtonElement>('button[aria-label="Delete"]')!

    await selectItem(deleteButton)

    expect(deleteProjectMock).toHaveBeenCalledWith('p1')
    // The deleted project's in-memory snapshot must be dropped so a later
    // switch can never rehydrate a tree/session list for a gone project.
    expect(dropProjectSnapshotMock).toHaveBeenCalledWith('p1')
  })
})

describe('ProjectSelector — per-project tab cleanup', () => {
  it('deleting a project drops its entries from both maps, leaving other projects intact', async () => {
    // Seed both per-project maps for two projects.
    useGitPanelStore.getState().setActiveTab('p1', 'changes')
    useGitPanelStore.getState().setActiveTab('p2', 'history')
    useUIStore.getState().setWorkspaceTab('p1', 'git')
    useUIStore.getState().setWorkspaceTab('p2', 'semantics')

    const container = await render(<ProjectSelector />)
    const menu = await openMenu(container)
    const alphaRow = menuItems(menu).find((i) => i.textContent?.includes('Alpha'))!
    const deleteButton = alphaRow.querySelector<HTMLButtonElement>('button[aria-label="Delete"]')!

    await selectItem(deleteButton)

    expect(deleteProjectMock).toHaveBeenCalledWith('p1')
    // The deleted project's tab entries are gone from both persisted maps...
    expect(useGitPanelStore.getState().activeTabByProject['p1']).toBeUndefined()
    expect(useUIStore.getState().workspaceTabByProject['p1']).toBeUndefined()
    // ...while every other project's entries are untouched.
    expect(useGitPanelStore.getState().activeTabByProject['p2']).toBe('history')
    expect(useUIStore.getState().workspaceTabByProject['p2']).toBe('semantics')
  })
})
