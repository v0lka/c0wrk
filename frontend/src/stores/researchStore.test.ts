import { describe, it, expect, beforeEach } from 'vitest'
import {
  useResearchStore,
  selectEnabled,
  selectActiveProject,
  selectActiveHypothesisId,
  RESEARCH_CARD_DEFAULT_HEIGHT,
} from './researchStore'
import type {
  ResearchProject,
  ResearchStatus,
  ResearchGraphResponse,
  HypothesisDraft,
} from '@/types/models'

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

/** A minimal well-formed draft (fields default to '' apart from the args). */
function draftOf(status: string, result: string, timebox: string): HypothesisDraft {
  return {
    title: 'Hypothesis',
    parents: '',
    status,
    decision: '',
    statement: '',
    verification_criterion: '',
    experiment_notes: '',
    timebox,
    result,
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

  it('reset restores workspace view state defaults (selection, draft, filter, height)', () => {
    const s = useResearchStore.getState()
    s.selectHypothesis('h1', draftOf('open', 'wip', '1w'))
    s.setHideTerminal(true)
    s.setCardHeight(420)
    useResearchStore.getState().reset()

    const after = useResearchStore.getState()
    expect(after.selectedHypothesisId).toBeNull()
    expect(after.selectedHypothesisProjectId).toBeNull()
    expect(after.hypothesisDraft).toBeNull()
    expect(after.hideTerminal).toBe(false)
    expect(after.cardHeight).toBe(RESEARCH_CARD_DEFAULT_HEIGHT)
  })
})

describe('researchStore — workspace view state', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  it('selectHypothesis / setHypothesisDraft / setHideTerminal / setCardHeight mutate only their slices', () => {
    const loadedStatus = statusOf(true)
    useResearchStore.getState().loadStatus(loadedStatus, 'proj-1')
    const draft = draftOf('confirmed', 'r', '2w')
    useResearchStore.getState().selectHypothesis('h1', draft)
    expect(useResearchStore.getState().selectedHypothesisId).toBe('h1')
    // The selection is stamped with the research project it was made in.
    expect(useResearchStore.getState().selectedHypothesisProjectId).toBe('r1')
    expect(useResearchStore.getState().hypothesisDraft).toBe(draft)

    // Clearing nulls the composite key together with the draft.
    useResearchStore.getState().selectHypothesis(null, null)
    expect(useResearchStore.getState().selectedHypothesisProjectId).toBeNull()
    useResearchStore.getState().selectHypothesis('h1', draft)

    const next = draftOf('confirmed', 'r2', '2w')
    useResearchStore.getState().setHypothesisDraft(next)
    expect(useResearchStore.getState().hypothesisDraft).toBe(next)

    useResearchStore.getState().setHideTerminal(true)
    expect(useResearchStore.getState().hideTerminal).toBe(true)
    useResearchStore.getState().setCardHeight(340)
    expect(useResearchStore.getState().cardHeight).toBe(340)

    // Selection/draft edits never touch the loading data slices: the loaded
    // status object is still the very reference loadStatus stored.
    const s = useResearchStore.getState()
    expect(s.status).toBe(loadedStatus)
    expect(s.error).toBeNull()
  })

  it('clearing the selection clears the draft with it', () => {
    useResearchStore.getState().selectHypothesis('h1', draftOf('open', 'wip', ''))
    useResearchStore.getState().selectHypothesis(null, null)
    const s = useResearchStore.getState()
    expect(s.selectedHypothesisId).toBeNull()
    expect(s.hypothesisDraft).toBeNull()
  })

  it('loadStatus for a different project drops the selection and draft', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    useResearchStore.getState().selectHypothesis('h1', draftOf('open', 'wip', ''))

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
    const draft: HypothesisDraft = draftOf('in-progress', 'wip', '')
    useResearchStore.getState().selectHypothesis('h1', draft)

    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    const s = useResearchStore.getState()
    expect(s.selectedHypothesisId).toBe('h1')
    expect(s.selectedHypothesisProjectId).toBe('r1')
    expect(s.hypothesisDraft).toBe(draft)
  })

  it('loadGraph never clobbers an in-progress draft', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    useResearchStore.getState().selectHypothesis('h1', draftOf('open', 'unsaved edits', '3d'))

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
    expect(s.hypothesisDraft).toEqual(draftOf('open', 'unsaved edits', '3d'))
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
    useResearchStore.getState().selectHypothesis('h1', draftOf('open', 'unsaved edits for r1', '3d'))

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
    expect(s.hypothesisDraft).toEqual(draftOf('open', 'unsaved edits for r1', '3d'))
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

// --- Fixtures for the dashboard current-card + pins suites ---

/** A research project fixture with explicit hypothesis nodes and active
 *  front (the front lists node ids; its first entry is the leader). */
function projOf(
  id: string,
  nodes: { id: string; status?: string }[],
  front: string[],
): ResearchProject {
  return {
    id,
    brief: { id, title: `Brief ${id}` },
    graph: {
      nodes: nodes.map((n) => ({ id: n.id, title: n.id, status: n.status ?? 'open' })),
      edges: [],
    },
    metrics: {
      total: nodes.length,
      by_status: {},
      confirmation_rate: 0,
      depth: 1,
      breadth: 1,
      active_front: front,
    },
    prior_art_count: 0,
    has_report: false,
    log: [],
  }
}

function statusWithRoot(
  projects: ResearchProject[],
  activeProjectId?: string,
  pins?: { research?: string[]; hypotheses?: Record<string, string[]> },
  projectId = 'proj-1',
): ResearchStatus {
  return {
    enabled: true,
    project_id: projectId,
    research_root: '/ws/.research',
    root: {
      path: '/ws/.research',
      index: [],
      active_project_id: activeProjectId,
      projects,
    },
    pinned_research: pins?.research,
    pinned_hypotheses: pins?.hypotheses,
  }
}

/** A graph response for one research project, mirroring projOf's shape. */
function cardGraphOf(
  projectId: string,
  nodes: { id: string; status?: string }[],
  front: string[],
): ResearchGraphResponse {
  const project = projOf(projectId, nodes, front)
  return {
    project_id: projectId,
    graph: project.graph,
    metrics: project.metrics,
    has_report: false,
    log: [],
  }
}

describe('researchStore — dashboard current card (active hypothesis)', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  it('loadStatus defaults the current card to the active front leading hypothesis', () => {
    useResearchStore
      .getState()
      .loadStatus(statusWithRoot([projOf('r1', [{ id: 'h1' }, { id: 'h2' }], ['h1', 'h2'])]), 'proj-1')

    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h1')
    expect(s.activeHypothesisResearchId).toBe('r1')
  })

  it('setActiveHypothesis stamps the active research project; null clears both fields', () => {
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot([projOf('r1', [{ id: 'h1' }, { id: 'h2' }, { id: 'h3' }], ['h1'])]),
        'proj-1',
      )
    // A non-leader card: distinguishes the user's pick from the front default.
    useResearchStore.getState().setActiveHypothesis('h3')

    let s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h3')
    expect(s.activeHypothesisResearchId).toBe('r1')

    useResearchStore.getState().setActiveHypothesis(null)
    s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBeNull()
    expect(s.activeHypothesisResearchId).toBeNull()
  })

  it('a still-valid current card survives same-project reloads', () => {
    const status = statusWithRoot(
      [projOf('r1', [{ id: 'h1' }, { id: 'h2' }, { id: 'h3' }], ['h1'])],
    )
    useResearchStore.getState().loadStatus(status, 'proj-1')
    useResearchStore.getState().setActiveHypothesis('h3')

    // Background refresh with identical data: the pick stays.
    useResearchStore.getState().loadStatus(status, 'proj-1')
    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h3')
    expect(s.activeHypothesisResearchId).toBe('r1')
  })

  it('the current card resets to the new front when the active research switches', () => {
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot(
          [projOf('r1', [{ id: 'h1' }], ['h1']), projOf('r2', [{ id: 'h9' }], ['h9'])],
          'r1',
        ),
        'proj-1',
      )
    useResearchStore.getState().setActiveHypothesis('h1')

    // The active R-NNN switches to r2 (e.g. SetActiveResearch) — the old
    // card belonged to r1, so the dashboard follows r2's front leader.
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot(
          [projOf('r1', [{ id: 'h1' }], ['h1']), projOf('r2', [{ id: 'h9' }], ['h9'])],
          'r2',
        ),
        'proj-1',
      )
    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h9')
    expect(s.activeHypothesisResearchId).toBe('r2')
  })

  it('the current card resets to the front when its hypothesis disappears', () => {
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot([projOf('r1', [{ id: 'h1' }, { id: 'h2' }], ['h1'])]),
        'proj-1',
      )
    useResearchStore.getState().setActiveHypothesis('h2')

    // h2's card was deleted externally: the refresh's graph no longer has
    // it — the current card falls back to the front leader.
    useResearchStore
      .getState()
      .loadStatus(statusWithRoot([projOf('r1', [{ id: 'h1' }], ['h1'])]), 'proj-1')
    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h1')
    expect(s.activeHypothesisResearchId).toBe('r1')
  })

  it('an empty active front clears the current card (project-level recommendation)', () => {
    // All hypotheses terminal → no front → no current card; the next-step
    // call sites then pass '' (project-level).
    useResearchStore
      .getState()
      .loadStatus(statusWithRoot([projOf('r1', [{ id: 'h1', status: 'confirmed' }], [])]), 'proj-1')
    let s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBeNull()
    expect(s.activeHypothesisResearchId).toBeNull()

    // A new open hypothesis re-opens the front — the card follows it again.
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot([
          projOf('r1', [{ id: 'h1', status: 'confirmed' }, { id: 'h2' }], ['h2']),
        ]),
        'proj-1',
      )
    s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h2')
    expect(s.activeHypothesisResearchId).toBe('r1')
  })

  it('a cross-project load never inherits the previous project current card (R-NNN collision)', () => {
    // proj-1: r1 with front leader h1; the user's current card is h1.
    useResearchStore
      .getState()
      .loadStatus(statusWithRoot([projOf('r1', [{ id: 'h1' }, { id: 'h2' }], ['h1'])]), 'proj-1')
    useResearchStore.getState().setActiveHypothesis('h1')
    expect(useResearchStore.getState().activeHypothesisId).toBe('h1')

    // proj-2: a DIFFERENT research root whose project also uses the ids r1
    // and h1 — but whose front leader is h2. Inheriting the previous
    // project's card (h1) would silently scope the new project's
    // recommendation to the wrong card; the load must re-derive from the
    // new front instead.
    useResearchStore.getState().loadStatus(
      statusWithRoot([projOf('r1', [{ id: 'h1' }, { id: 'h2' }], ['h2'])], undefined, undefined, 'proj-2'),
      'proj-2',
    )
    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h2')
    expect(s.activeHypothesisResearchId).toBe('r1')
  })

  it('a front entry with no matching node is skipped in favor of the next valid one', () => {
    // The RPC boundary drops malformed node entries, so a front entry can
    // dangle after sanitizing — the reconciliation picks the next front
    // entry that still resolves to a node.
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot([projOf('r1', [{ id: 'h1' }], ['h-ghost', 'h1'])]),
        'proj-1',
      )
    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h1')
    expect(s.activeHypothesisResearchId).toBe('r1')
  })

  it('loadGraph keeps the current card while it still resolves', () => {
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot([projOf('r1', [{ id: 'h1' }, { id: 'h2' }, { id: 'h3' }], ['h1'])]),
        'proj-1',
      )
    useResearchStore.getState().setActiveHypothesis('h3')

    const applied = useResearchStore
      .getState()
      .loadGraph(cardGraphOf('r1', [{ id: 'h1' }, { id: 'h2' }, { id: 'h3' }], ['h1']))
    expect(applied).toBe(true)
    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h3')
    expect(s.activeHypothesisResearchId).toBe('r1')
  })

  it('loadGraph resets the current card when its node disappears', () => {
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot([projOf('r1', [{ id: 'h1' }, { id: 'h2' }], ['h1'])]),
        'proj-1',
      )
    useResearchStore.getState().setActiveHypothesis('h2')

    // The incremental update carries the deletion of h2's card.
    const applied = useResearchStore.getState().loadGraph(cardGraphOf('r1', [{ id: 'h1' }], ['h1']))
    expect(applied).toBe(true)
    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h1')
    expect(s.activeHypothesisResearchId).toBe('r1')
  })

  it('loadGraph follows an active-R-NNN switch by resetting to the new project front', () => {
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot(
          [projOf('r1', [{ id: 'h1' }], ['h1']), projOf('r2', [{ id: 'h9' }], ['h9'])],
          'r1',
        ),
        'proj-1',
      )
    useResearchStore.getState().setActiveHypothesis('h1')

    // A response for r2 (the backend's fresh PickActiveProject choice).
    const applied = useResearchStore.getState().loadGraph(cardGraphOf('r2', [{ id: 'h9' }], ['h9']))
    expect(applied).toBe(true)
    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h9')
    expect(s.activeHypothesisResearchId).toBe('r2')
  })

  it('a stale loadGraph ticket leaves the current card untouched (rejected wholesale)', () => {
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot([projOf('r1', [{ id: 'h1' }, { id: 'h2' }], ['h1'])]),
        'proj-1',
      )
    useResearchStore.getState().setActiveHypothesis('h2')

    const applied = useResearchStore
      .getState()
      .loadGraph(cardGraphOf('r1', [{ id: 'h1' }], ['h1']), 0) // stale ticket
    expect(applied).toBe(false)
    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('h2')
    expect(s.activeHypothesisResearchId).toBe('r1')
  })
})

describe('researchStore — pins', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  it('loadStatus mirrors pinned_research/pinned_hypotheses from the status', () => {
    const pins = {
      research: ['R-001-web-exfil/brief.md'],
      hypotheses: {
        // The same H-001 pinned in two research projects — a list per key.
        'H-001': [
          'R-001-web-exfil/hypotheses/H-001.md',
          'R-002-other/hypotheses/H-001.md',
        ],
      },
    }
    useResearchStore
      .getState()
      .loadStatus(statusWithRoot([projOf('r1', [], [])], undefined, pins), 'proj-1')

    const s = useResearchStore.getState()
    expect(s.pinnedResearch).toEqual(pins.research)
    expect(s.pinnedHypotheses).toEqual(pins.hypotheses)
  })

  it('loadStatus defaults absent pins to empty collections (older payloads)', () => {
    useResearchStore
      .getState()
      .loadStatus(statusWithRoot([projOf('r1', [], [])]), 'proj-1')

    const s = useResearchStore.getState()
    expect(s.pinnedResearch).toEqual([])
    expect(s.pinnedHypotheses).toEqual({})
  })

  it('loadGraph preserves the pins (file edits never change them)', () => {
    const pins = { research: ['R-001-x/brief.md'] }
    useResearchStore
      .getState()
      .loadStatus(statusWithRoot([projOf('r1', [{ id: 'h1' }], ['h1'])], undefined, pins), 'proj-1')

    const applied = useResearchStore.getState().loadGraph(cardGraphOf('r1', [{ id: 'h1' }], ['h1']))
    expect(applied).toBe(true)
    // The incremental graph response carries no pins — the persisted pins
    // must survive untouched (they change only via the pin RPCs, whose
    // callers refresh the full status).
    expect(useResearchStore.getState().pinnedResearch).toEqual(pins.research)
  })

  it('reset clears the pins (and the current card) back to initial', () => {
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot(
          [projOf('r1', [{ id: 'h1' }], ['h1'])],
          undefined,
          { research: ['R-001-x/brief.md'], hypotheses: { 'H-001': ['R-001-x/hypotheses/H-001.md'] } },
        ),
        'proj-1',
      )
    expect(useResearchStore.getState().activeHypothesisId).toBe('h1')

    useResearchStore.getState().reset()
    const s = useResearchStore.getState()
    expect(s.pinnedResearch).toEqual([])
    expect(s.pinnedHypotheses).toEqual({})
    expect(s.activeHypothesisId).toBeNull()
    expect(s.activeHypothesisResearchId).toBeNull()
  })
})

describe('researchStore — selectActiveHypothesisId', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  it('returns the current card id while its stamp matches the active project', () => {
    useResearchStore
      .getState()
      .loadStatus(
        statusWithRoot([projOf('r1', [{ id: 'h1' }, { id: 'h2' }], ['h1'])]),
        'proj-1',
      )
    useResearchStore.getState().setActiveHypothesis('h2')
    expect(selectActiveHypothesisId(useResearchStore.getState())).toBe('h2')
  })

  it('returns "" with no current card (fresh store, or an empty front)', () => {
    expect(selectActiveHypothesisId(useResearchStore.getState())).toBe('')

    useResearchStore
      .getState()
      .loadStatus(statusWithRoot([projOf('r1', [{ id: 'h1', status: 'confirmed' }], [])]), 'proj-1')
    expect(selectActiveHypothesisId(useResearchStore.getState())).toBe('')
  })

  it('returns "" for an unstamped selection (made while nothing was active)', () => {
    // A selection made before any status loaded carries no R-NNN stamp —
    // it must never resolve (mirrors the workspace selection semantics).
    useResearchStore.getState().setActiveHypothesis('h1')
    expect(useResearchStore.getState().activeHypothesisId).toBe('h1')
    expect(useResearchStore.getState().activeHypothesisResearchId).toBeNull()
    expect(selectActiveHypothesisId(useResearchStore.getState())).toBe('')
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
