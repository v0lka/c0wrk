// @vitest-environment jsdom
//
// Tests for useNotificationClicks — the App-root listener that turns a
// `notification_clicked` global event into navigation (project switch →
// session select, the live-sessions radar's pattern). The store seam and the
// project-switch hook are mocked; the api/notifications subscription is
// captured so each test can push an event through the REAL subscription path.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Capture the subscription the hook installs, and drive it manually.
type ClickCallback = (data: { notification_id: string; session_id: string; project_id: string }) => void
let capturedCallback: ClickCallback | null = null
const { switchProjectWithStateMock } = vi.hoisted(() => ({ switchProjectWithStateMock: vi.fn() }))

vi.mock('@/api/notifications', () => ({
  onNotificationClicked: (cb: ClickCallback) => {
    capturedCallback = cb
    return () => {
      capturedCallback = null
    }
  },
  initSystemNotifications: vi.fn(async () => {}),
  sendSystemNotification: vi.fn(async () => {}),
  showTestNotification: vi.fn(async () => {}),
}))
vi.mock('@/hooks/useProjectSwitchState', () => ({
  useProjectSwitchState: () => switchProjectWithStateMock,
}))

import { useNotificationClicks } from '@/hooks/useNotificationClicks'
import { useActiveSessionsStore } from '@/stores/activeSessionsStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import type { SessionInfo } from '@/types/models'

function makeSession(overrides: Partial<SessionInfo> = {}): SessionInfo {
  const ts = new Date(0).toISOString()
  return {
    id: 's1',
    project_id: 'p1',
    name: 'Session',
    created_at: ts,
    last_active_at: ts,
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

const selectSessionMock = vi.fn()
const refreshNowMock = vi.fn(async () => {})

const roots: Root[] = []

function mount(): void {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  roots.push(root)
  act(() => {
    root.render(<Probe />)
  })
}

function Probe() {
  useNotificationClicks()
  return null
}

/** Push an event through the captured subscription and flush microtasks. */
async function click(payload: Parameters<ClickCallback>[0]): Promise<void> {
  const cb = capturedCallback
  if (!cb) throw new Error('subscription not installed')
  await act(async () => {
    cb(payload)
    await Promise.resolve()
    await Promise.resolve()
  })
}

beforeEach(() => {
  capturedCallback = null
  switchProjectWithStateMock.mockReset()
  switchProjectWithStateMock.mockResolvedValue(undefined)
  selectSessionMock.mockReset()
  refreshNowMock.mockReset()
  refreshNowMock.mockResolvedValue(undefined)
  useActiveSessionsStore.setState({
    sessions: null,
    pendingOverride: {},
    refreshing: false,
    refreshNow: refreshNowMock,
  } as never)
  useSessionStore.setState({ sessions: null, activeSessionId: null, selectSession: selectSessionMock } as never)
  useProjectStore.setState({ projects: null, activeProjectId: 'p-cur', lastRealProjectId: null } as never)
  mount()
})

describe('useNotificationClicks', () => {
  it('switches project first, then selects the session (radar pattern)', async () => {
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 's-2', project_id: 'p-2' })],
    } as never)

    await click({ notification_id: 'n1', session_id: 's-2', project_id: 'p-2' })

    expect(switchProjectWithStateMock).toHaveBeenCalledTimes(1)
    expect(switchProjectWithStateMock).toHaveBeenCalledWith('p-2')
    expect(selectSessionMock).toHaveBeenCalledTimes(1)
    expect(selectSessionMock).toHaveBeenCalledWith('s-2', 'p-2')
    // The switch order runs BEFORE the select (invocation call order).
    const switchOrder = switchProjectWithStateMock.mock.invocationCallOrder[0]!
    const selectOrder = selectSessionMock.mock.invocationCallOrder[0]!
    expect(switchOrder).toBeLessThan(selectOrder)
  })

  it('skips the project switch when already on that project', async () => {
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 's-cur', project_id: 'p-cur' })],
    } as never)

    await click({ notification_id: 'n2', session_id: 's-cur', project_id: 'p-cur' })

    expect(switchProjectWithStateMock).not.toHaveBeenCalled()
    expect(selectSessionMock).toHaveBeenCalledWith('s-cur', 'p-cur')
  })

  it('resolves the project from the snapshot when the payload omits it', async () => {
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 's-3', project_id: 'p-3' })],
    } as never)

    await click({ notification_id: 'n3', session_id: 's-3', project_id: '' })

    expect(switchProjectWithStateMock).toHaveBeenCalledWith('p-3')
    expect(selectSessionMock).toHaveBeenCalledWith('s-3', 'p-3')
  })

  it('refreshes the snapshot once before declaring a session unknown', async () => {
    // First read: not present. refreshNow then reveals it.
    useActiveSessionsStore.setState({ sessions: [makeSession({ id: 'other', project_id: 'p-1' })] } as never)
    refreshNowMock.mockImplementation(async () => {
      useActiveSessionsStore.setState({
        sessions: [makeSession({ id: 's-late', project_id: 'p-late' })],
      } as never)
    })

    await click({ notification_id: 'n4', session_id: 's-late', project_id: '' })

    expect(refreshNowMock).toHaveBeenCalledTimes(1)
    expect(selectSessionMock).toHaveBeenCalledWith('s-late', 'p-late')
  })

  it('a click on an unknown session id is a logged no-op', async () => {
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 'other', project_id: 'p-1' })],
    } as never)
    const infoSpy = vi.spyOn(console, 'info').mockImplementation(() => {})
    try {
      await click({ notification_id: 'n5', session_id: 'ghost', project_id: 'p-1' })
    } finally {
      infoSpy.mockRestore()
    }

    expect(refreshNowMock).toHaveBeenCalledTimes(1)
    expect(switchProjectWithStateMock).not.toHaveBeenCalled()
    expect(selectSessionMock).not.toHaveBeenCalled()
  })

  it('an unattributed banner (empty session id) focuses only — no navigation', async () => {
    const debugSpy = vi.spyOn(console, 'debug').mockImplementation(() => {})
    try {
      await click({ notification_id: 'n6', session_id: '', project_id: '' })
    } finally {
      debugSpy.mockRestore()
    }

    expect(refreshNowMock).not.toHaveBeenCalled()
    expect(switchProjectWithStateMock).not.toHaveBeenCalled()
    expect(selectSessionMock).not.toHaveBeenCalled()
  })

  it('survives a failed project switch without selecting', async () => {
    useActiveSessionsStore.setState({
      sessions: [makeSession({ id: 's-x', project_id: 'p-x' })],
    } as never)
    switchProjectWithStateMock.mockRejectedValue(new Error('switch failed'))

    await click({ notification_id: 'n7', session_id: 's-x', project_id: 'p-x' })

    expect(switchProjectWithStateMock).toHaveBeenCalledTimes(1)
    expect(selectSessionMock).not.toHaveBeenCalled()
  })
})
