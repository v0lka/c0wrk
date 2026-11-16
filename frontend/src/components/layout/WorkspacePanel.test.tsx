// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { SPECS_DETECTION_REPROBE_DEBOUNCE_MS } from './WorkspacePanel'
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

// The Specs status RPC is mocked at the api boundary: the tab strip's
// visibility answer (gate && detection) and the panel body both come from the
// SAME single call, so one controllable payload builder serves this suite
// (boundary validation itself is covered by api/specs.test.ts).
const specsDetection = vi.hoisted(() => ({ detected: ['sdd'] as string[] }))

/** A framework-aware payload with `detected` frameworks; "" disables the
 *  detected list (zero frameworks → no Specs surface). */
function specsPayload(detected: string[]): Record<string, unknown> {
  const frameworks = detected.map((id) => ({
    id,
    label: id === 'sdd' ? 'Specification System' : id,
    origin: 'builtin',
    scope: 'project',
    tree_present: true,
    root_present: true,
    marker_present: true,
    skills: [],
  }))
  return {
    present: frameworks.length > 0,
    tree_present: frameworks.length > 0,
    root: frameworks.length > 0 ? '/ws/p1/specs' : '',
    workspace: '/ws/p1',
    detected: frameworks,
    active_id: frameworks[0]?.id ?? '',
    active: frameworks[0],
    selector_needed: false,
    roots: [{ id: 'specs', path: 'specs', required: true, present: true }],
    documents: [
      {
        path: 'specs/INDEX.md', root: 'specs', concept: 'index', name: 'INDEX.md',
        slug: 'INDEX', title: 'Specs Index', headings: [], key_files: [], cross_refs: [],
        requirements: [], scenarios: [], deltas: [], principles: [], user_stories: [],
        task_items: [], tasks: [], missing: [],
      },
      {
        path: 'specs/domains/testing.md', root: 'specs', concept: 'domain', name: 'testing.md',
        slug: 'testing', title: 'Testing', headings: [], key_files: [], cross_refs: [],
        requirements: [], scenarios: [], deltas: [], principles: [], user_stories: [],
        task_items: [], tasks: [], missing: [],
      },
    ],
    nodes: [
      {
        id: 'specs/INDEX.md', kind: 'document', path: 'specs/INDEX.md', label: 'Specs Index',
        concept: 'index', name: 'INDEX.md', children: [],
      },
      {
        id: 'specs/domains/testing.md', kind: 'document', path: 'specs/domains/testing.md',
        label: 'Testing', concept: 'domain', name: 'testing.md', children: [],
      },
    ],
    views: [
      { id: 'tree', label: 'Specification tree', kind: 'tree', nodes: ['specs/INDEX.md', 'specs/domains/testing.md'] },
    ],
    findings: [],
    actions: [
      { kind: 'check', label: 'Check', skill: 'spec-check', prompt: 'Check.', requires: ['root'], available: true },
    ],
    counts: {
      documents: 2,
      findings: 0,
      by_concept: { index: 1, domain: 1 },
      findings_by_kind: {},
    },
    warnings: [],
  }
}

vi.mock('@/api/specs', () => ({
  getSpecsStatus: vi.fn(async () => specsPayload(specsDetection.detected)),
  setActiveSDD: vi.fn(async () => {}),
}))

// Indexing master switch double — the gate is driven directly per test; the
// real hook (and its GetConfig hydration/retry lifecycle) is covered by
// useIndexingSettings.test.ts.
const indexingGate = vi.hoisted(() => ({ enabled: true }))
vi.mock('@/hooks/useIndexingSettings', () => ({
  useIndexingSettings: () => indexingGate.enabled,
}))

// Sidebar surface gates double — same contract as the indexing double: the
// real hook's GetConfig hydration is covered by useSidebarPacks.test.ts.
const sidebarGate = vi.hoisted(() => ({ research: true, specs: true }))
vi.mock('@/hooks/useSidebarPacks', () => ({
  useSidebarPacks: () => ({ research: sidebarGate.research, specs: sidebarGate.specs }),
}))

import { WorkspacePanel } from './WorkspacePanel'
import { emitLocalEvent } from '@/api/runtime'
import { getSpecsStatus } from '@/api/specs'
import { useProjectStore } from '@/stores/projectStore'
import { useGitPanelStore, selectGitPanelTab } from '@/stores/gitPanelStore'
import { useUIStore, selectWorkspaceTab } from '@/stores/uiStore'
import { useSpecsStore } from '@/stores/specsStore'
import { useSddStore } from '@/stores/sddStore'
import type { ProjectInfo, SpecsStatus } from '@/types/models'

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
  sidebarGate.research = true
  sidebarGate.specs = true
  specsDetection.detected = ['sdd']
  useProjectStore.setState({ projects: [P1, NO_PROJECT], activeProjectId: 'p1' })
  useGitPanelStore.getState().reset()
  useUIStore.setState({ workspaceTabByProject: {} })
  useSpecsStore.getState().reset()
})

afterEach(() => {
  vi.useRealTimers()
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

describe('WorkspacePanel — sidebar surface gates (Research / Specs tabs)', () => {
  /** Whether a tab trigger with the given segment value renders. */
  function hasTab(value: string): boolean {
    return container?.querySelector(`[data-segment-value="${value}"]`) != null
  }

  it('hides the Research tab while its surface gate is off, keeping Specs', async () => {
    sidebarGate.research = false

    renderPanel()
    await flush()

    expect(hasTab('research')).toBe(false)
    expect(hasTab('specs')).toBe(true)
    expect(tabCount()).toBe(3) // Explorer | Search | Specs — no Research
  })

  it('hides the Specs tab while its surface gate is off, keeping Research', async () => {
    sidebarGate.specs = false

    renderPanel()
    await flush()

    expect(hasTab('specs')).toBe(false)
    expect(hasTab('research')).toBe(true)
    expect(tabCount()).toBe(3) // Explorer | Search | Research — no Specs
  })

  it('hides both gated tabs while both gates are off', async () => {
    sidebarGate.research = false
    sidebarGate.specs = false

    renderPanel()
    await flush()

    expect(hasTab('research')).toBe(false)
    expect(hasTab('specs')).toBe(false)
    expect(tabCount()).toBe(2) // Explorer | Search
  })

  it('remaps a remembered research tab to explorer (and persists it) when the gate is off', async () => {
    sidebarGate.research = false
    // A completed non-repo check for p1 — the normalization effect only
    // runs once the repo check has landed (the semantics-gate contract).
    act(() => {
      useGitPanelStore.getState().setGitRepo(false, 'p1')
    })
    useUIStore.setState({ workspaceTabByProject: { p1: 'research' } })

    renderPanel()
    await flush()

    // The store is corrected to the layout's primary tab...
    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('explorer')
    // ...and the explorer (not the hidden research panel) mounts.
    expect(has('file-tree')).toBe(true)
    expect(container!.querySelector('[role="tabpanel"]')!.id).toBe('workspace-panel-explorer')
  })

  it('remaps a remembered specs tab to git in a repo when the gate is off', async () => {
    sidebarGate.specs = false
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })
    useUIStore.setState({ workspaceTabByProject: { p1: 'specs' } })

    renderPanel()
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('git')
    expect(has('git-panel')).toBe(true)
    expect(container!.querySelector('[role="tabpanel"]')!.id).toBe('workspace-panel-git')
  })

  it('inline-remaps a remembered research tab while the repo check is pending (store untouched)', async () => {
    sidebarGate.research = false
    useUIStore.setState({ workspaceTabByProject: { p1: 'research' } })
    // No completed repo check for p1 — repoKnown is false, so the
    // normalization effect must not clobber the remembered tab yet.

    renderPanel()
    await flush()

    // Fail-closed non-git layout: the explorer renders this frame via the
    // INLINE remap, while the store still holds the remembered research tab.
    expect(has('file-tree')).toBe(true)
    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('research')

    // Once the check lands (a repo), the effect persists the git fallback.
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('git')
    expect(has('git-panel')).toBe(true)
  })

  it('keeps both gated tabs (and a remembered one) while both gates are on', async () => {
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p1')
    })
    useUIStore.setState({ workspaceTabByProject: { p1: 'research' } })

    renderPanel()
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('research')
    expect(container!.querySelector('[role="tabpanel"]')!.id).toBe('workspace-panel-research')
    expect(hasTab('research')).toBe(true)
    expect(hasTab('specs')).toBe(true)
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

    // The mocked @/api/specs payload flowed through specsStore into the DOM:
    // the declared section label, the declared document labels and the
    // framework name the payload reports.
    const s = useSpecsStore.getState()
    expect(s.projectId).toBe('p1')
    expect(s.status?.present).toBe(true)
    expect(s.error).toBeNull()
    expect(container!.textContent).toContain('Specification tree')
    expect(container!.textContent).toContain('Specs Index')
    expect(container!.textContent).toContain('2 documents')
    expect(container!.textContent).toContain('Specification System')
  })

  it('probes detection on a project switch without mounting the panel body', async () => {
    // The Git layout's default tab is neither Specs nor Research: the strip
    // still needs the detection answer to decide whether the Specs tab exists,
    // so ONE probe runs while no specs body is mounted.
    renderPanel()
    await flush()

    expect(useSpecsStore.getState().projectId).toBe('p1')
    expect(container!.querySelector('#workspace-panel-specs')).toBeNull()
  })

  // A fast p1→p2→p1 switch while the Specs tab stays inactive: the probe for
  // the RE-ENTERED project must start even though the sdd projection still
  // names it from its previous residency — the projection lands only with a
  // response, so matching on it skips a needed probe and lets the superseded
  // p2 response land late, overwriting the store context with a foreign
  // project (a phantom Specs tab no workspace event can heal: every
  // re-probe funnel refuses while the projection names a foreign project).
  it('supersedes an in-flight cross-project probe on a fast switch-back', async () => {
    renderPanel()
    await flush()

    // p1's own probe landed (positive); p2 is probed under a controlled,
    // still-in-flight deferred.
    expect(useSddStore.getState().projectId).toBe('p1')
    let releaseP2!: (payload: SpecsStatus) => void
    vi.mocked(getSpecsStatus).mockImplementationOnce(
      () =>
        new Promise<SpecsStatus>((resolve) => {
          releaseP2 = resolve
        }),
    )
    act(() => {
      useProjectStore.setState({ projects: [P1, P2, NO_PROJECT], activeProjectId: 'p2' })
    })
    await flush()

    expect(useSpecsStore.getState().projectId).toBe('p2')
    expect(useSpecsStore.getState().isLoading).toBe(true)
    const probesBeforeSwitchBack = vi.mocked(getSpecsStatus).mock.calls.length

    // While p2's probe is in flight, p2 turns negative and the user returns
    // to p1: the incoming probe must START (the fetch-START stamp is the
    // skip condition), superseding p2's flight.
    specsDetection.detected = []
    act(() => {
      useProjectStore.setState({ activeProjectId: 'p1' })
    })
    await flush()

    expect(vi.mocked(getSpecsStatus).mock.calls.length).toBe(probesBeforeSwitchBack + 1)
    expect(useSpecsStore.getState().projectId).toBe('p1')
    // p1's own (now negative) answer lands — no Specs tab.
    expect(useSddStore.getState().projectId).toBe('p1')
    expect(useSddStore.getState().present).toBe(false)
    expect(container!.querySelector('[data-segment-value="specs"]')).toBeNull()

    // The superseded p2 response arrives late and must be discarded
    // wholesale: the context stays p1's own, and no phantom Specs tab
    // appears.
    await act(async () => {
      releaseP2(specsPayload(['sdd']) as unknown as SpecsStatus)
    })
    await flush()

    expect(useSpecsStore.getState().projectId).toBe('p1')
    expect(useSddStore.getState().projectId).toBe('p1')
    expect(useSddStore.getState().present).toBe(false)
    expect(container!.querySelector('[data-segment-value="specs"]')).toBeNull()
  })

  it('hides the Specs tab when no SDD framework is detected (AC3)', async () => {
    specsDetection.detected = []

    renderPanel()
    await flush()

    expect(container!.querySelector('[data-segment-value="specs"]')).toBeNull()
    expect(useSddStore.getState().known).toBe(true)
    expect(useSddStore.getState().present).toBe(false)
  })

  it('falls a remembered specs tab back when nothing is detected', async () => {
    specsDetection.detected = []
    useUIStore.setState({ workspaceTabByProject: { p1: 'specs' } })
    // The store-side fallback runs once the repo check has landed; the inline
    // remap covers the pending window.
    useGitPanelStore.getState().setGitRepo(false, 'p1')

    renderPanel()
    await flush()

    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('explorer')
    expect(container!.querySelector('#workspace-panel-specs')).toBeNull()
  })

  // Review 16: a NEGATIVE detection answer must not be stale forever. The
  // panel that would refetch on workspace events never mounts for a hidden
  // Specs tab, so the permanently-mounted workspace panel re-probes when the
  // tree or the installed skill set changes — a framework installed AFTER the
  // first "not detected" answer makes the tab appear without a project
  // switch.
  it('re-probes a negative detection when the workspace tree changes', async () => {
    specsDetection.detected = []
    renderPanel()
    await flush()

    expect(container!.querySelector('[data-segment-value="specs"]')).toBeNull()
    expect(useSddStore.getState().known).toBe(true)
    expect(useSddStore.getState().present).toBe(false)
    const probesAfterMount = vi.mocked(getSpecsStatus).mock.calls.length

    // A framework is installed into the workspace; the watcher announces it.
    // The re-probe is debounced (one parse RPC per burst, not per event), so
    // virtual time advances past the trailing window.
    specsDetection.detected = ['sdd']
    vi.useFakeTimers()
    act(() => {
      emitLocalEvent('workspace:tree_changed', {
        paths: ['/ws/p1/.agents/skills/spec-init/SKILL.md'],
      })
    })
    expect(vi.mocked(getSpecsStatus).mock.calls.length).toBe(probesAfterMount)
    act(() => {
      vi.advanceTimersByTime(SPECS_DETECTION_REPROBE_DEBOUNCE_MS)
    })
    await flush()

    expect(vi.mocked(getSpecsStatus).mock.calls.length).toBeGreaterThan(probesAfterMount)
    expect(useSddStore.getState().present).toBe(true)
    expect(container!.querySelector('[data-segment-value="specs"]')).not.toBeNull()
  })

  // The skill set is the OTHER detection input (frameworks are recognised
  // from skill evidence): the skills:changed event re-probes too.
  it('re-probes a negative detection when the installed skills change', async () => {
    specsDetection.detected = []
    renderPanel()
    await flush()

    expect(useSddStore.getState().present).toBe(false)
    const probesAfterMount = vi.mocked(getSpecsStatus).mock.calls.length

    specsDetection.detected = ['sdd']
    vi.useFakeTimers()
    act(() => {
      emitLocalEvent('skills:changed', { dirs: ['/ws/p1/.agents/skills'] })
    })
    expect(vi.mocked(getSpecsStatus).mock.calls.length).toBe(probesAfterMount)
    act(() => {
      vi.advanceTimersByTime(SPECS_DETECTION_REPROBE_DEBOUNCE_MS)
    })
    await flush()

    expect(vi.mocked(getSpecsStatus).mock.calls.length).toBeGreaterThan(probesAfterMount)
    expect(useSddStore.getState().present).toBe(true)
  })

  // The debounce itself: a burst of invalidation events inside the window
  // collapses into ONE trailing probe — one parse RPC per burst, not per
  // event (the workspace-level re-probe walks the whole tree, so a write
  // burst must not multiply it).
  it('coalesces a burst of tree changes into a single trailing probe', async () => {
    specsDetection.detected = []
    renderPanel()
    await flush()

    expect(useSddStore.getState().present).toBe(false)
    const probesAfterMount = vi.mocked(getSpecsStatus).mock.calls.length

    vi.useFakeTimers()
    for (let i = 0; i < 5; i++) {
      act(() => {
        emitLocalEvent('workspace:tree_changed', { paths: [`/ws/p1/file-${i}.md`] })
      })
      // Restart the timer 1ms short of the window: every next event re-arms
      // it, so no probe may fire while the burst continues.
      act(() => {
        vi.advanceTimersByTime(SPECS_DETECTION_REPROBE_DEBOUNCE_MS - 1)
      })
      expect(vi.mocked(getSpecsStatus).mock.calls.length).toBe(probesAfterMount)
    }
    act(() => {
      vi.advanceTimersByTime(1)
    })
    await flush()
    expect(vi.mocked(getSpecsStatus).mock.calls.length).toBe(probesAfterMount + 1)
  })

  // A POSITIVE projection is refreshed from this level too. The Specs panel's
  // own `workspace:tree_changed` subscription only exists while the Specs tab
  // is MOUNTED, so on any other tab (Git, Explorer, Search, Research) nothing
  // else would notice a removed `specs/` directory or a removed skill: the tab
  // would outlive its framework and only exist to refuse itself when clicked.
  it('re-probes a positive detection when the workspace tree changes', async () => {
    renderPanel()
    await flush()

    expect(useSddStore.getState().present).toBe(true)
    const probesAfterMount = vi.mocked(getSpecsStatus).mock.calls.length

    specsDetection.detected = []
    vi.useFakeTimers()
    act(() => {
      emitLocalEvent('workspace:tree_changed', { paths: ['/ws/p1/specs/INDEX.md'] })
    })
    expect(vi.mocked(getSpecsStatus).mock.calls.length).toBe(probesAfterMount)
    act(() => {
      vi.advanceTimersByTime(SPECS_DETECTION_REPROBE_DEBOUNCE_MS)
    })
    await flush()

    expect(vi.mocked(getSpecsStatus).mock.calls.length).toBeGreaterThan(probesAfterMount)
    expect(useSddStore.getState().present).toBe(false)
    expect(container!.querySelector('[data-segment-value="specs"]')).toBeNull()
  })
})
