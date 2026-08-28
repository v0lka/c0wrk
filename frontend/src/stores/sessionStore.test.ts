import { describe, it, expect, beforeEach } from 'vitest'
import { useSessionStore } from '@/stores/sessionStore'
import type { SessionInfo } from '@/types/models'

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
