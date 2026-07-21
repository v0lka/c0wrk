import { describe, it, expect } from 'vitest'
import { pickMostRecentRealProject } from './useProjectLoader'
import type { ProjectInfo } from '@/types/models'

function makeProject(overrides: Partial<ProjectInfo> & { id: string }): ProjectInfo {
  return {
    name: overrides.name ?? `Project ${overrides.id}`,
    workspace_path: overrides.workspace_path ?? '/tmp',
    is_external: overrides.is_external ?? false,
    is_no_project: overrides.is_no_project ?? false,
    created_at: overrides.created_at ?? '2026-01-01T00:00:00Z',
    last_active_at: overrides.last_active_at ?? '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

describe('pickMostRecentRealProject', () => {
  it('returns null for an empty list', () => {
    expect(pickMostRecentRealProject([])).toBeNull()
  })

  it('returns null when only No Project exists', () => {
    const projects = [makeProject({ id: 'no-project', is_no_project: true })]
    expect(pickMostRecentRealProject(projects)).toBeNull()
  })

  it('ignores No Project and returns the single real project', () => {
    const noProject = makeProject({ id: 'no-project', is_no_project: true })
    const real = makeProject({ id: 'real-1' })
    expect(pickMostRecentRealProject([noProject, real])).toEqual(real)
  })

  it('picks the most recently active real project', () => {
    const older = makeProject({ id: 'old', last_active_at: '2026-01-01T00:00:00Z' })
    const newer = makeProject({ id: 'new', last_active_at: '2026-06-01T00:00:00Z' })
    expect(pickMostRecentRealProject([older, newer])).toEqual(newer)
  })

  it('tie-breaks deterministically by timestamp regardless of input order', () => {
    const newer = makeProject({ id: 'new', last_active_at: '2026-06-01T00:00:00Z' })
    const older = makeProject({ id: 'old', last_active_at: '2026-01-01T00:00:00Z' })
    expect(pickMostRecentRealProject([newer, older])).toEqual(newer)
  })

  it('falls back to created_at when last_active_at is missing', () => {
    const a = makeProject({ id: 'a', last_active_at: '', created_at: '2026-01-01T00:00:00Z' })
    const b = makeProject({ id: 'b', last_active_at: '', created_at: '2026-05-01T00:00:00Z' })
    expect(pickMostRecentRealProject([a, b])).toEqual(b)
  })

  it('falls back to 0 for invalid timestamps without throwing', () => {
    const a = makeProject({ id: 'a', last_active_at: 'not-a-date', created_at: 'also-bad' })
    const b = makeProject({ id: 'b', last_active_at: '2026-05-01T00:00:00Z' })
    expect(pickMostRecentRealProject([a, b])).toEqual(b)
  })

  it('does not mutate the input array', () => {
    const projects = [
      makeProject({ id: 'old', last_active_at: '2026-01-01T00:00:00Z' }),
      makeProject({ id: 'new', last_active_at: '2026-06-01T00:00:00Z' }),
    ]
    pickMostRecentRealProject(projects)
    expect(projects.map((p) => p.id)).toEqual(['old', 'new'])
  })
})
