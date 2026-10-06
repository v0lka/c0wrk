// Lifecycle tests for createSessionFromDraft (lib/sessionDraft.ts) — the
// single draft-commit path shared by send / attachment / paste / terminal.
//
// Covers the AC matrix: no session exists until the first action commits the
// draft; the local path creates a plain session; the branch path provisions
// through CreateManagedSession with the drafted arguments; failure keeps the
// draft armed; success adds + selects + clears; a stale cross-project draft
// is ignored and retired.
// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'
import type { Mock } from 'vitest'
import type { ProjectInfo, SessionInfo } from '@/types/models'
import { createSessionFromDraft } from '@/lib/sessionDraft'
import { useSessionDraftStore } from '@/stores/sessionDraftStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { createSession, createManagedSession } from '@/api/sessions'
import { logger } from '@/lib/logger'

vi.mock('@/api/sessions', () => ({
  createSession: vi.fn(),
  createManagedSession: vi.fn(),
}))
vi.mock('@/api/projects', () => ({
  saveProjectActiveSession: vi.fn(() => Promise.resolve()),
}))

const createSessionMock = createSession as unknown as Mock
const createManagedSessionMock = createManagedSession as unknown as Mock

function makeSession(overrides: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id: 's-1',
    project_id: 'proj-1',
    name: 'Session',
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: 'm',
    family: 'f',
    has_unfinished_task: false,
    ...overrides,
  }
}

function makeProject(overrides: Partial<ProjectInfo> = {}): ProjectInfo {
  return {
    id: 'proj-1',
    name: 'Proj',
    workspace_path: '/w/proj',
    is_external: false,
    is_no_project: false,
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function resetStores() {
  useSessionDraftStore.getState().clearDraft()
  useSessionDraftStore.getState().setCommitting(false)
  useSessionStore.setState({ sessions: null, activeSessionId: null })
  useProjectStore.setState({ activeProjectId: 'proj-1', projects: [makeProject()] })
  createSessionMock.mockReset()
  createManagedSessionMock.mockReset()
}

describe('createSessionFromDraft', () => {
  beforeEach(() => {
    resetStores()
  })

  it('no draft: creates a plain local session, selects it, stays draft-free', async () => {
    createSessionMock.mockResolvedValue(makeSession())
    const session = await createSessionFromDraft()
    expect(createSessionMock).toHaveBeenCalledOnce()
    expect(createManagedSessionMock).not.toHaveBeenCalled()
    expect(useSessionStore.getState().activeSessionId).toBe(session.id)
    expect(useSessionStore.getState().sessions?.some((s) => s.id === session.id)).toBe(true)
    expect(useSessionDraftStore.getState().draft).toBeNull()
  })

  it('local draft: creates a plain session (workspace kind local)', async () => {
    useSessionDraftStore.getState().startDraft('proj-1')
    createSessionMock.mockResolvedValue(makeSession())
    await createSessionFromDraft()
    expect(createSessionMock).toHaveBeenCalledOnce()
    expect(createManagedSessionMock).not.toHaveBeenCalled()
    expect(useSessionDraftStore.getState().draft).toBeNull()
  })

  it('branch draft: provisions via CreateManagedSession with the drafted args', async () => {
    useSessionDraftStore.getState().startDraft('proj-1')
    useSessionDraftStore
      .getState()
      .setDraftWorkspace({ kind: 'branch', branch: 'feat/x', createBranch: false, startPoint: '' })
    const managed = makeSession({
      id: 's-mgd',
      workspace_binding: { kind: 'managed_worktree', workspace_path: '/w/proj/.worktrees/s-abcd1234', worktree_name: 's-abcd1234', branch: 'feat/x' },
    })
    createManagedSessionMock.mockResolvedValue(managed)

    const session = await createSessionFromDraft()

    expect(createManagedSessionMock).toHaveBeenCalledOnce()
    expect(createManagedSessionMock).toHaveBeenCalledWith('feat/x', false, '')
    expect(createSessionMock).not.toHaveBeenCalled()
    expect(useSessionStore.getState().activeSessionId).toBe('s-mgd')
    expect(useSessionDraftStore.getState().draft).toBeNull()
    expect(session.id).toBe('s-mgd')
  })

  it('new-branch draft: passes createBranch=true and the start point', async () => {
    useSessionDraftStore.getState().startDraft('proj-1')
    useSessionDraftStore
      .getState()
      .setDraftWorkspace({ kind: 'branch', branch: 'feat/new', createBranch: true, startPoint: 'main' })
    createManagedSessionMock.mockResolvedValue(makeSession({ id: 's-mgd2' }))

    await createSessionFromDraft()

    expect(createManagedSessionMock).toHaveBeenCalledWith('feat/new', true, 'main')
  })

  it('provisioning failure keeps the draft armed for retry', async () => {
    // The failure path logs one error at its source — assert it (severity +
    // payload + count) so the run stays diagnostic-free while the behavior
    // under test stays observable.
    const errorSpy = vi.spyOn(logger, 'error').mockImplementation(() => {})
    useSessionDraftStore.getState().startDraft('proj-1')
    useSessionDraftStore
      .getState()
      .setDraftWorkspace({ kind: 'branch', branch: 'feat/x', createBranch: false, startPoint: '' })
    createManagedSessionMock.mockRejectedValue(new Error('branch already checked out'))

    await expect(createSessionFromDraft()).rejects.toThrow('branch already checked out')
    expect(errorSpy).toHaveBeenCalledOnce()
    expect(errorSpy).toHaveBeenCalledWith(
      'Failed to create session from draft:',
      expect.objectContaining({ message: 'branch already checked out' }),
    )
    errorSpy.mockRestore()

    // No session materialized and the draft survived for a re-pick.
    expect(useSessionStore.getState().activeSessionId).toBeNull()
    expect(useSessionDraftStore.getState().draft?.workspace).toEqual({
      kind: 'branch',
      branch: 'feat/x',
      createBranch: false,
      startPoint: '',
    })
    expect(useSessionDraftStore.getState().committing).toBe(false)
  })

  it('a draft from another project is ignored and retired', async () => {
    useSessionDraftStore.getState().startDraft('proj-other')
    useSessionDraftStore
      .getState()
      .setDraftWorkspace({ kind: 'branch', branch: 'feat/z', createBranch: false, startPoint: '' })
    createSessionMock.mockResolvedValue(makeSession())

    await createSessionFromDraft()

    expect(createManagedSessionMock).not.toHaveBeenCalled()
    expect(createSessionMock).toHaveBeenCalledOnce()
    expect(useSessionDraftStore.getState().draft).toBeNull()
  })

  it('toggles committing around the RPC window', async () => {
    let resolveCreation: (s: SessionInfo) => void = () => {}
    createSessionMock.mockReturnValue(
      new Promise<SessionInfo>((resolve) => {
        resolveCreation = resolve
      }),
    )
    const pending = createSessionFromDraft()
    expect(useSessionDraftStore.getState().committing).toBe(true)
    resolveCreation(makeSession())
    await pending
    expect(useSessionDraftStore.getState().committing).toBe(false)
  })
})
