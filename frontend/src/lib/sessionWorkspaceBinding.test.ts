import { describe, it, expect } from 'vitest'
import { resolveManagedBinding } from './sessionWorkspaceBinding'
import type { ProjectInfo, SessionInfo } from '@/types/models'

function makeSession(overrides: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id: 's1',
    project_id: 'p1',
    name: 'Session',
    created_at: '2024-01-01T00:00:00Z',
    last_active_at: '2024-01-01T00:00:00Z',
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

function makeProject(overrides: Partial<ProjectInfo> = {}): ProjectInfo {
  return {
    id: 'p1',
    name: 'Project',
    workspace_path: '/ws/p1',
    is_external: false,
    is_no_project: false,
    created_at: '2024-01-01T00:00:00Z',
    last_active_at: '2024-01-01T00:00:00Z',
    ...overrides,
  }
}

const managedBinding = {
  kind: 'managed_worktree' as const,
  workspace_path: '/w/trees/s-1',
  worktree_name: 's-1',
  branch: 'feat/x',
}

const base = {
  activeProject: makeProject(),
  activeProjectId: 'p1',
  activeSessionId: 's1',
  sessions: [makeSession({ id: 's1', project_id: 'p1', workspace_binding: managedBinding })],
}

describe('resolveManagedBinding — the shared binding-first rule', () => {
  it('a managed-worktree session of the active project owns the root', () => {
    expect(resolveManagedBinding(base)).toEqual({ isManaged: true, bindingPath: '/w/trees/s-1' })
  })

  it('a managed binding whose path has not landed reports empty (caller awaits)', () => {
    const ctx = {
      ...base,
      sessions: [makeSession({
        id: 's1',
        project_id: 'p1',
        workspace_binding: { kind: 'managed_worktree' as const, workspace_path: '' },
      })],
    }
    expect(resolveManagedBinding(ctx)).toEqual({ isManaged: true, bindingPath: '' })
  })

  it('a foreign-project session never leaks its tree', () => {
    const ctx = { ...base, activeProject: makeProject({ id: 'p2' }), activeProjectId: 'p2' }
    expect(resolveManagedBinding(ctx)).toEqual({ isManaged: false, bindingPath: '' })
  })

  it('the No Project active project never takes a binding', () => {
    const ctx = { ...base, activeProject: makeProject({ id: '__no_project__', is_no_project: true }) }
    expect(resolveManagedBinding(ctx)).toEqual({ isManaged: false, bindingPath: '' })
  })

  it('a local binding is not managed', () => {
    const ctx = {
      ...base,
      sessions: [makeSession({
        id: 's1',
        project_id: 'p1',
        workspace_binding: { kind: 'local' as const, workspace_path: '/ws/p1' },
      })],
    }
    expect(resolveManagedBinding(ctx)).toEqual({ isManaged: false, bindingPath: '' })
  })

  it('a session missing from the list (or no session at all) is not managed', () => {
    expect(resolveManagedBinding({ ...base, sessions: [] })).toEqual({ isManaged: false, bindingPath: '' })
    expect(resolveManagedBinding({ ...base, sessions: null })).toEqual({ isManaged: false, bindingPath: '' })
    expect(resolveManagedBinding({ ...base, activeSessionId: null })).toEqual({ isManaged: false, bindingPath: '' })
  })
})
