// Tests for the New Session gesture's draft flow (useSessionActions).
//
// In a CODE project, New Session arms a session draft (ADR-080) instead of
// creating a session row — no RPC runs, no session becomes active; the draft
// is committed later by the first send / attachment / terminal open (see
// lib/sessionDraft.test.ts). In CHAT (No Project) mode the gesture keeps
// the legacy eager creation.
//
// No @testing-library/react in this repo; we follow the established
// createRoot + jsdom harness pattern (see useMessageSender.test.tsx).

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import type { SessionInfo, ProjectInfo } from '@/types/models'

const spies = vi.hoisted(() => ({
  createSession: vi.fn<() => Promise<SessionInfo>>(),
}))

vi.mock('@/api/sessions', () => ({
  createSession: spies.createSession,
  renameSession: vi.fn(),
  archiveSession: vi.fn(),
  deleteSession: vi.fn(),
  forkSession: vi.fn(),
  pinSession: vi.fn(),
}))

vi.mock('@/api/projects', () => ({
  saveProjectActiveSession: vi.fn(() => Promise.resolve()),
}))

import { useSessionActions } from '@/hooks/useSessionActions'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionDraftStore } from '@/stores/sessionDraftStore'

let captured: ReturnType<typeof useSessionActions> | null = null
let container: HTMLDivElement | null = null
let root: Root | null = null

function Harness() {
  captured = useSessionActions()
  return null
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

function makeSession(id = 's-1', projectId = 'proj-1'): SessionInfo {
  return {
    id,
    project_id: projectId,
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
  }
}

beforeEach(() => {
  spies.createSession.mockReset()
  useSessionDraftStore.getState().clearDraft()
  useSessionStore.setState({ sessions: [], activeSessionId: null })
  useProjectStore.setState({ activeProjectId: 'proj-1', projects: [makeProject()] })
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(<Harness />)
  })
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
  captured = null
})

describe('handleNewSession — draft flow', () => {
  it('CODE project: arms a local draft and creates NO session', async () => {
    await act(async () => {
      await captured!.handleNewSession()
    })

    expect(spies.createSession).not.toHaveBeenCalled()
    expect(useSessionStore.getState().activeSessionId).toBeNull()
    expect(useSessionDraftStore.getState().draft).toEqual({
      projectId: 'proj-1',
      workspace: { kind: 'local' },
    })
  })

  it('CODE project: clears the previously active session in memory (no persistence write)', async () => {
    useSessionStore.setState({ activeSessionId: 's-prev', sessions: [makeSession('s-prev')] })

    await act(async () => {
      await captured!.handleNewSession()
    })

    expect(useSessionStore.getState().activeSessionId).toBeNull()
    // The previous session stays in the list — only the selection moved.
    expect(useSessionStore.getState().sessions?.some((s) => s.id === 's-prev')).toBe(true)
  })

  it('CHAT (No Project): keeps the legacy eager creation', async () => {
    spies.createSession.mockResolvedValue(
      makeSession('s-chat', '__no_project__'),
    )
    useProjectStore.setState({
      activeProjectId: '__no_project__',
      projects: [makeProject({ id: '__no_project__', is_no_project: true })],
    })

    await act(async () => {
      await captured!.handleNewSession()
    })

    expect(spies.createSession).toHaveBeenCalledOnce()
    expect(useSessionStore.getState().activeSessionId).toBe('s-chat')
    expect(useSessionDraftStore.getState().draft).toBeNull()
  })

  it('re-arming resets the workspace choice — every New Session starts local', async () => {
    await act(async () => {
      await captured!.handleNewSession()
    })
    useSessionDraftStore
      .getState()
      .setDraftWorkspace({ kind: 'branch', branch: 'feat/x', createBranch: false, startPoint: '' })

    await act(async () => {
      await captured!.handleNewSession()
    })

    expect(useSessionDraftStore.getState().draft?.workspace).toEqual({ kind: 'local' })
  })
})
