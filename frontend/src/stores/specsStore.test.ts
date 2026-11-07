// @vitest-environment jsdom
// (jsdom: the persist middleware reads window/localStorage on rehydration
// and the partialize outcome assertions need the runtime store.)
import { describe, it, expect, vi, beforeEach } from 'vitest'

const getSpecsStatusMock = vi.hoisted(() => vi.fn())

vi.mock('@/api/specs', () => ({
  getSpecsStatus: getSpecsStatusMock,
}))

import { useSpecsStore } from './specsStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from './sessionStore'
import type { SpecsStatus } from '@/types/models'

function statusOf(present: boolean, projectId: string): SpecsStatus {
  return {
    present,
    root: present ? `/ws/${projectId}/specs` : '',
    files: [],
    index_tasks: [],
    adrs: [],
    findings: [],
    counts: { files: 0, adrs: 0, findings: 0, findings_by_kind: {} },
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
