import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { ProjectInfo, SessionInfo } from '@/types/models'

vi.mock('@/api/workspace', () => ({
  getSessionWorkspace: vi.fn(),
}))

import { getSessionWorkspace } from '@/api/workspace'
import { resolveChatWorkspaceRoot } from './chatWorkspaceRoot'

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
    research_root: '',
    is_research: false,
    created_at: '2024-01-01T00:00:00Z',
    last_active_at: '2024-01-01T00:00:00Z',
    ...overrides,
  }
}

describe('resolveChatWorkspaceRoot', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useSessionStore.setState({ sessions: null, activeSessionId: null })
    useProjectStore.setState({ projects: null, activeProjectId: null })
    useFileTreeStore.setState({ rootPath: null })
  })

  it('resolves a regular session against its own project workspace', async () => {
    useProjectStore.setState({
      projects: [makeProject({ id: 'p1', workspace_path: '/ws/p1' })],
      activeProjectId: 'other',
    })
    useSessionStore.setState({
      sessions: [makeSession({ id: 's1', project_id: 'p1' })],
      activeSessionId: 's1',
    })

    await expect(resolveChatWorkspaceRoot()).resolves.toBe('/ws/p1')
    expect(getSessionWorkspace).not.toHaveBeenCalled()
  })

  it('resolves a No Project session via the per-session RPC', async () => {
    vi.mocked(getSessionWorkspace).mockResolvedValue('/ws/no-project/s1/workspace')
    useProjectStore.setState({
      projects: [makeProject({ id: '__no_project__', workspace_path: '/no-project', is_no_project: true })],
      activeProjectId: '__no_project__',
    })
    useSessionStore.setState({
      sessions: [makeSession({ id: 's1', project_id: '__no_project__' })],
      activeSessionId: 's1',
    })

    await expect(resolveChatWorkspaceRoot()).resolves.toBe('/ws/no-project/s1/workspace')
    expect(getSessionWorkspace).toHaveBeenCalledWith('s1')
  })

  it('falls back to the active project when the session is not in the list', async () => {
    useProjectStore.setState({
      projects: [makeProject({ id: 'p1', workspace_path: '/ws/p1' })],
      activeProjectId: 'p1',
    })
    useSessionStore.setState({
      sessions: [makeSession({ id: 'other', project_id: 'p1' })],
      activeSessionId: 's1',
    })

    await expect(resolveChatWorkspaceRoot()).resolves.toBe('/ws/p1')
  })

  it('resolves against the active real project when the session is a stale No Project session', async () => {
    // The file tree / active project are correct (a real project), but the
    // active session still points at a No Project session. The resolver must
    // NOT use the No Project per-session workspace — it must resolve against
    // the real project's workspace.
    useProjectStore.setState({
      projects: [
        makeProject({ id: 'real', workspace_path: '/ws/real' }),
        makeProject({ id: '__no_project__', workspace_path: '/no-project', is_no_project: true }),
      ],
      activeProjectId: 'real',
    })
    useSessionStore.setState({
      sessions: [makeSession({ id: 'np', project_id: '__no_project__' })],
      activeSessionId: 'np',
    })
    useFileTreeStore.setState({ rootPath: '/ws/real' })

    await expect(resolveChatWorkspaceRoot()).resolves.toBe('/ws/real')
    expect(getSessionWorkspace).not.toHaveBeenCalled()
  })

  it('resolves an unknown session via the RPC when the active project is No Project', async () => {
    // Reproduces the cross-project desync: the active project is No Project but
    // the active session belongs to a regular project that is not in the
    // session list. The RPC must be asked for the session's real workspace.
    vi.mocked(getSessionWorkspace).mockResolvedValue('/ws/real')
    useProjectStore.setState({
      projects: [makeProject({ id: '__no_project__', is_no_project: true })],
      activeProjectId: '__no_project__',
    })
    useSessionStore.setState({
      sessions: [makeSession({ id: 'np', project_id: '__no_project__' })],
      activeSessionId: 'real-session',
    })

    await expect(resolveChatWorkspaceRoot()).resolves.toBe('/ws/real')
    expect(getSessionWorkspace).toHaveBeenCalledWith('real-session')
  })

  it('returns the file-tree root as a last resort', async () => {
    useFileTreeStore.setState({ rootPath: '/ws/tree' })
    await expect(resolveChatWorkspaceRoot()).resolves.toBe('/ws/tree')
  })
})
