// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the Wails boundary so tests never touch the native runtime ---
const { runtimeMocks } = vi.hoisted(() => ({
  runtimeMocks: { setWindowTitle: vi.fn() },
}))

vi.mock('@/api/runtime', () => runtimeMocks)
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { APP_NAME, CHAT_LABEL, buildWindowTitle, useWindowTitle } from './useWindowTitle'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { ProjectInfo, SessionInfo } from '@/types/models'

const NO_PROJECT_ID = '__no_project__'

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

function makeSession(overrides: Partial<SessionInfo> & { id: string }): SessionInfo {
  return {
    project_id: overrides.project_id ?? 'proj-a',
    name: overrides.name ?? `Session ${overrides.id}`,
    created_at: overrides.created_at ?? '2026-01-01T00:00:00Z',
    last_active_at: overrides.last_active_at ?? '2026-01-01T00:00:00Z',
    archived: overrides.archived ?? false,
    pinned: overrides.pinned ?? false,
    active: overrides.active ?? false,
    total_input_tokens: overrides.total_input_tokens ?? 0,
    total_output_tokens: overrides.total_output_tokens ?? 0,
    model: overrides.model ?? 'test-model',
    family: overrides.family ?? 'test',
    has_unfinished_task: overrides.has_unfinished_task ?? false,
    ...overrides,
  }
}

// --- Pure title assembly -----------------------------------------------------

describe('buildWindowTitle', () => {
  it('returns the bare app name when no scope is active', () => {
    expect(buildWindowTitle(null, null)).toBe('c0wrk')
  })

  it('drops a session that has no project scope (no dangling separator)', () => {
    expect(buildWindowTitle(null, 'Refactor auth')).toBe('c0wrk')
  })

  it('renders the app name and project scope without a session', () => {
    expect(buildWindowTitle('MyProject', null)).toBe('c0wrk - MyProject')
  })

  it('renders the full `APP_NAME - <scope>: <session>` template', () => {
    expect(buildWindowTitle('MyProject', 'Refactor auth')).toBe('c0wrk - MyProject: Refactor auth')
  })

  it('renders the CHAT scope the same way', () => {
    expect(buildWindowTitle(CHAT_LABEL, 'Quick question')).toBe('c0wrk - CHAT: Quick question')
  })

  it('treats a blank scope as absent', () => {
    expect(buildWindowTitle('   ', 'Refactor auth')).toBe('c0wrk')
  })

  it('treats a blank session name as absent (no trailing colon)', () => {
    expect(buildWindowTitle('MyProject', '   ')).toBe('c0wrk - MyProject')
  })

  it('trims surrounding whitespace from both segments', () => {
    expect(buildWindowTitle('  MyProject  ', '  Refactor auth  ')).toBe('c0wrk - MyProject: Refactor auth')
  })

  it('exports the app name used as the first segment', () => {
    expect(APP_NAME).toBe('c0wrk')
  })
})

// --- Hook wiring -------------------------------------------------------------

let container: HTMLDivElement | null = null
let root: Root | null = null

function Harness() {
  useWindowTitle()
  return null
}

function renderHook(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(createElement(Harness))
  })
}

/** Title most recently pushed to the native runtime. */
function lastTitle(): string | undefined {
  const calls = runtimeMocks.setWindowTitle.mock.calls
  return calls[calls.length - 1]?.[0] as string | undefined
}

function resetStores() {
  useProjectStore.setState({
    projects: null,
    activeProjectId: null,
    lastRealProjectId: null,
    createDialogOpen: false,
  })
  useSessionStore.setState({ sessions: null, activeSessionId: null })
}

beforeEach(() => {
  runtimeMocks.setWindowTitle.mockReset()
  resetStores()
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

describe('useWindowTitle', () => {
  it('sets the bare app name on mount with nothing active', () => {
    renderHook()
    expect(lastTitle()).toBe('c0wrk')
  })

  it('adds the project segment once a project becomes active', () => {
    const project = makeProject({ id: 'proj-a', name: 'MyProject' })
    renderHook()

    act(() => {
      useProjectStore.getState().setProjects([project])
      useProjectStore.getState().setActiveProjectId('proj-a')
    })

    expect(lastTitle()).toBe('c0wrk - MyProject')
  })

  it('adds the session segment after the project scope once a session is selected', () => {
    const project = makeProject({ id: 'proj-a', name: 'MyProject' })
    const session = makeSession({ id: 'sess-1', name: 'Refactor auth' })
    renderHook()

    act(() => {
      useProjectStore.getState().setProjects([project])
      useProjectStore.getState().setActiveProjectId('proj-a')
      useSessionStore.getState().setSessions([session])
      useSessionStore.getState().setActiveSessionId('sess-1')
    })

    expect(lastTitle()).toBe('c0wrk - MyProject: Refactor auth')
  })

  it('shows CHAT instead of the No Project name', () => {
    const noProject = makeProject({ id: NO_PROJECT_ID, name: 'No Project', is_no_project: true })
    const session = makeSession({ id: 'sess-1', project_id: NO_PROJECT_ID, name: 'Quick question' })
    renderHook()

    act(() => {
      useProjectStore.getState().setProjects([noProject])
      useProjectStore.getState().setActiveProjectId(NO_PROJECT_ID)
      useSessionStore.getState().setSessions([session])
      useSessionStore.getState().setActiveSessionId('sess-1')
    })

    expect(lastTitle()).toBe('c0wrk - CHAT: Quick question')
  })

  it('follows a project rename without a project switch', () => {
    const project = makeProject({ id: 'proj-a', name: 'MyProject' })
    renderHook()

    act(() => {
      useProjectStore.getState().setProjects([project])
      useProjectStore.getState().setActiveProjectId('proj-a')
    })

    act(() => {
      useProjectStore.getState().updateProject('proj-a', { name: 'Renamed' })
    })

    expect(lastTitle()).toBe('c0wrk - Renamed')
  })

  it('follows a session rename (background auto-titling)', () => {
    const project = makeProject({ id: 'proj-a', name: 'MyProject' })
    const session = makeSession({ id: 'sess-1', name: 'Session sess-1' })
    renderHook()

    act(() => {
      useProjectStore.getState().setProjects([project])
      useProjectStore.getState().setActiveProjectId('proj-a')
      useSessionStore.getState().setSessions([session])
      useSessionStore.getState().setActiveSessionId('sess-1')
    })
    expect(lastTitle()).toBe('c0wrk - MyProject: Session sess-1')

    act(() => {
      useSessionStore.getState().updateSession('sess-1', { name: 'Refactor auth' })
    })

    expect(lastTitle()).toBe('c0wrk - MyProject: Refactor auth')
  })

  it('drops the session segment while a project switch clears session state', () => {
    const project = makeProject({ id: 'proj-a', name: 'MyProject' })
    const next = makeProject({ id: 'proj-b', name: 'OtherProject' })
    const session = makeSession({ id: 'sess-1', name: 'Refactor auth' })
    renderHook()

    act(() => {
      useProjectStore.getState().setProjects([project, next])
      useProjectStore.getState().setActiveProjectId('proj-a')
      useSessionStore.getState().setSessions([session])
      useSessionStore.getState().setActiveSessionId('sess-1')
    })
    expect(lastTitle()).toBe('c0wrk - MyProject: Refactor auth')

    // Mirrors useProjectSwitchState: sessions are cleared before the
    // destination list arrives, so the title honestly drops to two segments.
    act(() => {
      useSessionStore.getState().resetForProjectSwitch()
      useProjectStore.getState().setActiveProjectId('proj-b')
    })

    expect(lastTitle()).toBe('c0wrk - OtherProject')
  })

  it('drops the project segment when the active project is deleted', () => {
    const project = makeProject({ id: 'proj-a', name: 'MyProject' })
    renderHook()

    act(() => {
      useProjectStore.getState().setProjects([project])
      useProjectStore.getState().setActiveProjectId('proj-a')
    })

    act(() => {
      useProjectStore.getState().removeProject('proj-a')
    })

    expect(lastTitle()).toBe('c0wrk')
  })

  it('does not touch the runtime when an activity bump leaves the text unchanged', () => {
    const project = makeProject({ id: 'proj-a', name: 'MyProject' })
    const session = makeSession({ id: 'sess-1', name: 'Refactor auth' })
    renderHook()

    act(() => {
      useProjectStore.getState().setProjects([project])
      useProjectStore.getState().setActiveProjectId('proj-a')
      useSessionStore.getState().setSessions([session])
      useSessionStore.getState().setActiveSessionId('sess-1')
    })
    const callsBefore = runtimeMocks.setWindowTitle.mock.calls.length

    act(() => {
      useSessionStore.getState().touchSession('sess-1')
    })

    expect(runtimeMocks.setWindowTitle.mock.calls.length).toBe(callsBefore)
    expect(lastTitle()).toBe('c0wrk - MyProject: Refactor auth')
  })
})
