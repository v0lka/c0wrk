// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/api/workspace', () => ({
  getSessionWorkspace: vi.fn(),
}))

import { getSessionWorkspace } from '@/api/workspace'
import { useWorkspacePath } from './useWorkspacePath'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
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

let root: Root | null = null
let container: HTMLDivElement | null = null

type PathResult = ReturnType<typeof useWorkspacePath>
let last: PathResult = null

function Harness(): null {
  last = useWorkspacePath()
  return null
}

async function renderHook(): Promise<void> {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(createElement(Harness))
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  useSessionStore.setState({ sessions: null, activeSessionId: null })
  useProjectStore.setState({ projects: null, activeProjectId: null })
  last = null
})

afterEach(() => {
  if (root) {
    const r = root
    root = null
    act(() => {
      r.unmount()
    })
  }
  container?.remove()
  container = null
})

describe('useWorkspacePath — managed-worktree session', () => {
  it('returns the session tree for a managed session of the active project', async () => {
    useProjectStore.setState({
      projects: [makeProject({ id: 'p1', workspace_path: '/ws/p1' })],
      activeProjectId: 'p1',
    })
    useSessionStore.setState({
      sessions: [makeSession({ id: 's1', project_id: 'p1', workspace_binding: managedBinding })],
      activeSessionId: 's1',
    })

    await renderHook()
    expect(last).toBe('/w/trees/s-1')
    expect(getSessionWorkspace).not.toHaveBeenCalled()
  })

  it('fetches the tree via RPC when the managed binding has no path', async () => {
    vi.mocked(getSessionWorkspace).mockResolvedValue('/w/trees/s-1')
    useProjectStore.setState({
      projects: [makeProject({ id: 'p1', workspace_path: '/ws/p1' })],
      activeProjectId: 'p1',
    })
    useSessionStore.setState({
      sessions: [makeSession({
        id: 's1',
        project_id: 'p1',
        workspace_binding: { kind: 'managed_worktree', workspace_path: '' },
      })],
      activeSessionId: 's1',
    })

    await renderHook()
    expect(last).toBe('/w/trees/s-1')
    expect(getSessionWorkspace).toHaveBeenCalledWith('s1')
  })

  it('does not leak a foreign project managed tree', async () => {
    useProjectStore.setState({
      projects: [
        makeProject({ id: 'p1', workspace_path: '/ws/p1' }),
        makeProject({ id: 'p2', workspace_path: '/ws/p2' }),
      ],
      activeProjectId: 'p2',
    })
    useSessionStore.setState({
      sessions: [makeSession({ id: 's1', project_id: 'p1', workspace_binding: managedBinding })],
      activeSessionId: 's1',
    })

    await renderHook()
    expect(last).toBe('/ws/p2')
    expect(getSessionWorkspace).not.toHaveBeenCalled()
  })

  it('resolves a local-binding session to the project checkout', async () => {
    useProjectStore.setState({
      projects: [makeProject({ id: 'p1', workspace_path: '/ws/p1' })],
      activeProjectId: 'p1',
    })
    useSessionStore.setState({
      sessions: [makeSession({
        id: 's1',
        project_id: 'p1',
        workspace_binding: { kind: 'local', workspace_path: '/ws/p1' },
      })],
      activeSessionId: 's1',
    })

    await renderHook()
    expect(last).toBe('/ws/p1')
    expect(getSessionWorkspace).not.toHaveBeenCalled()
  })
})

describe('useWorkspacePath — pre-existing rules', () => {
  it('returns the project workspace for a regular project without a session', async () => {
    useProjectStore.setState({
      projects: [makeProject({ id: 'p1', workspace_path: '/ws/p1' })],
      activeProjectId: 'p1',
    })

    await renderHook()
    expect(last).toBe('/ws/p1')
    expect(getSessionWorkspace).not.toHaveBeenCalled()
  })

  it('fetches the per-session workspace for No Project', async () => {
    vi.mocked(getSessionWorkspace).mockResolvedValue('/ws/no-project/s1/workspace')
    useProjectStore.setState({
      projects: [makeProject({ id: '__no_project__', workspace_path: '/no-project', is_no_project: true })],
      activeProjectId: '__no_project__',
    })
    useSessionStore.setState({
      sessions: [makeSession({ id: 's1', project_id: '__no_project__' })],
      activeSessionId: 's1',
    })

    await renderHook()
    expect(last).toBe('/ws/no-project/s1/workspace')
    expect(getSessionWorkspace).toHaveBeenCalledWith('s1')
  })
})
