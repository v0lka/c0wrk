// @vitest-environment jsdom
// Tests for useActionEvents — the task_failed_resumable handler's session-list
// refresh. The backend emits task_failed_resumable right AFTER a terminal
// task_complete/error whenever the failed task stays resumable; the terminal
// handlers (useChatEvents) clear `has_unfinished_task` on their event, so this
// handler must re-set it — otherwise the archive/delete confirmation would
// skip and silently cancel the resumable task.
//
// Follows the createRoot + jsdom harness pattern (see usePasteHandler.test.tsx).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { useActionEvents } from '@/hooks/events/useActionEvents'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { isSessionBusy } from '@/hooks/useSessionStatusIndicator'
import type { SessionInfo } from '@/types/models'

const runtimeHandlers = vi.hoisted(() => new Map<string, Array<(data: unknown) => void>>())

vi.mock('@/api/runtime', () => ({
  onSessionEvent: (_sessionId: string, event: string, cb: (data: unknown) => void) => {
    const list = runtimeHandlers.get(event) ?? []
    list.push(cb)
    runtimeHandlers.set(event, list)
    return () => {
      runtimeHandlers.set(event, (runtimeHandlers.get(event) ?? []).filter(fn => fn !== cb))
    }
  },
  reportDroppedEvent: vi.fn(),
}))

const SESSION = 'sess-1'

function makeSessionInfo(overrides: Partial<SessionInfo>): SessionInfo {
  return {
    id: SESSION,
    project_id: 'proj-1',
    name: 'Session',
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
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

let container: HTMLDivElement
let root: Root

function Harness(): null {
  useActionEvents(SESSION)
  return null
}

beforeEach(() => {
  useChatStore.setState({
    messages: {},
    messageOrder: {},
    taskActive: { [SESSION]: false },
    paused: {},
    pausing: {},
    streamingText: {},
    activityStatus: {},
  })
  useSessionStore.setState({ sessions: [makeSessionInfo({ has_unfinished_task: false })] })

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(createElement(Harness))
  })
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

describe('useActionEvents task_failed_resumable → has_unfinished_task', () => {
  it('re-sets the flag after the terminal event cleared it (degraded completion sequence)', () => {
    // Sequence the backend guarantees: task_complete(success=false) or error
    // first (clears the flag via useChatEvents), then task_failed_resumable.
    expect(isSessionBusy(SESSION)).toBe(false)

    act(() => {
      for (const cb of runtimeHandlers.get('task_failed_resumable') ?? []) {
        cb({ message: 'Plan execution failed.', task_id: 't-1' })
      }
    })

    const sessions = useSessionStore.getState().sessions!
    expect(sessions[0]!.has_unfinished_task).toBe(true)
    // The resumable task makes the session busy again — archive/delete must
    // still ask for confirmation.
    expect(isSessionBusy(SESSION)).toBe(true)
  })

  it('adds the resume banner message alongside the flag update', () => {
    act(() => {
      for (const cb of runtimeHandlers.get('task_failed_resumable') ?? []) {
        cb({ message: 'Plan execution failed.', task_id: 't-1' })
      }
    })

    const store = useChatStore.getState()
    const order = store.messageOrder[SESSION] ?? []
    const banner = order.map(id => store.messages[SESSION]![id]!).find(m => m.type === 'task_failed_resumable')
    expect(banner).toBeDefined()
    expect(banner!.metadata?.resolved).toBe(false)
  })
})
