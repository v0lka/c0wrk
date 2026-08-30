import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { saveProjectActiveSession } from '@/api/projects'
import { logger } from '@/lib/logger'
import type { SessionInfo } from '@/types/models'

// The persist RPC is mocked so the store is tested in isolation (same pattern
// as workDirsStore.test.ts).
vi.mock('@/api/projects', () => ({
  saveProjectActiveSession: vi.fn(),
}))

const mockedSave = vi.mocked(saveProjectActiveSession)

function makeSession(overrides: Partial<SessionInfo> & { id: string }): SessionInfo {
  return {
    project_id: 'proj-1',
    name: `Session ${overrides.id}`,
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: '',
    family: '',
    has_unfinished_task: false,
    ...overrides,
  }
}

function resetStore() {
  useSessionStore.setState({ sessions: null, activeSessionId: null })
}

describe('sessionStore sorting', () => {
  beforeEach(resetStore)

  it('sorts pinned sessions to the top, regardless of activity', () => {
    // An older pinned session and a newer unpinned one: pinned must win.
    const sessions = [
      makeSession({ id: 'newer-unpinned', last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'older-pinned', pinned: true, last_active_at: '2026-01-01T00:00:00Z' }),
    ]
    useSessionStore.getState().setSessions(sessions)
    const sorted = useSessionStore.getState().sessions!
    expect(sorted.map((s) => s.id)).toEqual(['older-pinned', 'newer-unpinned'])
  })

  it('keeps pinned-first order stable when activity updates', () => {
    // setSessions dedupes by id list when unchanged; use distinct ids and
    // verify ordering via the returned store after a touch.
    useSessionStore.getState().setSessions([
      makeSession({ id: 'a', pinned: true, last_active_at: '2026-01-01T00:00:00Z' }),
      makeSession({ id: 'b', pinned: false, last_active_at: '2026-09-01T00:00:00Z' }),
      makeSession({ id: 'c', pinned: true, last_active_at: '2026-05-01T00:00:00Z' }),
    ])
    // Pinned (a, c) come before unpinned (b); within pinned, by activity desc (c then a).
    expect(useSessionStore.getState().sessions!.map((s) => s.id)).toEqual(['c', 'a', 'b'])
  })

  it('orders unpinned sessions by last_active_at desc when none are pinned', () => {
    useSessionStore.getState().setSessions([
      makeSession({ id: 'old', last_active_at: '2026-01-01T00:00:00Z' }),
      makeSession({ id: 'new', last_active_at: '2026-12-01T00:00:00Z' }),
    ])
    expect(useSessionStore.getState().sessions!.map((s) => s.id)).toEqual(['new', 'old'])
  })

  it('keeps pinned on top after updateSession toggles a pin', () => {
    useSessionStore.getState().setSessions([
      makeSession({ id: 'x', last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'y', last_active_at: '2026-01-01T00:00:00Z' }),
    ])
    // Pin the older session: it should jump to the top.
    useSessionStore.getState().updateSession('y', { pinned: true })
    expect(useSessionStore.getState().sessions!.map((s) => s.id)).toEqual(['y', 'x'])
  })
})

describe('setUnfinishedTask', () => {
  beforeEach(resetStore)

  it('clears the flag for the matching session and leaves others untouched', () => {
    // The runtime reconcile / terminal task events push the authoritative
    // value through here so isSessionBusy() stays truthful without a restart.
    useSessionStore.getState().setSessions([
      makeSession({ id: 'a', has_unfinished_task: true }),
      makeSession({ id: 'b', has_unfinished_task: true }),
    ])

    useSessionStore.getState().setUnfinishedTask('a', false)

    const sessions = useSessionStore.getState().sessions!
    expect(sessions.find((s) => s.id === 'a')!.has_unfinished_task).toBe(false)
    expect(sessions.find((s) => s.id === 'b')!.has_unfinished_task).toBe(true)
  })

  it('sets the flag true when a task becomes resumable', () => {
    useSessionStore.getState().setSessions([makeSession({ id: 'a' })])

    useSessionStore.getState().setUnfinishedTask('a', true)

    expect(useSessionStore.getState().sessions![0]!.has_unfinished_task).toBe(true)
  })

  it('keeps the sessions reference when the value already matches (stable selectors)', () => {
    // A new array on a no-op update would needlessly re-render every
    // subscriber of `sessions` — the guard must return the same reference.
    useSessionStore.getState().setSessions([makeSession({ id: 'a', has_unfinished_task: true })])
    const before = useSessionStore.getState().sessions

    useSessionStore.getState().setUnfinishedTask('a', true)

    expect(useSessionStore.getState().sessions).toBe(before)
  })

  it('no-ops for an unknown session id', () => {
    useSessionStore.getState().setSessions([makeSession({ id: 'a' })])
    const before = useSessionStore.getState().sessions

    useSessionStore.getState().setUnfinishedTask('missing', false)

    expect(useSessionStore.getState().sessions).toBe(before)
  })

  it('no-ops before the session list is loaded', () => {
    useSessionStore.getState().setUnfinishedTask('a', true)
    expect(useSessionStore.getState().sessions).toBeNull()
  })
})

describe('selectSession', () => {
  beforeEach(() => {
    resetStore()
    useProjectStore.setState({
      projects: null,
      activeProjectId: null,
      lastRealProjectId: null,
      createDialogOpen: false,
    })
    vi.clearAllMocks()
    mockedSave.mockResolvedValue(undefined)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('persists the user selection as the project saved session (click path)', () => {
    useSessionStore.getState().selectSession('s1', 'proj-1')

    // Selection applies synchronously AND is persisted under the passed
    // owning project id so saved_session_id is authoritative by switch/exit
    // time.
    expect(useSessionStore.getState().activeSessionId).toBe('s1')
    expect(mockedSave).toHaveBeenCalledTimes(1)
    expect(mockedSave).toHaveBeenCalledWith('proj-1', 's1')
  })

  it('persists under the passed owning project, not the global active project', () => {
    // Mid-switch the global activeProjectId may already point at the
    // destination project while the visible list still shows the source
    // project's sessions — the session's own project_id must win.
    useProjectStore.getState().setActiveProjectId('proj-destination')

    useSessionStore.getState().selectSession('s1', 'proj-source')

    expect(mockedSave).toHaveBeenCalledWith('proj-source', 's1')
  })

  it('does not persist on programmatic setActiveSessionId (restore paths)', () => {
    useProjectStore.getState().setActiveProjectId('proj-1')

    useSessionStore.getState().setActiveSessionId('s2')

    // performSwitch / useSessionLoader apply an already-persisted value —
    // echoing it back would race the switch-time snapshot.
    expect(useSessionStore.getState().activeSessionId).toBe('s2')
    expect(mockedSave).not.toHaveBeenCalled()
  })

  it('logs a warning and keeps the selection when persisting fails', async () => {
    mockedSave.mockRejectedValue(new Error('rpc down'))
    const warnSpy = vi.spyOn(logger, 'warn').mockImplementation(() => {})

    useSessionStore.getState().selectSession('s1', 'proj-1')

    // The failed persist must not break selection: activeSessionId is already
    // updated synchronously; the rejection surfaces only as a warning.
    expect(useSessionStore.getState().activeSessionId).toBe('s1')
    await vi.waitFor(() => {
      expect(warnSpy).toHaveBeenCalledTimes(1)
    })
  })

  it('does not persist when the selection is cleared (id = null)', () => {
    useSessionStore.getState().selectSession(null)

    expect(useSessionStore.getState().activeSessionId).toBeNull()
    expect(mockedSave).not.toHaveBeenCalled()
  })

  it('does not persist when the owning project id is missing', () => {
    const warnSpy = vi.spyOn(logger, 'warn').mockImplementation(() => {})

    useSessionStore.getState().selectSession('s1')

    expect(useSessionStore.getState().activeSessionId).toBe('s1')
    expect(mockedSave).not.toHaveBeenCalled()
    expect(warnSpy).toHaveBeenCalledTimes(1)
  })
})
