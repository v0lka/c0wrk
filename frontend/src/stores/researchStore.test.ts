import { describe, it, expect, beforeEach } from 'vitest'
import {
  useResearchStore,
  selectEnabled,
  selectActiveProject,
  RESEARCH_SIDEBAR_DEFAULT_WIDTH,
} from './researchStore'
import type { ResearchStatus, ResearchGraphResponse, HypothesisDraft } from '@/types/models'

function statusOf(enabled: boolean, projectId = 'proj-1'): ResearchStatus {
  return {
    enabled,
    project_id: projectId,
    research_root: enabled ? '/ws/.research' : '',
    root: enabled
      ? {
          path: '/ws/.research',
          index: [],
          projects: [
            {
              id: 'r1',
              brief: { id: 'r1', title: 'Brief' },
              graph: {
                nodes: [{ id: 'h1', title: 'H1', status: 'open' }],
                edges: [],
              },
              metrics: {
                total: 1,
                by_status: { open: 1 },
                confirmation_rate: 0,
                depth: 1,
                breadth: 1,
              },
              prior_art_count: 0,
              has_report: false,
              log: [],
            },
          ],
        }
      : undefined,
  }
}

describe('researchStore', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  it('starts empty', () => {
    const s = useResearchStore.getState()
    expect(s.status).toBeNull()
    expect(s.isLoading).toBe(false)
    expect(s.isToggling).toBe(false)
    expect(s.error).toBeNull()
    expect(s.projectId).toBeNull()
  })

  it('loadStatus stores status, stamps projectId, clears loading+error', () => {
    useResearchStore.getState().setLoading(true)
    useResearchStore.getState().setError('boom')
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')

    const s = useResearchStore.getState()
    expect(s.status?.enabled).toBe(true)
    expect(s.projectId).toBe('proj-1')
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })

  it('reset clears everything back to initial', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    useResearchStore.getState().setToggling(true)
    useResearchStore.getState().reset()
    const s = useResearchStore.getState()
    expect(s.status).toBeNull()
    expect(s.projectId).toBeNull()
    expect(s.isToggling).toBe(false)
  })

  it('setToggling / setLoading / setError mutate only their slice', () => {
    useResearchStore.getState().setToggling(true)
    expect(useResearchStore.getState().isToggling).toBe(true)
    useResearchStore.getState().setLoading(true)
    expect(useResearchStore.getState().isLoading).toBe(true)
    useResearchStore.getState().setError('err')
    expect(useResearchStore.getState().error).toBe('err')
  })

  it('reset restores workspace view state defaults (selection, draft, filter, width)', () => {
    const s = useResearchStore.getState()
    s.selectHypothesis('h1', { status: 'open', result: 'wip', timebox: '1w' })
    s.setHideTerminal(true)
    s.setSidebarWidth(420)
    useResearchStore.getState().reset()

    const after = useResearchStore.getState()
    expect(after.selectedHypothesisId).toBeNull()
    expect(after.selectedHypothesisProjectId).toBeNull()
    expect(after.hypothesisDraft).toBeNull()
    expect(after.hideTerminal).toBe(false)
    expect(after.sidebarWidth).toBe(RESEARCH_SIDEBAR_DEFAULT_WIDTH)
  })
})

describe('researchStore — workspace view state', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  it('selectHypothesis / setHypothesisDraft / setHideTerminal / setSidebarWidth mutate only their slices', () => {
    const loadedStatus = statusOf(true)
    useResearchStore.getState().loadStatus(loadedStatus, 'proj-1')
    const draft: HypothesisDraft = { status: 'confirmed', result: 'r', timebox: '2w' }
    useResearchStore.getState().selectHypothesis('h1', draft)
    expect(useResearchStore.getState().selectedHypothesisId).toBe('h1')
    // The selection is stamped with the research project it was made in.
    expect(useResearchStore.getState().selectedHypothesisProjectId).toBe('r1')
    expect(useResearchStore.getState().hypothesisDraft).toBe(draft)

    // Clearing nulls the composite key together with the draft.
    useResearchStore.getState().selectHypothesis(null, null)
    expect(useResearchStore.getState().selectedHypothesisProjectId).toBeNull()
    useResearchStore.getState().selectHypothesis('h1', draft)

    const next: HypothesisDraft = { status: 'confirmed', result: 'r2', timebox: '2w' }
    useResearchStore.getState().setHypothesisDraft(next)
    expect(useResearchStore.getState().hypothesisDraft).toBe(next)

    useResearchStore.getState().setHideTerminal(true)
    expect(useResearchStore.getState().hideTerminal).toBe(true)
    useResearchStore.getState().setSidebarWidth(340)
    expect(useResearchStore.getState().sidebarWidth).toBe(340)

    // Selection/draft edits never touch the loading data slices: the loaded
    // status object is still the very reference loadStatus stored.
    const s = useResearchStore.getState()
    expect(s.status).toBe(loadedStatus)
    expect(s.error).toBeNull()
  })

  it('clearing the selection clears the draft with it', () => {
    useResearchStore.getState().selectHypothesis('h1', {
      status: 'open',
      result: 'wip',
      timebox: '',
    })
    useResearchStore.getState().selectHypothesis(null, null)
    const s = useResearchStore.getState()
    expect(s.selectedHypothesisId).toBeNull()
    expect(s.hypothesisDraft).toBeNull()
  })

  it('loadStatus for a different project drops the selection and draft', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    useResearchStore.getState().selectHypothesis('h1', {
      status: 'open',
      result: 'wip',
      timebox: '',
    })

    // Project switch: the new status belongs to another project — a generic
    // node id like 'h1' must not silently reopen the wrong project's card.
    useResearchStore.getState().loadStatus(statusOf(true, 'proj-2'), 'proj-2')
    const s = useResearchStore.getState()
    expect(s.projectId).toBe('proj-2')
    expect(s.selectedHypothesisId).toBeNull()
    expect(s.selectedHypothesisProjectId).toBeNull()
    expect(s.hypothesisDraft).toBeNull()
  })

  it('loadStatus for the same project keeps the selection and draft (background refresh)', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    const draft: HypothesisDraft = { status: 'in-progress', result: 'wip', timebox: '' }
    useResearchStore.getState().selectHypothesis('h1', draft)

    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    const s = useResearchStore.getState()
    expect(s.selectedHypothesisId).toBe('h1')
    expect(s.selectedHypothesisProjectId).toBe('r1')
    expect(s.hypothesisDraft).toBe(draft)
  })

  it('loadGraph never clobbers an in-progress draft', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    useResearchStore.getState().selectHypothesis('h1', {
      status: 'open',
      result: 'unsaved edits',
      timebox: '3d',
    })

    // Incremental file-change update for the active project arrives while the
    // user is mid-edit — the draft must survive untouched.
    useResearchStore.getState().loadGraph({
      project_id: 'r1',
      graph: {
        nodes: [{ id: 'h1', title: 'H1', status: 'confirmed', result: 'external write' }],
        edges: [],
      },
      metrics: {
        total: 1,
        by_status: { confirmed: 1 },
        confirmation_rate: 1,
        depth: 1,
        breadth: 1,
      },
      has_report: false,
      log: [],
    })

    const s = useResearchStore.getState()
    expect(s.selectedHypothesisId).toBe('h1')
    expect(s.selectedHypothesisProjectId).toBe('r1')
    expect(s.hypothesisDraft).toEqual({
      status: 'open',
      result: 'unsaved edits',
      timebox: '3d',
    })
  })
})

describe('researchStore — loadGraph convergence semantics', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  function graphOf(projectId: string, logCount: number): ResearchGraphResponse {
    return {
      project_id: projectId,
      graph: { nodes: [], edges: [] },
      metrics: {
        total: logCount,
        by_status: {},
        confirmation_rate: 0,
        depth: 0,
        breadth: 0,
      },
      has_report: false,
      log: Array.from({ length: logCount }, (_, i) => ({
        id: String(i + 1),
        kind: 'note',
        created_at: '2026-01-01T00:00:00Z',
        message: `entry ${i + 1}`,
      })),
    }
  }

  it('applies an update and stamps lastGraphSyncAt even when the active R-NNN changed since the last full load', () => {
    // The store's cached active_project_id is a parse snapshot; the fresh
    // response (backend PickActiveProject) may legitimately name a DIFFERENT
    // known project — e.g. research-init created R-002 and updated the index.
    // The old guard rejected such updates forever, freezing the panel.
    const status = statusOf(true)
    status.root!.projects.push({
      ...status.root!.projects[0]!,
      id: 'r2',
      log: [],
    })
    useResearchStore.getState().loadStatus(status, 'proj-1')

    const applied = useResearchStore.getState().loadGraph(graphOf('r2', 5))
    expect(applied).toBe(true)
    const s = useResearchStore.getState()
    // The active project follows the fresh response…
    expect(s.status?.root?.active_project_id).toBe('r2')
    expect(selectActiveProject(s)?.log).toHaveLength(5)
    // …and the sync stamp moves (the watchdog compares it).
    expect(s.lastGraphSyncAt).toBeGreaterThan(0)
  })

  it('keeps the selection keyed to its project when the active R-NNN switches (no silent rebind)', () => {
    const status = statusOf(true)
    status.root!.projects.push({
      ...status.root!.projects[0]!,
      id: 'r2',
      log: [],
    })
    // r1 must be the active project at selection time (without an explicit
    // active_project_id the fallback picks the highest-numbered project).
    status.root!.active_project_id = 'r1'
    useResearchStore.getState().loadStatus(status, 'proj-1')
    useResearchStore.getState().selectHypothesis('h1', {
      status: 'open',
      result: 'unsaved edits for r1',
      timebox: '3d',
    })

    // Same workspace project, but the active research project switches to
    // r2 (research-init + PickActiveProject). The selection must stay keyed
    // to r1 — a generic node id like 'h1' exists in r2 as well, and letting
    // it rebind would pair r1's unsaved draft with r2's card (Save would
    // then overwrite the wrong project's hypothesis via the active-R-NNN
    // backend semantics).
    const applied = useResearchStore.getState().loadGraph(graphOf('r2', 5))
    expect(applied).toBe(true)
    const s = useResearchStore.getState()
    expect(s.status?.root?.active_project_id).toBe('r2')
    expect(s.selectedHypothesisId).toBe('h1')
    expect(s.selectedHypothesisProjectId).toBe('r1')
    expect(s.hypothesisDraft).toEqual({
      status: 'open',
      result: 'unsaved edits for r1',
      timebox: '3d',
    })
  })

  it('rejects a snapshot fetched before the last sync (stale) — no apply, no re-stamp', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')

    // A newer sync lands (e.g. the watchdog's full refresh): seq → 2.
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')

    // The slow incremental fetch STARTED at seq 1 — before that sync — and
    // only resolves now: applying it would regress the panel to the older
    // snapshot AND re-stamp it fresh (defusing the watchdog).
    const applied = useResearchStore.getState().loadGraph(graphOf('r1', 5), 1)
    expect(applied).toBe(false)
    const s = useResearchStore.getState()
    expect(s.graphSyncSeq).toBe(2)
    expect(selectActiveProject(s)?.log).toHaveLength(0)

    // A fetch started after the last sync applies normally.
    const appliedFresh = useResearchStore.getState().loadGraph(graphOf('r1', 5), 2)
    expect(appliedFresh).toBe(true)
    expect(selectActiveProject(useResearchStore.getState())?.log).toHaveLength(5)
  })

  it('returns false (no partial apply) for a brand-new unknown R-NNN — caller must full-refresh', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    const before = useResearchStore.getState().status
    const applied = useResearchStore.getState().loadGraph(graphOf('r-new', 3))
    expect(applied).toBe(false)
    // Untouched — the incremental path cannot render an unknown project.
    expect(useResearchStore.getState().status).toBe(before)
  })

  it('loadStatus also stamps lastGraphSyncAt (a full sync is a sync)', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    expect(useResearchStore.getState().lastGraphSyncAt).toBeGreaterThan(0)
  })

  it('loadGraph clears a stuck error and isLoading when it applies — a successful incremental sync is a successful sync', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    // A previously failed status fetch left an error behind, and a refresh
    // cycle left the spinner on — nothing on the loadStatus path clears
    // them once the incremental path takes over.
    useResearchStore.getState().setError('previous fetch failed')
    useResearchStore.getState().setLoading(true)

    const applied = useResearchStore.getState().loadGraph(graphOf('r1', 3))
    expect(applied).toBe(true)
    const s = useResearchStore.getState()
    expect(s.error).toBeNull()
    expect(s.isLoading).toBe(false)
    // The graph itself landed too.
    expect(selectActiveProject(s)?.log).toHaveLength(3)
  })

  it('loadStatus for a different project drops the recommended next step (cross-project), same-project keeps it', () => {
    const nextStep = {
      project_id: 'r1',
      action: 'research-hypothesis' as const,
      reason: 'add a competing hypothesis',
      skill: 'research-hypothesis',
    }
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    useResearchStore.getState().loadNextStep(nextStep)
    expect(useResearchStore.getState().nextStep).not.toBeNull()

    // Project switch: the recommendation is phase-derived per project and
    // its refetch is best-effort — the OLD project's card must not survive
    // the switch (a failed next-step fetch would leave it rendering
    // indefinitely).
    useResearchStore.getState().loadStatus(statusOf(true, 'proj-2'), 'proj-2')
    expect(useResearchStore.getState().nextStep).toBeNull()

    // A same-project background reload keeps the current recommendation —
    // ordinary refreshes must not blank the card between fetches.
    useResearchStore.getState().loadNextStep({ ...nextStep, project_id: 'r1' })
    useResearchStore.getState().loadStatus(statusOf(true, 'proj-2'), 'proj-2')
    expect(useResearchStore.getState().nextStep).not.toBeNull()
  })

  it('last-write-wins: stale full-refresh and mutation payloads (older seq tickets) are rejected wholesale', () => {
    // Direction 1 — a mutation that started before a newer full sync must
    // not overwrite it: the watchdog's full refresh and the file-watcher's
    // fallback both write via loadStatus, so its ticket guard covers them.
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1') // seq → 1
    const watchdogTicket = useResearchStore.getState().graphSyncSeq // captured at fetch START (= 1)
    // …the user's quick-mutation lands first (seq → 2)…
    useResearchStore.getState().loadGraph(graphOf('r1', 5), 1)
    // …then the watchdog's pre-mutation snapshot resolves last — rejected.
    const statusApplied = useResearchStore.getState().loadStatus(
      statusOf(true),
      'proj-1',
      watchdogTicket,
    )
    expect(statusApplied).toBe(false)
    let s = useResearchStore.getState()
    expect(selectActiveProject(s)?.log).toHaveLength(5) // mutation survived
    expect(s.graphSyncSeq).toBe(2)

    // Direction 2 — a mutation response resolving after a newer sync must
    // not regress the panel either (the old wall-clock guard never covered
    // mutations: they applied unconditionally).
    const mutationTicket = useResearchStore.getState().graphSyncSeq // = 2
    // …a newer sync lands while the mutation RPC is in flight (seq → 3)…
    useResearchStore.getState().loadGraph(graphOf('r1', 4), 2)
    // …then the mutation's (older) response resolves last — rejected.
    const mutationApplied = useResearchStore.getState().loadGraph(
      graphOf('r1', 0),
      mutationTicket,
    )
    expect(mutationApplied).toBe(false)
    s = useResearchStore.getState()
    expect(selectActiveProject(s)?.log).toHaveLength(4) // the newer sync's data
    expect(s.graphSyncSeq).toBe(3)

    // A write whose ticket is current applies normally afterwards.
    expect(useResearchStore.getState().loadGraph(graphOf('r1', 7), 3)).toBe(true)
    expect(selectActiveProject(useResearchStore.getState())?.log).toHaveLength(7)
  })
})

describe('researchStore selectors', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  it('selectEnabled is false when no status loaded', () => {
    expect(selectEnabled(useResearchStore.getState())).toBe(false)
  })

  it('selectEnabled reflects status.enabled', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    expect(selectEnabled(useResearchStore.getState())).toBe(true)
    useResearchStore.getState().loadStatus(statusOf(false), 'proj-1')
    expect(selectEnabled(useResearchStore.getState())).toBe(false)
  })

  it('selectActiveProject returns the first project when on, null when off', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    expect(selectActiveProject(useResearchStore.getState())?.id).toBe('r1')
    useResearchStore.getState().loadStatus(statusOf(false), 'proj-1')
    expect(selectActiveProject(useResearchStore.getState())).toBeNull()
  })

  it('selectActiveProject follows active_project_id over projects[0] ordering', () => {
    // Two projects: r1 (sorted first) and r2. The active project is r2 (the
    // latest), carried as root.active_project_id by the backend. The selector
    // must follow it, not blindly pick projects[0].
    const status: ResearchStatus = {
      enabled: true,
      project_id: 'proj-1',
      research_root: '/ws/.research',
      root: {
        path: '/ws/.research',
        index: [],
        active_project_id: 'r2',
        projects: [
          {
            id: 'r1',
            brief: { id: 'r1', title: 'First' },
            graph: { nodes: [], edges: [] },
            metrics: {
              total: 0,
              by_status: {},
              confirmation_rate: 0,
              depth: 0,
              breadth: 0,
            },
            prior_art_count: 0,
            has_report: false,
            log: [],
          },
          {
            id: 'r2',
            brief: { id: 'r2', title: 'Second (latest)' },
            graph: { nodes: [], edges: [] },
            metrics: {
              total: 0,
              by_status: {},
              confirmation_rate: 0,
              depth: 0,
              breadth: 0,
            },
            prior_art_count: 0,
            has_report: false,
            log: [],
          },
        ],
      },
    }
    useResearchStore.getState().loadStatus(status, 'proj-1')
    expect(selectActiveProject(useResearchStore.getState())?.id).toBe('r2')
  })

  it('selectors return stable primitives/references (no per-call allocation)', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    const st = useResearchStore.getState()
    // selectActiveProject returns the same object reference both calls.
    expect(selectActiveProject(st)).toBe(selectActiveProject(st))
    // selectEnabled is a primitive boolean.
    expect(typeof selectEnabled(st)).toBe('boolean')
  })
})
