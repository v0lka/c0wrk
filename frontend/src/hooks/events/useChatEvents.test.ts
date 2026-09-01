// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { shouldAddTaskCompleteOutput, shouldTriggerReview, useChatEvents } from '@/hooks/events/useChatEvents'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { isSessionBusy } from '@/hooks/useSessionStatusIndicator'
import type { SessionInfo } from '@/types/models'
import type { ChatMessageUI, MessageType } from '@/stores/chatStore'

// The terminal-event tests below render the hook: capture the handlers it
// registers with onSessionEvent so tests can emit events without the Wails
// runtime (follows the createRoot + jsdom harness pattern, see
// usePasteHandler.test.tsx).
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

// Terminal handlers call refreshCompactionNoOp → getSessionRuntimeStatus.
// Stub the RPC (null status = no-op) so the node-free test never touches
// the Wails-backed wrapper.
vi.mock('@/api/chat', () => ({
  getSessionRuntimeStatus: vi.fn(async () => null),
}))

let counter = 0
function makeUI(overrides: Partial<ChatMessageUI> & { type: MessageType }): ChatMessageUI {
  counter++
  return {
    id: `msg-${counter}`,
    sessionId: 'sess-1',
    content: '',
    metadata: undefined,
    timestamp: 1000 + counter,
    ...overrides,
  }
}

describe('shouldAddTaskCompleteOutput', () => {
  it('returns false for empty output', () => {
    expect(shouldAddTaskCompleteOutput([], '')).toBe(false)
  })

  it('returns true when no assistant message exists', () => {
    // Explicit finish-tool path: no assistant_done was streamed, only the
    // finish tool result (buffered, not displayed). task_complete.output is
    // the only place the answer appears — must be added.
    const msgs = [makeUI({ type: 'user', content: 'do the task' })]
    expect(shouldAddTaskCompleteOutput(msgs, 'the answer')).toBe(true)
  })

  it('returns false when output matches the last assistant message (implicit finish dedup)', () => {
    // Implicit text-only finish: assistant_done flushed the streamed answer,
    // and task_complete carries the SAME output — must NOT add a duplicate.
    const msgs = [
      makeUI({ type: 'user', content: 'do the task' }),
      makeUI({ type: 'assistant', content: 'the answer' }),
    ]
    expect(shouldAddTaskCompleteOutput(msgs, 'the answer')).toBe(false)
  })

  it('returns true when output differs from the last assistant message', () => {
    // The streamed thought differs from the finish answer — both are real,
    // distinct content; task_complete.output must be added.
    const msgs = [
      makeUI({ type: 'user', content: 'do the task' }),
      makeUI({ type: 'assistant', content: 'thinking text' }),
    ]
    expect(shouldAddTaskCompleteOutput(msgs, 'the real answer')).toBe(true)
  })

  it('scopes the dedup to the current task (stops at the last user message)', () => {
    // A prior task's assistant message has the same content, but a new user
    // message started a fresh task with no assistant message yet — the
    // task_complete.output must be added (not falsely deduped across tasks).
    const msgs = [
      makeUI({ type: 'user', content: 'first task' }),
      makeUI({ type: 'assistant', content: 'same text' }),
      makeUI({ type: 'user', content: 'second task' }),
    ]
    expect(shouldAddTaskCompleteOutput(msgs, 'same text')).toBe(true)
  })

  it('ignores non-assistant messages between the last user message and the end', () => {
    // Tool calls / thoughts sit between the user message and the end, with no
    // assistant message — output must be added.
    const msgs = [
      makeUI({ type: 'user', content: 'do the task' }),
      makeUI({ type: 'thought', content: 'reasoning', metadata: { step_num: 1 } }),
      makeUI({ type: 'tool_call', metadata: { tool: 'bash', step: 1 } }),
    ]
    expect(shouldAddTaskCompleteOutput(msgs, 'the answer')).toBe(true)
  })
})

describe('shouldTriggerReview', () => {
  it('returns false in CHAT (No Project) mode even when there are git changes', () => {
    // Review is a CODE-mode-only feature: a leaked/stale isGitRepo must never
    // fire a review prompt in CHAT (No Project) mode.
    expect(shouldTriggerReview(true, true)).toBe(false)
  })

  it('returns false in CHAT mode when there are no git changes', () => {
    expect(shouldTriggerReview(true, false)).toBe(false)
  })

  it('returns true in CODE mode when there are git changes', () => {
    expect(shouldTriggerReview(false, true)).toBe(true)
  })

  it('returns false in CODE mode when there are no git changes', () => {
    expect(shouldTriggerReview(false, false)).toBe(false)
  })
})

describe('useChatEvents terminal events → has_unfinished_task refresh', () => {
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

  function seedBusySession(): void {
    // Mirror the stale-list-snapshot bug: the flag says "unfinished" while the
    // task is no longer running (taskActive false, no pending prompts).
    useChatStore.setState({
      messages: {},
      messageOrder: {},
      taskActive: { [SESSION]: false },
      paused: {},
      pausing: {},
      streamingText: {},
      activityStatus: {},
    })
    useSessionStore.setState({ sessions: [makeSessionInfo({ has_unfinished_task: true })] })
  }

  function emit(event: string, data?: unknown): void {
    act(() => {
      for (const cb of runtimeHandlers.get(event) ?? []) cb(data)
    })
  }

  function unfinishedFlag(): boolean {
    return useSessionStore.getState().sessions![0]!.has_unfinished_task
  }

  let container: HTMLDivElement
  let root: Root

  function Harness(): null {
    useChatEvents(SESSION)
    return null
  }

  beforeEach(() => {
    seedBusySession()
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

  it('task_complete clears the flag and the session passes isSessionBusy', () => {
    expect(isSessionBusy(SESSION)).toBe(true) // stale snapshot: busy before the event

    emit('task_complete', { success: true, output: 'done' })

    expect(unfinishedFlag()).toBe(false)
    expect(isSessionBusy(SESSION)).toBe(false) // no app restart needed
  })

  it('a degraded task_complete also clears the flag (task_failed_resumable re-sets it, see useActionEvents)', () => {
    // The backend emits task_failed_resumable right AFTER a degraded
    // completion whenever the task stays resumable — that handler owns
    // restoring the flag.
    emit('task_complete', { success: false, output: 'partial' })

    expect(unfinishedFlag()).toBe(false)
  })

  it('task_cancelled clears the flag', () => {
    emit('task_cancelled')

    expect(unfinishedFlag()).toBe(false)
    expect(isSessionBusy(SESSION)).toBe(false)
  })

  it('a terminal error clears the flag', () => {
    emit('error', { error: 'boom' })

    expect(unfinishedFlag()).toBe(false)
    expect(isSessionBusy(SESSION)).toBe(false)
  })
})
