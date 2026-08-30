import { describe, it, expect } from 'vitest'
import { pickLatestRestorableSession, resolveRestoreSession } from './sessionRestore'
import type { SessionInfo } from '@/types/models'

function makeSession(overrides: Partial<SessionInfo> & { id: string }): SessionInfo {
  return {
    id: overrides.id,
    project_id: overrides.project_id ?? 'project-1',
    name: overrides.name ?? `Session ${overrides.id}`,
    created_at: overrides.created_at ?? '2026-01-01T00:00:00Z',
    last_active_at: overrides.last_active_at ?? '2026-01-01T00:00:00Z',
    archived: overrides.archived ?? false,
    pinned: overrides.pinned ?? false,
    active: overrides.active ?? false,
    total_input_tokens: overrides.total_input_tokens ?? 0,
    total_output_tokens: overrides.total_output_tokens ?? 0,
    model: overrides.model ?? '',
    family: overrides.family ?? '',
    has_unfinished_task: overrides.has_unfinished_task ?? false,
  }
}

describe('pickLatestRestorableSession', () => {
  it('returns null for an empty list', () => {
    expect(pickLatestRestorableSession([])).toBeNull()
  })

  it('picks the session with the freshest activity regardless of list order', () => {
    const sessions = [
      makeSession({ id: 'older', last_active_at: '2026-01-01T00:00:00Z' }),
      makeSession({ id: 'freshest', last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'middle', last_active_at: '2026-03-01T00:00:00Z' }),
    ]
    expect(pickLatestRestorableSession(sessions)?.id).toBe('freshest')
  })

  it('never picks an archived session, even with the freshest activity', () => {
    const sessions = [
      makeSession({ id: 'archived-fresh', archived: true, last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'active-stale', last_active_at: '2026-01-01T00:00:00Z' }),
    ]
    expect(pickLatestRestorableSession(sessions)?.id).toBe('active-stale')
  })

  it('returns null when every session is archived', () => {
    const sessions = [
      makeSession({ id: 'a1', archived: true, last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'a2', archived: true, last_active_at: '2026-03-01T00:00:00Z' }),
    ]
    expect(pickLatestRestorableSession(sessions)).toBeNull()
  })

  it('falls back to created_at when last_active_at is empty', () => {
    const sessions = [
      makeSession({ id: 'no-activity', last_active_at: '', created_at: '2026-05-01T00:00:00Z' }),
      makeSession({ id: 'has-activity', last_active_at: '2026-02-01T00:00:00Z' }),
    ]
    expect(pickLatestRestorableSession(sessions)?.id).toBe('no-activity')
  })
})

describe('resolveRestoreSession', () => {
  it('restores the saved session even when another session is fresher', () => {
    const sessions = [
      makeSession({ id: 'freshest', last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'saved', last_active_at: '2026-01-01T00:00:00Z' }),
    ]
    expect(resolveRestoreSession(sessions, 'saved')?.id).toBe('saved')
  })

  it('falls back to the latest non-archived session when the saved one was deleted', () => {
    const sessions = [
      makeSession({ id: 'older', last_active_at: '2026-01-01T00:00:00Z' }),
      makeSession({ id: 'freshest', last_active_at: '2026-06-01T00:00:00Z' }),
    ]
    expect(resolveRestoreSession(sessions, 'deleted-saved')?.id).toBe('freshest')
  })

  it('falls back to the latest non-archived session when the saved one is archived', () => {
    const sessions = [
      makeSession({ id: 'saved-archived', archived: true, last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'active', last_active_at: '2026-01-01T00:00:00Z' }),
    ]
    expect(resolveRestoreSession(sessions, 'saved-archived')?.id).toBe('active')
  })

  it('picks the latest non-archived session when no saved id exists', () => {
    const sessions = [
      makeSession({ id: 'archived-fresh', archived: true, last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'active-stale', last_active_at: '2026-01-01T00:00:00Z' }),
    ]
    expect(resolveRestoreSession(sessions, '')?.id).toBe('active-stale')
  })

  it('returns null when nothing is restorable (all archived or empty)', () => {
    expect(resolveRestoreSession([], '')).toBeNull()
    expect(resolveRestoreSession([makeSession({ id: 'a', archived: true })], '')).toBeNull()
    expect(resolveRestoreSession([makeSession({ id: 'a', archived: true })], 'a')).toBeNull()
  })
})
