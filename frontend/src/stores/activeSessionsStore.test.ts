// Unit tests for stores/activeSessionsStore.ts — debounced refresh, error
// resilience, in-flight dedup, pending override.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

vi.mock('@/api/sessions', () => ({
  listAllSessions: vi.fn(),
}))
vi.mock('@/api/chat', () => ({
  getPendingActions: vi.fn(async () => null),
}))

import { listAllSessions } from '@/api/sessions'
import { getPendingActions } from '@/api/chat'
import {
  useActiveSessionsStore,
  cancelPendingRefresh,
  sweepPendingActions,
} from './activeSessionsStore'
import type { SessionInfo } from '@/types/models'

const mockedList = vi.mocked(listAllSessions)
const mockedGetPendingActions = vi.mocked(getPendingActions)

function makeSession(overrides: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id: 'sess-1',
    project_id: 'p1',
    name: 'Session',
    created_at: new Date(0).toISOString(),
    last_active_at: new Date(0).toISOString(),
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: '',
    family: '',
    has_unfinished_task: false,
    unfinished_task_status: '',
    ...overrides,
  }
}

function resetStore(): void {
  cancelPendingRefresh()
  useActiveSessionsStore.setState({ sessions: null, pendingOverride: {}, refreshing: false })
}

describe('activeSessionsStore', () => {
  beforeEach(() => {
    mockedList.mockReset()
    mockedGetPendingActions.mockReset()
    mockedGetPendingActions.mockResolvedValue(null)
    resetStore()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('starts with a null snapshot and no override', () => {
    const s = useActiveSessionsStore.getState()
    expect(s.sessions).toBeNull()
    expect(s.pendingOverride).toEqual({})
    expect(s.refreshing).toBe(false)
  })

  describe('refresh (debounced)', () => {
    it('coalesces rapid calls into a single RPC after the window', async () => {
      vi.useFakeTimers()
      mockedList.mockResolvedValue([])

      useActiveSessionsStore.getState().refresh()
      useActiveSessionsStore.getState().refresh()
      useActiveSessionsStore.getState().refresh()

      await vi.advanceTimersByTimeAsync(499)
      expect(mockedList).not.toHaveBeenCalled()

      await vi.advanceTimersByTimeAsync(1)
      expect(mockedList).toHaveBeenCalledTimes(1)
      expect(useActiveSessionsStore.getState().sessions).toEqual([])
    })

    it('restarts the window on each call (trailing debounce)', async () => {
      vi.useFakeTimers()
      mockedList.mockResolvedValue([])

      useActiveSessionsStore.getState().refresh()
      await vi.advanceTimersByTimeAsync(300)
      useActiveSessionsStore.getState().refresh()
      await vi.advanceTimersByTimeAsync(300)
      // 600 ms since the FIRST call, but only 300 since the last — not yet.
      expect(mockedList).not.toHaveBeenCalled()

      await vi.advanceTimersByTimeAsync(200)
      expect(mockedList).toHaveBeenCalledTimes(1)
    })

    it('cancelPendingRefresh drops a scheduled refresh', async () => {
      vi.useFakeTimers()
      mockedList.mockResolvedValue([])

      useActiveSessionsStore.getState().refresh()
      cancelPendingRefresh()
      await vi.advanceTimersByTimeAsync(1000)

      expect(mockedList).not.toHaveBeenCalled()
    })
  })

  describe('refreshNow (immediate)', () => {
    it('loads the snapshot immediately and tracks refreshing', async () => {
      const snapshot = [makeSession({ id: 'a' }), makeSession({ id: 'b' })]
      let resolveList!: (v: SessionInfo[]) => void
      mockedList.mockReturnValue(new Promise<SessionInfo[]>((r) => { resolveList = r }))

      const promise = useActiveSessionsStore.getState().refreshNow()
      expect(useActiveSessionsStore.getState().refreshing).toBe(true)

      resolveList(snapshot)
      await promise

      const s = useActiveSessionsStore.getState()
      expect(s.sessions).toEqual(snapshot)
      expect(s.refreshing).toBe(false)
    })

    it('an RPC error preserves the previous snapshot and never rejects', async () => {
      const previous = [makeSession({ id: 'keep-me' })]
      useActiveSessionsStore.setState({ sessions: previous })
      mockedList.mockRejectedValue(new Error('backend gone'))

      await expect(useActiveSessionsStore.getState().refreshNow()).resolves.toBeUndefined()

      const s = useActiveSessionsStore.getState()
      expect(s.sessions).toEqual(previous)
      expect(s.refreshing).toBe(false)
    })

    it('recovers on the next refresh after a failure', async () => {
      mockedList.mockRejectedValueOnce(new Error('transient'))
      await useActiveSessionsStore.getState().refreshNow()

      const fresh = [makeSession({ id: 'back' })]
      mockedList.mockResolvedValueOnce(fresh)
      await useActiveSessionsStore.getState().refreshNow()

      expect(useActiveSessionsStore.getState().sessions).toEqual(fresh)
    })

    it('deduplicates concurrent calls into one RPC', async () => {
      const snapshot = [makeSession()]
      let resolveList!: (v: SessionInfo[]) => void
      mockedList.mockImplementation(() => new Promise<SessionInfo[]>((r) => { resolveList = r }))

      const first = useActiveSessionsStore.getState().refreshNow()
      const second = useActiveSessionsStore.getState().refreshNow()
      resolveList(snapshot)
      await Promise.all([first, second])

      expect(mockedList).toHaveBeenCalledTimes(1)
      expect(useActiveSessionsStore.getState().sessions).toEqual(snapshot)
    })

    it('a debounced refresh following an in-flight one still runs', async () => {
      vi.useFakeTimers()
      let calls = 0
      let resolveFirst!: (v: SessionInfo[]) => void
      mockedList.mockImplementation(() => {
        calls++
        // Only the FIRST RPC is held; later ones resolve immediately so no
        // in-flight promise leaks into the next test.
        if (calls === 1) return new Promise<SessionInfo[]>((r) => { resolveFirst = r })
        return Promise.resolve([makeSession({ id: 'second' })])
      })

      const immediate = useActiveSessionsStore.getState().refreshNow()
      useActiveSessionsStore.getState().refresh() // scheduled behind the in-flight one
      resolveFirst([makeSession({ id: 'first' })])
      await immediate

      await vi.advanceTimersByTimeAsync(500)
      expect(mockedList).toHaveBeenCalledTimes(2)
      expect(useActiveSessionsStore.getState().sessions).toEqual([makeSession({ id: 'second' })])
    })
  })

  describe('pendingOverride', () => {
    it('setPendingOverride(true) records the bit and is idempotent', () => {
      const { setPendingOverride } = useActiveSessionsStore.getState()
      setPendingOverride('s1', true)
      const afterFirst = useActiveSessionsStore.getState().pendingOverride
      expect(afterFirst).toEqual({ s1: true })

      setPendingOverride('s1', true)
      expect(useActiveSessionsStore.getState().pendingOverride).toBe(afterFirst) // same reference
    })

    it('setPendingOverride(false) deletes the key', () => {
      useActiveSessionsStore.getState().setPendingOverride('s1', true)
      useActiveSessionsStore.getState().setPendingOverride('s1', false)
      expect(useActiveSessionsStore.getState().pendingOverride).toEqual({})
    })

    it('setPendingOverride(false) on an absent key is a no-op (same reference)', () => {
      const before = useActiveSessionsStore.getState().pendingOverride
      useActiveSessionsStore.getState().setPendingOverride('ghost', false)
      expect(useActiveSessionsStore.getState().pendingOverride).toBe(before)
    })

    it('applyPendingActions maps a response with prompts to true', () => {
      useActiveSessionsStore.getState().applyPendingActions('s1', {
        tool_confirms: [],
        step_limits: [],
        plan_approvals: [],
        ask_user: [{ request_id: 'r1', questions: [] }],
        goal_proposals: [],
      })
      expect(useActiveSessionsStore.getState().pendingOverride).toEqual({ s1: true })
    })

    it('applyPendingActions maps an empty response to false (key removed)', () => {
      useActiveSessionsStore.getState().setPendingOverride('s1', true)
      useActiveSessionsStore.getState().applyPendingActions('s1', {
        tool_confirms: [],
        step_limits: [],
        plan_approvals: [],
        ask_user: [],
        goal_proposals: [],
      })
      expect(useActiveSessionsStore.getState().pendingOverride).toEqual({})
    })

    it('applyPendingActions maps a null response (RPC failure) to false', () => {
      useActiveSessionsStore.getState().setPendingOverride('s1', true)
      useActiveSessionsStore.getState().applyPendingActions('s1', null)
      expect(useActiveSessionsStore.getState().pendingOverride).toEqual({})
    })
  })

  describe('sweepPendingActions', () => {
    it('queries only live sessions whose pending state is unknown', async () => {
      useActiveSessionsStore.setState({
        sessions: [
          makeSession({ id: 'unknown', unfinished_task_status: 'in_progress' }),
          makeSession({ id: 'known', unfinished_task_status: 'in_progress' }),
          makeSession({ id: 'idle' }),
        ],
        pendingOverride: { known: true },
      })

      await sweepPendingActions()

      expect(mockedGetPendingActions).toHaveBeenCalledTimes(1)
      expect(mockedGetPendingActions).toHaveBeenCalledWith('unknown')
    })

    it('applies a pending response as the override for that session', async () => {
      useActiveSessionsStore.setState({
        sessions: [makeSession({ id: 's1', unfinished_task_status: 'in_progress' })],
      })
      mockedGetPendingActions.mockResolvedValue({
        tool_confirms: [],
        step_limits: [],
        plan_approvals: [],
        ask_user: [{ request_id: 'r1', questions: [] }],
        goal_proposals: [],
      })

      await sweepPendingActions()

      expect(useActiveSessionsStore.getState().pendingOverride).toEqual({ s1: true })
    })
  })

  describe('selector stability (React #185)', () => {
    it('state fields are references that only change on real updates', async () => {
      const snapshot = [makeSession()]
      mockedList.mockResolvedValue(snapshot)

      await useActiveSessionsStore.getState().refreshNow()
      const s1 = useActiveSessionsStore.getState()
      const sessionsRef = s1.sessions
      const overrideRef = s1.pendingOverride

      // Unrelated update (pendingOverride) must not touch the sessions ref.
      s1.setPendingOverride('x', true)
      const s2 = useActiveSessionsStore.getState()
      expect(s2.sessions).toBe(sessionsRef)
      expect(s2.pendingOverride).not.toBe(overrideRef)

      // A no-op override update must not allocate a new record either.
      const overrideRef2 = s2.pendingOverride
      s2.setPendingOverride('x', true)
      expect(useActiveSessionsStore.getState().pendingOverride).toBe(overrideRef2)
    })
  })
})
