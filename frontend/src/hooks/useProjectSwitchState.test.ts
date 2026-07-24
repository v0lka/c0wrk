import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { ProjectSwitchState, SessionInfo } from '@/types/models'

const mocks = vi.hoisted(() => ({
  saveProjectSwitchStateMock: vi.fn<(...args: unknown[]) => Promise<void>>(),
  switchProjectMock: vi.fn<(...args: unknown[]) => Promise<void>>(),
  getProjectSwitchStateMock: vi.fn<(...args: unknown[]) => Promise<ProjectSwitchState | null>>(),
  listSessionsMock: vi.fn<(...args: unknown[]) => Promise<SessionInfo[]>>(),
  createSessionMock: vi.fn<(...args: unknown[]) => Promise<SessionInfo>>(),
}))

vi.mock('react', () => ({
  useCallback: <T extends (...args: unknown[]) => unknown>(fn: T) => fn,
}))

vi.mock('@/api/projects', () => ({
  saveProjectSwitchState: mocks.saveProjectSwitchStateMock,
  switchProject: mocks.switchProjectMock,
  getProjectSwitchState: mocks.getProjectSwitchStateMock,
}))

vi.mock('@/api/sessions', () => ({
  listSessions: mocks.listSessionsMock,
  createSession: mocks.createSessionMock,
}))

function makeSession(overrides: Partial<SessionInfo> & { id: string; project_id: string }): SessionInfo {
  return {
    id: overrides.id,
    project_id: overrides.project_id,
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

function resetStores() {
  useProjectStore.setState({ projects: null, activeProjectId: null })
  useSessionStore.setState({ sessions: null, activeSessionId: null })
  useFileViewerStore.setState({
    files: {},
    openTabs: [],
    activeFile: null,
    width: 500,
    collapsed: false,
    highlightLine: null,
    fileIcons: {},
  })
}

describe('useProjectSwitchState', () => {
  beforeEach(() => {
    resetStores()
    mocks.saveProjectSwitchStateMock.mockReset()
    mocks.switchProjectMock.mockReset()
    mocks.getProjectSwitchStateMock.mockReset()
    mocks.listSessionsMock.mockReset()
    mocks.createSessionMock.mockReset()

    mocks.saveProjectSwitchStateMock.mockResolvedValue(undefined)
    mocks.switchProjectMock.mockResolvedValue(undefined)
    mocks.getProjectSwitchStateMock.mockResolvedValue(null)
    mocks.listSessionsMock.mockResolvedValue([])
    mocks.createSessionMock.mockResolvedValue(makeSession({
      id: 'created-1',
      project_id: 'target-project',
      last_active_at: '2026-02-01T00:00:00Z',
    }))
  })

  it('saves source project state before switching and restores destination open files + saved session', async () => {
    useProjectStore.getState().setActiveProjectId('source-project')
    useSessionStore.getState().setActiveSessionId('source-session')
    useFileViewerStore.getState().restoreProjectFiles(['src/a.ts', 'src/b.ts'], 'src/b.ts')

    mocks.getProjectSwitchStateMock.mockResolvedValue({
      project_id: 'target-project',
      saved_session_id: 'target-saved-session',
      open_tabs: ['dest/readme.md', 'dest/main.go'],
      active_file: 'dest/main.go',
      updated_at: '2026-03-01T00:00:00Z',
    })

    mocks.listSessionsMock.mockResolvedValue([
      makeSession({ id: 'target-saved-session', project_id: 'target-project', last_active_at: '2026-03-01T00:00:00Z' }),
      makeSession({ id: 'other-session', project_id: 'target-project', last_active_at: '2026-02-01T00:00:00Z' }),
    ])

    const { useProjectSwitchState } = await import('@/hooks/useProjectSwitchState')
    const runSwitch = useProjectSwitchState()
    await runSwitch('target-project')

    expect(mocks.saveProjectSwitchStateMock).toHaveBeenCalledTimes(1)
    expect(mocks.saveProjectSwitchStateMock).toHaveBeenCalledWith({
      project_id: 'source-project',
      open_tabs: ['src/a.ts', 'src/b.ts'],
      active_file: 'src/b.ts',
      saved_session_id: 'source-session',
    })

    expect(mocks.switchProjectMock).toHaveBeenCalledWith('target-project')
    expect(mocks.getProjectSwitchStateMock).toHaveBeenCalledWith('target-project')

    const fileState = useFileViewerStore.getState()
    expect(fileState.openTabs).toEqual(['dest/readme.md', 'dest/main.go'])
    expect(fileState.activeFile).toBe('dest/main.go')

    const sessionState = useSessionStore.getState()
    expect(sessionState.activeSessionId).toBe('target-saved-session')
    expect(sessionState.sessions?.map((s) => s.id)).toEqual(['target-saved-session', 'other-session'])
    expect(mocks.createSessionMock).not.toHaveBeenCalled()
    expect(useProjectStore.getState().activeProjectId).toBe('target-project')
  })

  it('falls back to latest session when saved session is absent from destination list', async () => {
    useProjectStore.getState().setActiveProjectId('source-project')

    mocks.getProjectSwitchStateMock.mockResolvedValue({
      project_id: 'target-project',
      saved_session_id: 'missing-saved-session',
      open_tabs: ['dest/one.ts'],
      active_file: 'dest/one.ts',
      updated_at: '2026-03-02T00:00:00Z',
    })

    mocks.listSessionsMock.mockResolvedValue([
      makeSession({ id: 'older-session', project_id: 'target-project', last_active_at: '2026-01-01T00:00:00Z' }),
      makeSession({ id: 'latest-session', project_id: 'target-project', last_active_at: '2026-06-01T10:00:00Z' }),
    ])

    const { useProjectSwitchState } = await import('@/hooks/useProjectSwitchState')
    const runSwitch = useProjectSwitchState()
    await runSwitch('target-project')

    expect(useSessionStore.getState().activeSessionId).toBe('latest-session')
    expect(mocks.createSessionMock).not.toHaveBeenCalled()
  })

  it('creates a new session when destination project has no sessions', async () => {
    useProjectStore.getState().setActiveProjectId('source-project')

    mocks.getProjectSwitchStateMock.mockResolvedValue({
      project_id: 'target-project',
      saved_session_id: '',
      open_tabs: ['dest/new.ts'],
      active_file: 'dest/new.ts',
      updated_at: '2026-03-03T00:00:00Z',
    })
    mocks.listSessionsMock.mockResolvedValue([])
    mocks.createSessionMock.mockResolvedValue(makeSession({
      id: 'created-session',
      project_id: 'target-project',
      last_active_at: '2026-03-03T01:00:00Z',
    }))

    const { useProjectSwitchState } = await import('@/hooks/useProjectSwitchState')
    const runSwitch = useProjectSwitchState()
    await runSwitch('target-project')

    expect(mocks.createSessionMock).toHaveBeenCalledTimes(1)
    expect(useSessionStore.getState().activeSessionId).toBe('created-session')
    expect(useSessionStore.getState().sessions?.map((s) => s.id)).toContain('created-session')
  })

  it('prefers latest session by activity when saved session exists but is stale', async () => {
    // Simulates the restart scenario: savedSessionId is stale (saved during a
    // previous app session), but newer sessions were created afterward with
    // more recent last_active_at timestamps.
    useProjectStore.getState().setActiveProjectId('old-project')

    mocks.getProjectSwitchStateMock.mockResolvedValue({
      project_id: 'target-project',
      saved_session_id: 'stale-saved-session', // was active 4 sessions ago
      open_tabs: ['dest/stale.ts'],
      active_file: 'dest/stale.ts',
      updated_at: '2026-03-01T00:00:00Z',
    })

    mocks.listSessionsMock.mockResolvedValue([
      makeSession({ id: 'session-1', project_id: 'target-project', last_active_at: '2026-03-05T00:00:00Z' }),
      makeSession({ id: 'session-2', project_id: 'target-project', last_active_at: '2026-03-04T00:00:00Z' }),
      makeSession({ id: 'session-3', project_id: 'target-project', last_active_at: '2026-03-03T00:00:00Z' }),
      makeSession({ id: 'stale-saved-session', project_id: 'target-project', last_active_at: '2026-03-02T00:00:00Z' }),
      makeSession({ id: 'session-5', project_id: 'target-project', last_active_at: '2026-03-01T00:00:00Z' }),
    ])

    const { useProjectSwitchState } = await import('@/hooks/useProjectSwitchState')
    const runSwitch = useProjectSwitchState()
    await runSwitch('target-project')

    // Must pick the most recently active session (session-1), NOT the stale saved one.
    const sessionState = useSessionStore.getState()
    expect(sessionState.activeSessionId).toBe('session-1')
    expect(mocks.createSessionMock).not.toHaveBeenCalled()
  })

  it('does not restore stale backend open tabs on initial (startup) activation', async () => {
    // Reproduces the persistence bug: on app startup loadAndActivate calls
    // switchProjectWithState with NO previously active project. The backend
    // per-project switch state is stale on restart (it is only persisted when
    // switching *away* from a project), so it may still reference tabs the
    // user dismissed — e.g. a plan that was opened then closed. The file-viewer
    // store was already rehydrated from localStorage with the tabs that were
    // actually open at shutdown; the backend tabs must NOT overwrite them.
    useProjectStore.setState({ projects: null, activeProjectId: null })

    // Simulate the localStorage-rehydrated state: user had a single source
    // file open and the plan was already closed (not present here).
    useFileViewerStore.setState({
      files: { 'src/main.ts': { content: '', loading: true } },
      openTabs: ['src/main.ts'],
      activeFile: 'src/main.ts',
    })

    // Backend still has the stale snapshot from days ago, including the plan.
    mocks.getProjectSwitchStateMock.mockResolvedValue({
      project_id: 'target-project',
      saved_session_id: '',
      open_tabs: ['src/main.ts', 'plans/old-plan.md'],
      active_file: 'plans/old-plan.md',
      updated_at: '2026-03-01T00:00:00Z',
    })

    mocks.listSessionsMock.mockResolvedValue([
      makeSession({ id: 'latest-session', project_id: 'target-project', last_active_at: '2026-06-01T10:00:00Z' }),
    ])

    const { useProjectSwitchState } = await import('@/hooks/useProjectSwitchState')
    const runSwitch = useProjectSwitchState()
    await runSwitch('target-project')

    // The localStorage-rehydrated tabs must be preserved untouched — the stale
    // backend plan must NOT be reopened.
    const fileState = useFileViewerStore.getState()
    expect(fileState.openTabs).toEqual(['src/main.ts'])
    expect(fileState.activeFile).toBe('src/main.ts')
    // No source project to save on initial activation.
    expect(mocks.saveProjectSwitchStateMock).not.toHaveBeenCalled()
  })
})
