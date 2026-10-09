// Boundary test for projectStore.selectTitleScope (#186): the selector runs
// at the app root (useWindowTitle → App shell render), so a non-string active
// project name must degrade to "no title segment" instead of throwing
// `name.trim is not a function` during the shell render.

import { describe, expect, it } from 'vitest'
import { selectTitleScope, CHAT_LABEL, type ProjectState } from './projectStore'
import type { ProjectInfo } from '@/types/models'

const project = (overrides: Partial<ProjectInfo> & { id: string }): ProjectInfo => ({
  name: 'Demo',
  workspace_path: '/ws',
  is_external: false,
  is_no_project: false,
  created_at: '2026-01-01T00:00:00Z',
  last_active_at: '2026-01-01T00:00:00Z',
  ...overrides,
})

const stateWith = (projects: ProjectInfo[], activeProjectId: string | null): ProjectState => ({
  projects,
  activeProjectId,
  lastRealProjectId: null,
  createDialogOpen: false,
})

describe('selectTitleScope (#186 name.trim hardening)', () => {
  it('returns the trimmed name of the active project', () => {
    const state = stateWith([project({ id: 'p1', name: '  Demo  ' })], 'p1')
    expect(selectTitleScope(state)).toBe('Demo')
  })

  it('returns CHAT_LABEL for the No Project pseudo-project', () => {
    const state = stateWith([project({ id: 'p0', name: 'No Project', is_no_project: true })], 'p0')
    expect(selectTitleScope(state)).toBe(CHAT_LABEL)
  })

  it('returns null while projects are loading or nothing is active', () => {
    expect(selectTitleScope(stateWith([], null))).toBeNull()
    expect(selectTitleScope({ ...stateWith([], 'p1'), projects: null })).toBeNull()
    expect(selectTitleScope(stateWith([], 'missing'))).toBeNull()
  })

  it('returns null for an empty/whitespace name', () => {
    expect(selectTitleScope(stateWith([project({ id: 'p1', name: '   ' })], 'p1'))).toBeNull()
  })

  it('degrades to null instead of throwing when the active name is not a string', () => {
    const state = stateWith([project({ id: 'p1', name: { text: 'x' } as unknown as string })], 'p1')
    expect(() => selectTitleScope(state)).not.toThrow()
    expect(selectTitleScope(state)).toBeNull()
  })
})
