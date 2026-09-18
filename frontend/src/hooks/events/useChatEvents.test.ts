// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { shouldAddTaskCompleteOutput, shouldTriggerReview, useChatEvents } from '@/hooks/events/useChatEvents'
import { useSubagentEvents } from '@/hooks/events/useSubagentEvents'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { groupMessages } from '@/lib/chatUtils'
import { useSessionStore } from '@/stores/sessionStore'
import { useAutonomyStore } from '@/stores/autonomyStore'
import { useProjectStore } from '@/stores/projectStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import type { GitPanelEntry } from '@/stores/gitPanelStore'
import { useReviewStore } from '@/stores/reviewStore'
import type { ProjectInfo } from '@/types/models'
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

// Terminal handlers call refreshCompactionAvailability → getSessionRuntimeStatus.
// Stub the RPC (null status = no-op) so the node-free test never touches
// the Wails-backed wrapper.
vi.mock('@/api/chat', () => ({
  getSessionRuntimeStatus: vi.fn(async () => null),
}))

// The post-task review trigger calls SaveReviewPrompt; stub it so the review
// tests never touch the Wails-backed wrapper.
const reviewApiMocks = vi.hoisted(() => ({ saveReviewPrompt: vi.fn() }))
vi.mock('@/api/review', () => ({
  saveReviewPrompt: (...args: unknown[]) => reviewApiMocks.saveReviewPrompt(...args),
  resolveReviewPrompt: vi.fn(),
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

/** Let pending promise chains (e.g. the async SaveReviewPrompt trigger) settle. */
const flush = () => act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })

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

describe('useChatEvents terminal events → live unfinished-task overlay', () => {
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
    // Mirror the stale-list-snapshot bug: the DB snapshot still says the task
    // is unfinished while no live overlay is set and the task is no longer
    // running (taskActive false, no pending prompts).
    useChatStore.setState({
      messages: {},
      messageOrder: {},
      taskActive: { [SESSION]: false },
      unfinishedTaskStatus: {},
      paused: {},
      pausing: {},
      streamingText: {},
      activityStatus: {},
    })
    useSessionStore.setState({
      sessions: [makeSessionInfo({ has_unfinished_task: true, unfinished_task_status: 'in_progress' })],
    })
  }

  function emit(event: string, data?: unknown): void {
    act(() => {
      for (const cb of runtimeHandlers.get(event) ?? []) cb(data)
    })
  }

  function liveStatus(): string | undefined {
    return useChatStore.getState().unfinishedTaskStatus[SESSION]
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

  it('task_complete clears the overlay and the session passes isSessionBusy', () => {
    expect(isSessionBusy(SESSION)).toBe(true) // stale DB snapshot: busy before the event

    emit('task_complete', { success: true, output: 'done' })

    expect(liveStatus()).toBe('')
    expect(isSessionBusy(SESSION)).toBe(false) // no app restart needed
  })

  it('a degraded task_complete also clears the overlay (task_failed_resumable re-arms it, see useActionEvents)', () => {
    // The backend emits task_failed_resumable right AFTER a degraded
    // completion whenever the task stays resumable — that handler owns
    // restoring the overlay as 'failed'.
    emit('task_complete', { success: false, output: 'partial' })

    expect(liveStatus()).toBe('')
  })

  it('task_cancelled clears the overlay', () => {
    emit('task_cancelled')

    expect(liveStatus()).toBe('')
    expect(isSessionBusy(SESSION)).toBe(false)
  })

  it('a terminal error clears the overlay', () => {
    emit('error', { error: 'boom' })

    expect(liveStatus()).toBe('')
    expect(isSessionBusy(SESSION)).toBe(false)
  })

  // ── Silent mode suppresses the post-task review prompt ──
  const sessionMessages = () => selectSessionMessages(useChatStore.getState(), SESSION)

  function seedCodeModeWithChanges(): void {
    useProjectStore.setState({
      activeProjectId: 'p1',
      projects: [{ id: 'p1', is_no_project: false } as unknown as ProjectInfo],
    })
    useGitPanelStore.setState({
      isGitRepo: true,
      gitRepoProjectId: 'p1',
      entries: [{ path: 'src/a.ts' } as unknown as GitPanelEntry],
    })
  }

  it('suppresses the review prompt while silent mode is on', async () => {
    seedCodeModeWithChanges()
    useReviewStore.setState({ reviewLoopActive: {}, promptShownForTask: {} })
    useAutonomyStore.getState().setAutonomy({ autonomy_mode: 'silent', review_prompt: { mode: 'suppress' } })

    emit('task_complete', { success: true, output: 'done' })
    await flush()

    expect(reviewApiMocks.saveReviewPrompt).not.toHaveBeenCalled()
    expect(sessionMessages().some((m) => m.type === 'review_prompt')).toBe(false)
  })

  it('injects the review prompt when silent mode is off (unchanged behavior)', async () => {
    seedCodeModeWithChanges()
    useReviewStore.setState({ reviewLoopActive: {}, promptShownForTask: {} })
    useAutonomyStore.getState().setAutonomy({ autonomy_mode: 'standard', review_prompt: { mode: 'suppress' } })
    reviewApiMocks.saveReviewPrompt.mockResolvedValue({ prompt_id: 'p-1', content: 'Review the changes?' })

    emit('task_complete', { success: true, output: 'done' })
    await flush()

    expect(reviewApiMocks.saveReviewPrompt).toHaveBeenCalledTimes(1)
    expect(sessionMessages().some((m) => m.type === 'review_prompt')).toBe(true)
  })
})

describe('useChatEvents scoped assistant answer (subagent / plan step)', () => {
  const SESSION = 'sess-1'

  function emit(event: string, data?: unknown): void {
    act(() => {
      for (const cb of runtimeHandlers.get(event) ?? []) cb(data)
    })
  }

  let container: HTMLDivElement
  let root: Root

  // Both hooks register their handlers with the mocked runtime; the scoped
  // subagent launch/complete events come from useSubagentEvents while the
  // assistant stream comes from useChatEvents — render both so the integration
  // path (launch → scoped answer → grouping) is exercised end to end.
  function Harness(): null {
    useChatEvents(SESSION)
    useSubagentEvents(SESSION)
    return null
  }

  beforeEach(() => {
    useChatStore.setState({
      messages: {},
      messageOrder: {},
      streamingText: {},
      activityStatus: {},
      taskActive: {},
      paused: {},
      pausing: {},
    })
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

  const messages = () => selectSessionMessages(useChatStore.getState(), SESSION)

  it('nests a subagent-scoped final answer in its block and keeps it out of the root chat', () => {
    emit('subagent_launch', { step_id: 'del_1', description: 'Research topic' })
    // The scoped emitter flushes chunk+done together, both tagged plan_step_id.
    emit('assistant_chunk', { content: 'the answer', accumulated_content: 'the answer', plan_step_id: 'del_1' })
    emit('assistant_done', { content: 'the answer', input_tokens: 1, output_tokens: 1, plan_step_id: 'del_1' })

    // The scoped chunk must not leak into the session-global streaming buffer
    // (which ChatArea renders at the root of the Conductor chat).
    expect(useChatStore.getState().streamingText[SESSION]).toBeUndefined()

    const { items } = groupMessages(messages())
    const sub = items.find(it => it.kind === 'subagent') as
      { children: Array<{ kind: string; message?: { content: string } }> } | undefined
    expect(sub).toBeDefined()
    // The answer nests INSIDE the subagent block...
    expect(sub!.children.some(c => c.kind === 'assistant' && c.message?.content === 'the answer')).toBe(true)
    // ...and no assistant item leaked into the root (Conductor) stream.
    expect(items.some(it => it.kind === 'assistant')).toBe(false)
  })

  it('still flushes a root (Conductor) assistant answer to the chat', () => {
    emit('assistant_chunk', { content: 'conductor answer', accumulated_content: 'conductor answer' })
    expect(useChatStore.getState().streamingText[SESSION]).toBe('conductor answer')

    emit('assistant_done', { content: 'conductor answer', input_tokens: 1, output_tokens: 1 })

    const { items } = groupMessages(messages())
    expect(items.some(it => it.kind === 'assistant')).toBe(true)
    expect(useChatStore.getState().streamingText[SESSION]).toBeUndefined()
  })
})
