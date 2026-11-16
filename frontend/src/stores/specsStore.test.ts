// @vitest-environment jsdom
// (jsdom: the persist middleware reads window/localStorage on rehydration
// and the partialize outcome assertions need the runtime store.)
import { describe, it, expect, vi, beforeEach } from 'vitest'

const getSpecsStatusMock = vi.hoisted(() => vi.fn())
const setActiveSDDMock = vi.hoisted(() => vi.fn())

vi.mock('@/api/specs', () => ({
  getSpecsStatus: getSpecsStatusMock,
  setActiveSDD: setActiveSDDMock,
}))

import { useSpecsStore } from './specsStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from './sessionStore'
import { useSddStore } from './sddStore'
import type { SDDFramework, SpecsStatus } from '@/types/models'

/** One detected framework as the backend reports it. */
function frameworkOf(id: string, label: string, treePresent = true): SDDFramework {
  return {
    id,
    label,
    origin: 'builtin',
    scope: 'project',
    tree_present: treePresent,
    root_present: treePresent,
    marker_present: treePresent,
    skills: [],
  }
}

function statusOf(present: boolean, projectId: string): SpecsStatus {
  const detected = present ? [frameworkOf('sdd', 'Specification System')] : []
  return {
    present,
    tree_present: present,
    root: present ? `/ws/${projectId}/specs` : '',
    workspace: `/ws/${projectId}`,
    detected,
    active_id: present ? 'sdd' : '',
    active: detected[0],
    selector_needed: false,
    roots: [],
    documents: [],
    nodes: [],
    views: [],
    findings: [],
    actions: [],
    counts: { documents: 0, findings: 0, by_concept: {}, findings_by_kind: {} },
    warnings: [],
  }
}

/** Seed the project store the way the app does: a loaded list with `id`
 *  active. `isNoProject` marks the entry as the CHAT pseudo-project. */
function seedProject(id: string | null, isNoProject = false): void {
  useProjectStore.setState({
    projects:
      id === null
        ? null
        : [
            {
              id,
              name: isNoProject ? 'No Project' : id,
              workspace_path: '',
              is_external: false,
              is_no_project: isNoProject,
              created_at: '2026-01-01T00:00:00Z',
              last_active_at: '2026-01-01T00:00:00Z',
            },
          ],
    activeProjectId: id,
  })
}

/** A controllable pending fetch: resolves only when the test releases it. */
interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason: unknown) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/** Let pending microtasks (fetch continuations) settle. */
async function flush(): Promise<void> {
  for (let i = 0; i < 10; i++) await Promise.resolve()
}

beforeEach(() => {
  getSpecsStatusMock.mockReset()
  setActiveSDDMock.mockReset()
  useSpecsStore.getState().reset()
  useProjectStore.setState({ projects: null, activeProjectId: null })
  useSessionStore.setState({ sessions: null, activeSessionId: null })
})

describe('specsStore — fetchStatus', () => {
  it('stores a successful status stamped with its projectId', async () => {
    const status = statusOf(true, 'p1')
    getSpecsStatusMock.mockResolvedValue(status)

    await useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(getSpecsStatusMock).toHaveBeenCalledWith('p1')
    expect(s.status).toBe(status)
    expect(s.projectId).toBe('p1')
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })

  it('stores the neutral present=false state as data, not an error', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(false, 'p1'))

    await useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(s.status?.present).toBe(false)
    expect(s.error).toBeNull()
    expect(s.isLoading).toBe(false)
  })

  it('stores the error message and keeps the previous status on failure', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')

    getSpecsStatusMock.mockRejectedValue(new Error('parse exploded'))
    await useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(s.error).toBe('parse exploded')
    expect(s.isLoading).toBe(false)
    // The stale-but-useful previous status stays rendered alongside.
    expect(s.status?.present).toBe(true)
    expect(s.projectId).toBe('p1')
  })

  it('marks isLoading for the whole flight and clears it at the end', async () => {
    const d = deferred<SpecsStatus>()
    getSpecsStatusMock.mockReturnValue(d.promise)

    const pending = useSpecsStore.getState().fetchStatus('p1')
    expect(useSpecsStore.getState().isLoading).toBe(true)

    d.resolve(statusOf(true, 'p1'))
    await pending

    expect(useSpecsStore.getState().isLoading).toBe(false)
  })

  it('discards a superseded response (last-write-wins by initiation order)', async () => {
    const first = deferred<SpecsStatus>()
    const second = deferred<SpecsStatus>()
    getSpecsStatusMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    const p1 = useSpecsStore.getState().fetchStatus('p1')
    const p2 = useSpecsStore.getState().fetchStatus('p2')

    // The OLDER fetch resolves last — it must not overwrite the newer one.
    second.resolve(statusOf(true, 'p2'))
    await p2
    first.resolve(statusOf(true, 'p1'))
    await p1
    await flush()

    const s = useSpecsStore.getState()
    expect(s.projectId).toBe('p2')
    expect(s.status?.root).toBe('/ws/p2/specs')
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })

  it('discards a superseded error response too', async () => {
    const first = deferred<SpecsStatus>()
    const second = deferred<SpecsStatus>()
    getSpecsStatusMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    const p1 = useSpecsStore.getState().fetchStatus('p1')
    const p2 = useSpecsStore.getState().fetchStatus('p2')

    second.resolve(statusOf(true, 'p2'))
    await p2
    first.reject(new Error('stale failure'))
    await p1
    await flush()

    const s = useSpecsStore.getState()
    expect(s.projectId).toBe('p2')
    expect(s.status?.root).toBe('/ws/p2/specs')
    expect(s.error).toBeNull()
  })
})

describe('specsStore — cross-project guards', () => {
  it("clears the previous project's status the moment a different project's fetch starts", async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')
    expect(useSpecsStore.getState().status?.root).toBe('/ws/p1/specs')

    // p2's fetch never resolves: the clear must be synchronous with START,
    // not an effect of the response landing.
    getSpecsStatusMock.mockReturnValue(new Promise(() => {}))
    void useSpecsStore.getState().fetchStatus('p2')

    const s = useSpecsStore.getState()
    expect(s.status).toBeNull()
    expect(s.projectId).toBe('p2')
    expect(s.isLoading).toBe(true)
  })

  it('a failed different-project fetch leaves no cross-project data to keep', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')

    getSpecsStatusMock.mockRejectedValue(new Error('p2 fetch failed'))
    await useSpecsStore.getState().fetchStatus('p2')

    const s = useSpecsStore.getState()
    expect(s.error).toBe('p2 fetch failed')
    expect(s.status).toBeNull()
    expect(s.projectId).toBe('p2')
  })

  it('keeps the previous status during a SAME-project refetch (no flicker)', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')

    getSpecsStatusMock.mockReturnValue(new Promise(() => {}))
    void useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(s.status?.root).toBe('/ws/p1/specs')
    expect(s.projectId).toBe('p1')
    expect(s.isLoading).toBe(true)
  })
})

describe('specsStore — session scoping (the parse belongs to a (project, session) pair)', () => {
  it('stamps the ACTIVE session id on a successful fetch', async () => {
    useSessionStore.setState({ sessions: null, activeSessionId: 's1' })
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))

    await useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(s.projectId).toBe('p1')
    expect(s.sessionId).toBe('s1')
    expect(s.status?.present).toBe(true)
  })

  it("a same-project fetch for a NEW session clears the previous status the moment it starts", async () => {
    // The backend scans the ACTIVE session's workspace, so s2's parse is a
    // different parse even though the project is unchanged — the previous
    // session's status must not render during the flight.
    useSessionStore.setState({ sessions: null, activeSessionId: 's1' })
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')
    expect(useSpecsStore.getState().status?.root).toBe('/ws/p1/specs')

    useSessionStore.setState({ sessions: null, activeSessionId: 's2' })
    // s2's fetch never resolves: the clear must be synchronous with START,
    // mirroring the different-project rule.
    getSpecsStatusMock.mockReturnValue(new Promise(() => {}))
    void useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(s.status).toBeNull()
    expect(s.projectId).toBe('p1')
    expect(s.sessionId).toBe('s2')
    expect(s.isLoading).toBe(true)
  })

  it('a failed new-session fetch leaves no previous-session data to keep', async () => {
    useSessionStore.setState({ sessions: null, activeSessionId: 's1' })
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')

    useSessionStore.setState({ sessions: null, activeSessionId: 's2' })
    getSpecsStatusMock.mockRejectedValue(new Error('s2 parse failed'))
    await useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(s.error).toBe('s2 parse failed')
    expect(s.status).toBeNull()
    expect(s.projectId).toBe('p1')
    expect(s.sessionId).toBe('s2')
  })

  it('keeps the previous status when BOTH the project and the session are unchanged', async () => {
    useSessionStore.setState({ sessions: null, activeSessionId: 's1' })
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')

    getSpecsStatusMock.mockReturnValue(new Promise(() => {}))
    void useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(s.status?.root).toBe('/ws/p1/specs')
    expect(s.projectId).toBe('p1')
    expect(s.sessionId).toBe('s1')
    expect(s.isLoading).toBe(true)
  })

  it('LWW holds across a session switch: the previous session\'s late response is discarded', async () => {
    const first = deferred<SpecsStatus>()
    const second = deferred<SpecsStatus>()
    getSpecsStatusMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    useSessionStore.setState({ sessions: null, activeSessionId: 's1' })
    const p1 = useSpecsStore.getState().fetchStatus('p1')
    // Session switches to s2 while s1's fetch is still in flight — the
    // refetch starts and stamps s2.
    useSessionStore.setState({ sessions: null, activeSessionId: 's2' })
    const p2 = useSpecsStore.getState().fetchStatus('p1')

    second.resolve(statusOf(true, 'p1'))
    await p2
    // The OLDER (previous-session) fetch resolves last — the seq ticket
    // must discard it, not resurrect s1's parse.
    first.resolve(statusOf(true, 'p1'))
    await p1
    await flush()

    const s = useSpecsStore.getState()
    expect(s.projectId).toBe('p1')
    expect(s.sessionId).toBe('s2')
    expect(s.status?.present).toBe(true)
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })
})

describe('specsStore — reset', () => {
  it('clears status, projectId, sessionId, error and isLoading', async () => {
    useSessionStore.setState({ sessions: null, activeSessionId: 's1' })
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')

    useSpecsStore.getState().reset()

    const s = useSpecsStore.getState()
    expect(s.status).toBeNull()
    expect(s.projectId).toBeNull()
    expect(s.sessionId).toBeNull()
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })
})

describe('specsStore — sidebar tree expansion (per-project, persisted)', () => {
  it('defaults to everything collapsed for every project', () => {
    seedProject('p1')
    expect(useSpecsStore.getState().treeExpandedNodes).toEqual({})
  })

  it('keys the expansion on the ACTIVE project and isolates projects', () => {
    seedProject('p1')
    useSpecsStore.getState().toggleTreeNode('domains')
    useSpecsStore.getState().toggleTreeNode('domains/frontend')
    expect(useSpecsStore.getState().treeExpandedNodes).toEqual({
      p1: { domains: true, 'domains/frontend': true },
    })

    // A second toggle collapses again (a plain boolean flip).
    useSpecsStore.getState().toggleTreeNode('domains')
    expect(useSpecsStore.getState().treeExpandedNodes.p1).toEqual({
      domains: false,
      'domains/frontend': true,
    })

    // Switching to p2 keys NEW toggles on p2 and leaves p1's set untouched.
    seedProject('p2')
    useSpecsStore.getState().toggleTreeNode('contracts')
    const s = useSpecsStore.getState().treeExpandedNodes
    expect(s.p2).toEqual({ contracts: true })
    expect(s.p1).toEqual({ domains: false, 'domains/frontend': true })
  })

  it('is a no-op without an active project', () => {
    seedProject(null)
    useSpecsStore.getState().toggleTreeNode('domains')
    expect(useSpecsStore.getState().treeExpandedNodes).toEqual({})
  })

  it('persists ONLY the expansion map (partialize outcome)', () => {
    seedProject('p1')
    useSpecsStore.getState().toggleTreeNode('domains')

    // The persisted snapshot carries the expansion map and NOTHING else —
    // a stale persisted parse must never resurrect, and the rest of the
    // store is session-scoped and stays out of localStorage.
    const partialize = useSpecsStore.persist.getOptions().partialize!
    const persisted = partialize(useSpecsStore.getState()) as Record<string, unknown>
    expect(persisted).toEqual({
      treeExpandedNodes: { p1: { domains: true } },
    })
  })

  it('reset restores the expansion defaults', () => {
    seedProject('p1')
    useSpecsStore.getState().toggleTreeNode('domains')
    useSpecsStore.getState().reset()
    expect(useSpecsStore.getState().treeExpandedNodes).toEqual({})
  })
})

describe('specsStore — framework context (sddStore projection)', () => {
  it('mirrors every successful parse into the framework context', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))

    await useSpecsStore.getState().fetchStatus('p1')

    const sdd = useSddStore.getState()
    expect(sdd.projectId).toBe('p1')
    expect(sdd.known).toBe(true)
    expect(sdd.present).toBe(true)
    expect(sdd.frameworks.map((fw) => fw.id)).toEqual(['sdd'])
    expect(sdd.activeId).toBe('sdd')
    expect(sdd.confirmedActiveId).toBe('sdd')
  })

  it('reports present=false (no detected framework) to the tab strip', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(false, 'p1'))

    await useSpecsStore.getState().fetchStatus('p1')

    expect(useSddStore.getState().present).toBe(false)
    expect(useSddStore.getState().frameworks).toEqual([])
  })

  it('reset clears the framework context with the parse', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')

    useSpecsStore.getState().reset()

    const sdd = useSddStore.getState()
    expect(sdd.known).toBe(false)
    expect(sdd.projectId).toBeNull()
    expect(sdd.frameworks).toEqual([])
    expect(sdd.activeId).toBe('')
  })
})

describe('specsStore — selectFramework', () => {
  it('persists the choice, then re-parses through the same fetch ticket', async () => {
    getSpecsStatusMock.mockResolvedValueOnce(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')
    // The re-parse reports the new selection: that is what settles the switch.
    getSpecsStatusMock.mockResolvedValueOnce({ ...statusOf(true, 'p1'), active_id: 'openspec' })
    setActiveSDDMock.mockResolvedValue(undefined)

    await useSpecsStore.getState().selectFramework('openspec')

    expect(setActiveSDDMock).toHaveBeenCalledWith('p1', 'openspec')
    // The switch re-uses fetchStatus: one newer flight, so its ticket wins.
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(2)
    expect(useSddStore.getState().switching).toBe(false)
    expect(useSddStore.getState().pendingActiveId).toBeNull()
  })

  it('shows the chosen framework at once while the previous tree stays rendered', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')
    const firstStatus = useSpecsStore.getState().status

    const persist = deferred<void>()
    setActiveSDDMock.mockReturnValue(persist.promise)
    const reparse = deferred<SpecsStatus>()
    getSpecsStatusMock.mockReturnValue(reparse.promise)

    const flight = useSpecsStore.getState().selectFramework('openspec')
    await flush()

    // While the persist is in flight: the selector already reports the
    // operator's choice, and the PREVIOUS parse is still on screen.
    expect(useSddStore.getState().activeId).toBe('openspec')
    expect(useSddStore.getState().switching).toBe(true)
    expect(useSpecsStore.getState().status).toBe(firstStatus)

    persist.resolve(undefined)
    await flush()
    // While the re-parse is in flight: still the previous tree (the store
    // keeps a same-(project, session) status).
    expect(useSpecsStore.getState().status).toBe(firstStatus)

    reparse.resolve({ ...statusOf(true, 'p1'), active_id: 'openspec' })
    await flight
    await flush()

    expect(useSddStore.getState().activeId).toBe('openspec')
    expect(useSddStore.getState().switching).toBe(false)
    expect(useSpecsStore.getState().status?.active_id).toBe('openspec')
  })

  it('reverts the optimistic choice when the persist fails', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')
    setActiveSDDMock.mockRejectedValue(new Error('SDD framework "nope" is not detected'))

    await useSpecsStore.getState().selectFramework('nope')

    const sdd = useSddStore.getState()
    expect(sdd.activeId).toBe('sdd') // the confirmed selection again
    expect(sdd.pendingActiveId).toBeNull()
    expect(sdd.switching).toBe(false)
    expect(sdd.error).toMatch(/not detected/)
    // The panel's own banner carries it too.
    expect(useSpecsStore.getState().error).toMatch(/not detected/)
    // A failed persist never re-parses.
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(1)
  })

  it('is a no-op without a project', async () => {
    await useSpecsStore.getState().selectFramework('openspec')
    expect(setActiveSDDMock).not.toHaveBeenCalled()
  })

  // Review 14: a persist that lands after the operator has moved to another
  // project must not fire a follow-up fetch for the OLD project — that would
  // clear the newer project's status mid-flight (its own fetch loses the LWW
  // ticket to the stale continuation's NEW fetch) and re-stamp the store and
  // the framework projection with abandoned data.
  it('does not refetch the old project when the persist lands after a project switch', async () => {
    getSpecsStatusMock.mockResolvedValueOnce(statusOf(true, 'p1'))
    seedProject('p1')
    await useSpecsStore.getState().fetchStatus('p1')

    const persist = deferred<void>()
    setActiveSDDMock.mockReturnValue(persist.promise)
    const flight = useSpecsStore.getState().selectFramework('openspec')
    await flush()

    // The operator moves on: switch to p2 and load ITS Specs surface while
    // the p1 persist is still in flight.
    seedProject('p2')
    const p2Status = statusOf(true, 'p2')
    getSpecsStatusMock.mockResolvedValueOnce(p2Status)
    await useSpecsStore.getState().fetchStatus('p2')

    // The abandoned p1 persist lands now.
    persist.resolve(undefined)
    await flight
    await flush()

    // Exactly the two parses that legitimately ran: the initial p1 parse and
    // the p2 parse — the stale continuation started no third flight.
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(2)
    expect(getSpecsStatusMock).toHaveBeenLastCalledWith('p2')
    // p2 owns the store AND the framework projection; p1's abandoned switch
    // left nothing behind (the p2 parse reset the context wholesale).
    expect(useSpecsStore.getState().status).toBe(p2Status)
    expect(useSpecsStore.getState().projectId).toBe('p2')
    expect(useSddStore.getState().projectId).toBe('p2')
    expect(useSddStore.getState().activeId).toBe('sdd')
    expect(useSddStore.getState().switching).toBe(false)
  })

  // Review 15: the switch is the persist + re-parse PAIR. A re-parse that
  // fails (transport error, or the framework vanished between persist and
  // parse) has still ENDED — the selector must unlock and the failure must
  // surface, never leave a disabled selector forever.
  it('unlocks the selector when the re-parse fails after a successful persist', async () => {
    getSpecsStatusMock.mockResolvedValueOnce(statusOf(true, 'p1'))
    seedProject('p1')
    await useSpecsStore.getState().fetchStatus('p1')
    setActiveSDDMock.mockResolvedValue(undefined)
    getSpecsStatusMock.mockRejectedValueOnce(new Error('reparse failed'))

    await useSpecsStore.getState().selectFramework('openspec')

    const sdd = useSddStore.getState()
    expect(sdd.switching).toBe(false)
    expect(sdd.pendingActiveId).toBeNull()
    // The persist DID land, so the selector keeps showing the saved choice.
    expect(sdd.activeId).toBe('openspec')
    // The refresh failure surfaces in the panel banner (the "selection
    // saved, refresh failed" report), not as a wedged selector.
    expect(useSpecsStore.getState().error).toMatch(/reparse failed/)
    expect(useSpecsStore.getState().isLoading).toBe(false)
  })

  // Review 15, second half: an authoritative payload whose active_id differs
  // from the pending choice (the backend fell back — the framework vanished)
  // must also END the switch: pending clears, the selector unlocks, and the
  // server's answer is recorded as the confirmed selection.
  it('settles the switch when the re-parse reports a different active framework', async () => {
    getSpecsStatusMock.mockResolvedValueOnce(statusOf(true, 'p1'))
    seedProject('p1')
    await useSpecsStore.getState().fetchStatus('p1')
    setActiveSDDMock.mockResolvedValue(undefined)
    // The parse lands with the backend's own fallback, not the operator's
    // pending choice.
    getSpecsStatusMock.mockResolvedValueOnce(statusOf(true, 'p1'))

    await useSpecsStore.getState().selectFramework('openspec')

    const sdd = useSddStore.getState()
    expect(sdd.switching).toBe(false)
    expect(sdd.pendingActiveId).toBeNull()
    expect(sdd.confirmedActiveId).toBe('sdd')
  })
})
