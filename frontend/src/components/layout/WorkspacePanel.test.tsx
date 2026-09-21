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

import { WorkspacePanel } from './WorkspacePanel'
import { useProjectStore } from '@/stores/projectStore'
import { useGitPanelStore, selectGitPanelTab } from '@/stores/gitPanelStore'
import { useUIStore, selectWorkspaceTab } from '@/stores/uiStore'
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
  useProjectStore.setState({ projects: [P1, NO_PROJECT], activeProjectId: 'p1' })
  useGitPanelStore.getState().reset()
  useUIStore.setState({ workspaceTabByProject: {} })
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

    expect(tabCount()).toBe(3) // Explorer | Search | Research — no Git
    expect(has('file-tree')).toBe(true)
    expect(has('git-panel')).toBe(false)
  })

  it('git project: renders the Git tab strip without an Explorer trigger, mounts GitPanel', async () => {
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })

    renderPanel()
    await flush()

    expect(tabCount()).toBe(3) // Git | Search | Research — no Explorer
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
