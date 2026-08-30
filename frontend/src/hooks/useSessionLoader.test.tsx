// jsdom environment: the hook is rendered through a real React root
// (createRoot) so effects run; document is required. Mirrors the
// useExitGuard.test.tsx harness pattern.
// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ProjectSwitchState, SessionInfo } from '@/types/models'

const mocks = vi.hoisted(() => ({
  listSessionsMock: vi.fn<(...args: unknown[]) => Promise<SessionInfo[]>>(),
  getProjectSwitchStateMock: vi.fn<(...args: unknown[]) => Promise<ProjectSwitchState | null>>(),
  // event name → subscribed callbacks, so tests can fire push events.
  subscribers: new Map<string, Array<(data: unknown) => void>>(),
}))

vi.mock('@/api/runtime', () => ({
  subscribe: (eventName: string, callback: (data: unknown) => void) => {
    const list = mocks.subscribers.get(eventName) ?? []
    list.push(callback)
    mocks.subscribers.set(eventName, list)
    return () => {
      const current = mocks.subscribers.get(eventName) ?? []
      mocks.subscribers.set(eventName, current.filter(cb => cb !== callback))
    }
  },
}))

vi.mock('@/api/sessions', () => ({
  listSessions: mocks.listSessionsMock,
}))

vi.mock('@/api/projects', () => ({
  getProjectSwitchState: mocks.getProjectSwitchStateMock,
  // sessionStore imports this for selectSession; never called on restore
  // paths, but must exist so the store module initializes.
  saveProjectActiveSession: vi.fn(),
}))

import { useSessionLoader } from './useSessionLoader'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'

let root: Root | undefined
let container: HTMLDivElement | undefined

function Harness() {
  useSessionLoader()
  return null
}

function renderLoader() {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(<Harness />)
  })
}

/** Flush pending microtasks (the loader's fetch promises) inside act. */
async function flush() {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

function fireEvent(eventName: string, data: unknown) {
  const callbacks = mocks.subscribers.get(eventName) ?? []
  if (callbacks.length === 0) throw new Error(`no subscribers for ${eventName}`)
  act(() => {
    for (const cb of callbacks) cb(data)
  })
}

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

function savedState(sessionId: string): ProjectSwitchState {
  return {
    project_id: 'project-1',
    saved_session_id: sessionId,
    open_tabs: [],
    active_file: '',
    updated_at: '2026-03-01T00:00:00Z',
  }
}

beforeEach(() => {
  mocks.subscribers.clear()
  mocks.listSessionsMock.mockReset()
  mocks.getProjectSwitchStateMock.mockReset()
  mocks.listSessionsMock.mockResolvedValue([])
  mocks.getProjectSwitchStateMock.mockResolvedValue(null)
  useProjectStore.setState({ projects: null, activeProjectId: 'project-1' })
  useSessionStore.setState({ sessions: null, activeSessionId: null })
})

afterEach(() => {
  if (root) {
    act(() => {
      root!.unmount()
    })
    root = undefined
  }
  container?.remove()
  container = undefined
})

describe('useSessionLoader', () => {
  it('restores the saved session from the project switch state even when another session is fresher', async () => {
    mocks.getProjectSwitchStateMock.mockResolvedValue(savedState('saved-session'))
    mocks.listSessionsMock.mockResolvedValue([
      makeSession({ id: 'freshest', project_id: 'project-1', last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'saved-session', project_id: 'project-1', last_active_at: '2026-01-01T00:00:00Z' }),
    ])

    renderLoader()
    await flush()

    expect(mocks.getProjectSwitchStateMock).toHaveBeenCalledWith('project-1')
    expect(useSessionStore.getState().activeSessionId).toBe('saved-session')
  })

  it('falls back to the latest non-archived session when the saved session was deleted', async () => {
    mocks.getProjectSwitchStateMock.mockResolvedValue(savedState('deleted-session'))
    mocks.listSessionsMock.mockResolvedValue([
      makeSession({ id: 'older', project_id: 'project-1', last_active_at: '2026-01-01T00:00:00Z' }),
      makeSession({ id: 'freshest', project_id: 'project-1', last_active_at: '2026-06-01T00:00:00Z' }),
    ])

    renderLoader()
    await flush()

    expect(useSessionStore.getState().activeSessionId).toBe('freshest')
  })

  it('falls back to the latest non-archived session when the saved session is archived', async () => {
    mocks.getProjectSwitchStateMock.mockResolvedValue(savedState('archived-saved'))
    mocks.listSessionsMock.mockResolvedValue([
      makeSession({ id: 'archived-saved', project_id: 'project-1', archived: true, last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'active-older', project_id: 'project-1', last_active_at: '2026-01-01T00:00:00Z' }),
    ])

    renderLoader()
    await flush()

    expect(useSessionStore.getState().activeSessionId).toBe('active-older')
  })

  it('never restores an archived session even with the freshest activity', async () => {
    mocks.listSessionsMock.mockResolvedValue([
      makeSession({ id: 'archived-freshest', project_id: 'project-1', archived: true, last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'active-older', project_id: 'project-1', last_active_at: '2026-01-01T00:00:00Z' }),
    ])

    renderLoader()
    await flush()

    expect(useSessionStore.getState().activeSessionId).toBe('active-older')
  })

  it('leaves no active session when only archived sessions exist (creation is the switch flow\u2019s job)', async () => {
    mocks.listSessionsMock.mockResolvedValue([
      makeSession({ id: 'archived-only', project_id: 'project-1', archived: true, last_active_at: '2026-06-01T00:00:00Z' }),
    ])

    renderLoader()
    await flush()

    expect(useSessionStore.getState().activeSessionId).toBeNull()
  })

  it('does not override a session that became active while the fetch was in flight', async () => {
    let releaseList: ((sessions: SessionInfo[]) => void) | undefined
    const gate = new Promise<SessionInfo[]>((resolve) => {
      releaseList = resolve
    })
    mocks.listSessionsMock.mockImplementation(() => gate)
    mocks.getProjectSwitchStateMock.mockResolvedValue(savedState('saved-session'))

    renderLoader()
    // The effect cleared the previous project's active session.
    expect(useSessionStore.getState().activeSessionId).toBeNull()

    // performSwitch (or an explicit selection) activates a session before the
    // loader's fetch resolves.
    act(() => {
      useSessionStore.getState().setActiveSessionId('already-active')
    })

    releaseList?.([
      makeSession({ id: 'freshest', project_id: 'project-1', last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'saved-session', project_id: 'project-1', last_active_at: '2026-01-01T00:00:00Z' }),
    ])
    await flush()

    expect(useSessionStore.getState().activeSessionId).toBe('already-active')
  })

  it('restores the saved session from a sessions:loaded push while idle', async () => {
    // Initial fetch finds no sessions (nothing restored) but captures the
    // saved session id once for this activation.
    mocks.getProjectSwitchStateMock.mockResolvedValue(savedState('saved-session'))
    mocks.listSessionsMock.mockResolvedValue([])

    renderLoader()
    await flush()
    expect(useSessionStore.getState().activeSessionId).toBeNull()

    fireEvent('sessions:loaded', [
      makeSession({ id: 'freshest', project_id: 'project-1', last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'saved-session', project_id: 'project-1', last_active_at: '2026-01-01T00:00:00Z' }),
    ])
    // The push handler defers to a microtask (it first awaits the
    // saved-session pointer gate).
    await flush()

    expect(useSessionStore.getState().activeSessionId).toBe('saved-session')
  })

  it('defers a sessions:loaded push that arrives before the saved-session fetch resolves', async () => {
    let releaseList: ((sessions: SessionInfo[]) => void) | undefined
    const gate = new Promise<SessionInfo[]>((resolve) => {
      releaseList = resolve
    })
    mocks.listSessionsMock.mockImplementation(() => gate)
    mocks.getProjectSwitchStateMock.mockResolvedValue(savedState('saved-session'))

    renderLoader()

    // The push arrives while the initial fetch (and with it the saved
    // pointer) is still in flight: restoring now would pick the
    // latest-by-activity fallback instead of the saved session.
    fireEvent('sessions:loaded', [
      makeSession({ id: 'freshest', project_id: 'project-1', last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'saved-session', project_id: 'project-1', last_active_at: '2026-01-01T00:00:00Z' }),
    ])
    await flush()
    expect(useSessionStore.getState().activeSessionId).toBeNull()

    releaseList?.([
      makeSession({ id: 'freshest', project_id: 'project-1', last_active_at: '2026-06-01T00:00:00Z' }),
      makeSession({ id: 'saved-session', project_id: 'project-1', last_active_at: '2026-01-01T00:00:00Z' }),
    ])
    await flush()

    expect(useSessionStore.getState().activeSessionId).toBe('saved-session')
  })

  it('ignores pushed sessions belonging to other projects', async () => {
    renderLoader()
    await flush()

    fireEvent('sessions:loaded', [
      makeSession({ id: 'foreign', project_id: 'other-project', last_active_at: '2026-06-01T00:00:00Z' }),
    ])

    expect(useSessionStore.getState().activeSessionId).toBeNull()
    expect(useSessionStore.getState().sessions).toEqual([])
  })

  it('still updates titles on session:renamed pushes', async () => {
    const existing = makeSession({ id: 's1', project_id: 'project-1', name: 'Old name' })
    mocks.listSessionsMock.mockResolvedValue([existing])

    renderLoader()
    await flush()

    fireEvent('session:renamed', { id: 's1', name: 'New name' })

    expect(useSessionStore.getState().sessions?.find(s => s.id === 's1')?.name).toBe('New name')
  })
})
