// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Heavy workspace sections are replaced with sentinels — this suite tests
// the WorkspacePanel's own layout logic (which tab strip renders, which
// content mounts, tab normalization), not the sections' internals. The
// eager git-repo detection hook is inert here; tests drive gitPanelStore
// directly to simulate a completed check.
vi.mock('./FileTreePanel', () => ({
  FileTreePanel: () => createElement('div', { 'data-testid': 'file-tree' }, 'FILE_TREE'),
}))
vi.mock('@/components/GitPanel', () => ({
  GitPanel: () => createElement('div', { 'data-testid': 'git-panel' }, 'GIT_PANEL'),
}))
vi.mock('./VectorStorePanel', () => ({ VectorStorePanel: () => null }))
vi.mock('@/components/research', () => ({ ResearchPanel: () => null }))
vi.mock('@/hooks/useProjectGitRepo', () => ({ useProjectGitRepo: vi.fn() }))

// The Specs tab body fetches through @/api/specs — mocked here so the
// specsStore fetch effect resolves without a Wails runtime (the boundary
// validation itself is covered by api/specs.test.ts).
vi.mock('@/api/specs', () => ({
  getSpecsStatus: vi.fn(async () => ({
    present: true,
    root: '/ws/p1/specs',
    files: [
      { path: 'INDEX.md', title: 'Specs Index' },
      { path: 'domains/testing.md', section: 'domains', title: 'Testing' },
    ],
    index_tasks: [],
    adrs: [
      {
        number: '105',
        slug: 'specs-sidebar-panel',
        file: 'decisions/105-specs-sidebar-panel.md',
        status: 'accepted',
      },
    ],
    findings: [],
    counts: { files: 2, adrs: 1, findings: 0, findings_by_kind: {} },
  })),
}))

// Indexing master switch double — the gate is driven directly per test; the
// real hook (and its GetConfig hydration/retry lifecycle) is covered by
// useIndexingSettings.test.ts.
const indexingGate = vi.hoisted(() => ({ enabled: true }))
vi.mock('@/hooks/useIndexingSettings', () => ({
  useIndexingSettings: () => indexingGate.enabled,
}))

import { WorkspacePanel } from './WorkspacePanel'
import { useProjectStore } from '@/stores/projectStore'
import { useGitPanelStore, selectGitPanelTab } from '@/stores/gitPanelStore'
import { useUIStore, selectWorkspaceTab } from '@/stores/uiStore'
import { useSpecsStore } from '@/stores/specsStore'
import type { ProjectInfo } from '@/types/models'

function makeProject(overrides: Partial<ProjectInfo> & { id: string }): ProjectInfo {
  return {
    name: overrides.id,
    workspace_path: `/ws/${overrides.id}`,
    is_external: false,
    is_no_project: false,
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

const P1 = makeProject({ id: 'p1' })
const P2 = makeProject({ id: 'p2' })
const NO_PROJECT = makeProject({ id: 'np', is_no_project: true })

let root: Root | null = null
let container: HTMLDivElement | null = null

function renderPanel(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(createElement(WorkspacePanel))
  })
}

/** Let the normalization effect's state writes settle. */
async function flush(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve()
  })
}

function tabCount(): number {
  return container?.querySelectorAll('[role="tab"]').length ?? -1
}

function has(testId: string): boolean {
  return container?.querySelector(`[data-testid="${testId}"]`) != null
}

beforeEach(() => {
  indexingGate.enabled = true
  useProjectStore.setState({ projects: [P1, NO_PROJECT], activeProjectId: 'p1' })
  useGitPanelStore.getState().reset()
  useUIStore.setState({ workspaceTabByProject: {} })
  useSpecsStore.getState().reset()
})

afterEach(() => {
  if (root) {
    const r = root
    root = null
    act(() => {
      r.unmount()
    })
  }
  container?.remove()
  container = null
})

describe('WorkspacePanel — layout by git-repo state', () => {
  it('non-git project: renders the Explorer tab strip without a Git trigger, mounts FileTreePanel', async () => {
    // No completed repo check for p1 — fail-closed to the non-git layout.
    renderPanel()
    await flush()

    expect(tabCount()).toBe(4) // Explorer | Search | Research | Specs — no Git
    expect(has('file-tree')).toBe(true)
    expect(has('git-panel')).toBe(false)
  })

  it('git project: renders the Git tab strip without an Explorer trigger, mounts GitPanel', async () => {
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })

    renderPanel()
    await flush()

    expect(tabCount()).toBe(4) // Git | Search | Research | Specs — no Explorer
    expect(has('git-panel')).toBe(true)
    expect(has('file-tree')).toBe(false)
  })

  it('a stale repo check for another project does not flip the layout', async () => {
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'other-project')
    })

    renderPanel()
    await flush()

    expect(has('file-tree')).toBe(true)
    expect(has('git-panel')).toBe(false)
  })

  it('No Project (CHAT) mode: no tab strip, only the file explorer', async () => {
    useProjectStore.setState({ activeProjectId: 'np' })

    renderPanel()
    await flush()

    expect(tabCount()).toBe(0)
    expect(container?.querySelector('[data-segment-value="specs"]')).toBeNull()
    expect(has('file-tree')).toBe(true)
  })
})

describe('WorkspacePanel — per-project workspaceTab (normalization + restore)', () => {
  it('a first visit to a git project lands on the git section with files', async () => {
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })
    // No remembered tab for p1 beyond the default 'explorer' — it must be
    // normalized to the git section (whose first internal section is 'files').
    useUIStore.setState({ workspaceTabByProject: { p1: 'explorer' } })

    renderPanel()
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('git')
    expect(selectGitPanelTab(useGitPanelStore.getState(), 'p1')).toBe('files')
  })

  it('non-git project remaps a lingering git tab to explorer once the check lands', async () => {
    useUIStore.setState({ workspaceTabByProject: { p1: 'git' } })
    // A completed non-repo check for p1 is required before normalization.
    act(() => {
      useGitPanelStore.getState().setGitRepo(false, 'p1')
    })

    renderPanel()
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('explorer')
  })

  it('does NOT reset a remembered git tab while the repo check is still pending', async () => {
    // p2 remembers 'git'; the store still reports the check for p1 (stale).
    useUIStore.setState({ workspaceTabByProject: { p2: 'git' } })
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })
    useProjectStore.setState({ projects: [P1, P2, NO_PROJECT], activeProjectId: 'p2' })

    renderPanel()
    await flush()

    // repoKnown is false for p2, so the normalization effect must not have
    // clobbered p2's remembered 'git' with 'explorer' during the pending check.
    expect(selectWorkspaceTab(useUIStore.getState(), 'p2')).toBe('git')
  })

  it('restores each git project its own outer tab (incl. semantics) on switch-back', async () => {
    useUIStore.setState({ workspaceTabByProject: { p1: 'semantics', p2: 'git' } })
    useProjectStore.setState({ projects: [P1, P2, NO_PROJECT], activeProjectId: 'p1' })
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })

    renderPanel()
    await flush()
    // p1's remembered 'semantics' is preserved (a valid git-project tab).
    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('semantics')

    // Switch to p2 — its own remembered 'git' shows, and p1 is untouched.
    act(() => {
      useProjectStore.setState({ activeProjectId: 'p2' })
      useGitPanelStore.getState().setGitRepo(true, 'p2')
    })
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p2')).toBe('git')
    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('semantics')
  })

  it('switching the active project from git to non-git falls back to explorer', async () => {
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })

    renderPanel()
    await flush()
    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('git')

    // Project switch: the new project's eager check reports non-repo.
    act(() => {
      useProjectStore.setState({ projects: [P1, P2, NO_PROJECT], activeProjectId: 'p2' })
      useGitPanelStore.getState().setGitRepo(false, 'p2')
    })
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p2')).toBe('explorer')
    expect(has('file-tree')).toBe(true)
    expect(has('git-panel')).toBe(false)
  })
})

describe('WorkspacePanel — ARIA tabs wiring', () => {
  /** The currently selected tab button. */
  function selectedTab(): HTMLElement {
    return container!.querySelector('[role="tab"][aria-selected="true"]')!
  }

  it('wires the active tab to its panel through id + aria-controls (explorer)', async () => {
    renderPanel()
    await flush()

    const tab = selectedTab()
    expect(tab.getAttribute('data-segment-value')).toBe('explorer')
    expect(tab.id).toBe('workspace-tab-explorer')
    expect(tab.getAttribute('aria-controls')).toBe('workspace-panel-explorer')

    const panel = container!.querySelector('[role="tabpanel"]')!
    expect(panel.id).toBe('workspace-panel-explorer')
    expect(panel.getAttribute('aria-labelledby')).toBe('workspace-tab-explorer')
  })

  it('re-points the association when the tab switches (semantics panel renders)', async () => {
    renderPanel()
    await flush()

    act(() => {
      useUIStore.getState().setWorkspaceTab('p1', 'semantics')
    })
    await flush()

    const tab = selectedTab()
    expect(tab.getAttribute('data-segment-value')).toBe('semantics')
    expect(tab.id).toBe('workspace-tab-semantics')

    const panel = container!.querySelector('[role="tabpanel"]')!
    expect(panel.id).toBe('workspace-panel-semantics')
    expect(panel.getAttribute('aria-labelledby')).toBe('workspace-tab-semantics')
  })

  it('wires the git tab to the git panel in a git project', async () => {
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })

    renderPanel()
    await flush()

    const tab = selectedTab()
    expect(tab.getAttribute('data-segment-value')).toBe('git')
    expect(tab.getAttribute('aria-controls')).toBe('workspace-panel-git')

    const panel = container!.querySelector('[role="tabpanel"]')!
    expect(panel.id).toBe('workspace-panel-git')
    expect(panel.getAttribute('aria-labelledby')).toBe('workspace-tab-git')
    expect(has('git-panel')).toBe(true)
  })

  it('No Project mode renders no tabs and no panels', async () => {
    useProjectStore.setState({ activeProjectId: 'np' })

    renderPanel()
    await flush()

    expect(container!.querySelectorAll('[role="tab"]')).toHaveLength(0)
    expect(container!.querySelectorAll('[role="tabpanel"]')).toHaveLength(0)
  })
})

describe('WorkspacePanel — indexing gate (Search tab)', () => {
  /** Whether a tab trigger with the given segment value renders. */
  function hasTab(value: string): boolean {
    return container?.querySelector(`[data-segment-value="${value}"]`) != null
  }

  it('renders the Search tab in both layouts while indexing is enabled', async () => {
    renderPanel()
    await flush()

    expect(hasTab('semantics')).toBe(true)
    expect(tabCount()).toBe(4) // Explorer | Search | Research | Specs

    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })
    await flush()

    expect(hasTab('semantics')).toBe(true)
    expect(tabCount()).toBe(4) // Git | Search | Research | Specs
  })

  it('hides the Search tab from the git layout while indexing is disabled', async () => {
    indexingGate.enabled = false
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })

    renderPanel()
    await flush()

    expect(hasTab('semantics')).toBe(false)
    expect(hasTab('specs')).toBe(true) // Specs is not gated by indexing
    expect(tabCount()).toBe(3) // Git | Research | Specs — no Search
  })

  it('hides the Search tab from the non-git layout while indexing is disabled', async () => {
    indexingGate.enabled = false

    renderPanel()
    await flush()

    expect(hasTab('semantics')).toBe(false)
    expect(hasTab('specs')).toBe(true)
    expect(tabCount()).toBe(3) // Explorer | Research | Specs — no Search
  })

  it('remaps a remembered semantics tab to git (and persists it) when indexing is off', async () => {
    indexingGate.enabled = false
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })
    useUIStore.setState({ workspaceTabByProject: { p1: 'semantics' } })

    renderPanel()
    await flush()

    // The store is corrected to the layout's primary tab...
    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('git')
    // ...and the git panel (not the hidden semantics panel) mounts.
    expect(has('git-panel')).toBe(true)
    expect(container!.querySelector('[role="tabpanel"]')!.id).toBe('workspace-panel-git')
  })

  it('remaps a remembered semantics tab to explorer when indexing is off (non-git)', async () => {
    indexingGate.enabled = false
    act(() => {
      useGitPanelStore.getState().setGitRepo(false, 'p1')
    })
    useUIStore.setState({ workspaceTabByProject: { p1: 'semantics' } })

    renderPanel()
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('explorer')
    expect(has('file-tree')).toBe(true)
    expect(container!.querySelector('[role="tabpanel"]')!.id).toBe('workspace-panel-explorer')
  })

  it('inline-remaps semantics while the repo check is pending (store untouched until it lands)', async () => {
    indexingGate.enabled = false
    useUIStore.setState({ workspaceTabByProject: { p1: 'semantics' } })
    // No completed repo check for p1 — repoKnown is false, so the
    // normalization effect must not clobber the remembered tab yet.

    renderPanel()
    await flush()

    // Fail-closed non-git layout: the explorer renders this frame via the
    // INLINE remap, while the store still holds the remembered semantics.
    expect(has('file-tree')).toBe(true)
    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('semantics')

    // Once the check lands (a repo), the effect persists the git fallback.
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('git')
    expect(has('git-panel')).toBe(true)
  })

  it('keeps a remembered semantics tab while indexing is enabled', async () => {
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })
    useUIStore.setState({ workspaceTabByProject: { p1: 'semantics' } })

    renderPanel()
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('semantics')
    expect(container!.querySelector('[role="tabpanel"]')!.id).toBe('workspace-panel-semantics')
  })
})

describe('WorkspacePanel — Specs tab', () => {
  /** The rendered tab trigger values, in strip order. */
  function tabValues(): string[] {
    return Array.from(container!.querySelectorAll('[data-segment-value]')).map(
      (el) => el.getAttribute('data-segment-value')!,
    )
  }

  it('renders the Specs trigger after Research in both layouts', async () => {
    renderPanel()
    await flush()

    expect(tabValues()).toEqual(['explorer', 'semantics', 'research', 'specs'])

    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })
    await flush()

    expect(tabValues()).toEqual(['git', 'semantics', 'research', 'specs'])
  })

  it('a remembered specs tab stays put (no layout normalization touches it)', async () => {
    useUIStore.setState({ workspaceTabByProject: { p1: 'specs' } })

    renderPanel()
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('specs')
    const panel = container!.querySelector('[role="tabpanel"]')!
    expect(panel.id).toBe('workspace-panel-specs')
    expect(panel.getAttribute('aria-labelledby')).toBe('workspace-tab-specs')
  })

  it('mounts the specs body: fetch -> store -> rendered counts', async () => {
    useUIStore.setState({ workspaceTabByProject: { p1: 'specs' } })

    renderPanel()
    await flush()

    // The mocked @/api/specs payload flowed through specsStore into the DOM.
    const s = useSpecsStore.getState()
    expect(s.projectId).toBe('p1')
    expect(s.status?.present).toBe(true)
    expect(s.error).toBeNull()
    expect(container!.textContent).toContain('2 spec files')
    expect(container!.textContent).toContain('1 ADRs')
  })

  it('does not fetch while another tab is active', async () => {
    renderPanel()
    await flush()

    expect(useSpecsStore.getState().status).toBeNull()
    expect(useSpecsStore.getState().isLoading).toBe(false)
  })
})
