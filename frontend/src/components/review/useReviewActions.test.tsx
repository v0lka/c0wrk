// Unit tests for useReviewActions.handleSubmit's optimistic presentation.
// A review submit bypasses the chat input (and therefore useMessageSender),
// while the chat UI renders user messages exclusively from its own optimistic
// copy — the message_received event the backend emits is intentionally
// ignored. The submit flow must therefore add the user message card itself:
// without it, the review feedback the agent is already working on stays
// invisible until a session reload brings in the persisted row.
//
// No @testing-library/react in this repo; we follow the established
// createRoot + jsdom harness pattern (see useMessageSender.test.tsx).

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { useReviewActions } from './useReviewActions'
import { useReviewStore } from '@/stores/reviewStore'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useE2SStore } from '@/stores/e2sStore'
import { logger } from '@/lib/logger'
import type { ChatMessageUI } from '@/types/messages'

// Spies exist before vi.mock factories run so they can be referenced there.
const spies = vi.hoisted(() => ({
  sendMessage: vi.fn<(...args: unknown[]) => Promise<void>>(),
  getReviewDiff: vi.fn<() => Promise<unknown[]>>(),
  clearReviewComments: vi.fn<() => Promise<void>>(),
  setReviewStatus: vi.fn<() => Promise<void>>(),
}))

vi.mock('@/api/chat', () => ({
  sendMessage: spies.sendMessage,
  cancelTask: vi.fn(),
}))

vi.mock('@/api/review', () => ({
  getReview: vi.fn(),
  getReviewDiff: spies.getReviewDiff,
  getCommitDiff: vi.fn(),
  saveReviewGeneralComment: vi.fn(),
  saveReviewFileComment: vi.fn(),
  saveReviewHunkComment: vi.fn(),
  deleteReviewComment: vi.fn(),
  setReviewStatus: spies.setReviewStatus,
  clearReviewComments: spies.clearReviewComments,
  clearReview: vi.fn(),
}))

vi.mock('@/api/git', () => ({
  stageAll: vi.fn(),
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

// Harness: capture the submit callback from the hook.
let capturedSubmit: (() => Promise<void>) | null = null
function Harness({ sessionId }: { sessionId: string }) {
  const { handleSubmit } = useReviewActions(sessionId)
  capturedSubmit = handleSubmit
  return null
}

/** All user messages currently in the chat store for a session. */
function userMessages(sessionId: string): ChatMessageUI[] {
  const order = useChatStore.getState().messageOrder[sessionId] ?? []
  const index = useChatStore.getState().messages[sessionId] ?? {}
  return order
    .map((id) => index[id])
    .filter((m): m is ChatMessageUI => m !== undefined && m.type === 'user')
}

/** Seed a session review buffer with a single general comment. */
function seedReviewComment(sessionId: string, comment: string): void {
  act(() => {
    useReviewStore.setState({
      bySession: {
        [sessionId]: {
          status: 'active',
          generalComment: comment,
          hunkComments: {},
          fileComments: {},
          loaded: true,
        },
      },
    })
  })
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  spies.sendMessage.mockReset().mockResolvedValue(undefined)
  spies.getReviewDiff.mockReset().mockResolvedValue([])
  spies.clearReviewComments.mockReset().mockResolvedValue(undefined)
  spies.setReviewStatus.mockReset().mockResolvedValue(undefined)

  act(() => {
    useChatStore.setState({
      messages: {},
      messageOrder: {},
      paused: {},
      taskActive: {},
      activityStatus: {},
    })
    useSessionStore.setState({ sessions: [], activeSessionId: null })
    useReviewStore.setState({
      bySession: {},
      reviewPageOpen: false,
      activeReviewSession: null,
      reviewLoopActive: {},
    })
    useE2SStore.getState().clearAll()
  })

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<Harness sessionId="s1" />)
  })
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

describe('useReviewActions.handleSubmit optimistic presentation', () => {
  it('adds the review feedback as a user message and marks the task active', async () => {
    seedReviewComment('s1', 'fix the null deref')

    await act(async () => {
      await capturedSubmit!()
    })

    // The optimistic card carries the formatted comments text.
    const msgs = userMessages('s1')
    expect(msgs).toHaveLength(1)
    expect(msgs[0]!.content).toBe('General comment:\nfix the null deref')
    expect(msgs[0]!.sessionId).toBe('s1')

    // The message is dispatched as review feedback (reviewMode = true, in the
    // LAST position — e2s sits before it and must stay false here).
    expect(spies.sendMessage).toHaveBeenCalledTimes(1)
    expect(spies.sendMessage).toHaveBeenCalledWith(
      's1', 'General comment:\nfix the null deref', [], [], '', '', false, '', false, true,
    )

    // Fresh-task state: the session shows as running with an activity label,
    // exactly like a normal optimistic send.
    expect(useChatStore.getState().taskActive['s1']).toBe(true)
    expect(useChatStore.getState().activityStatus['s1']).toBe('Processing...')
  })

  it('rolls back the optimistic card and keeps the review intact when the send fails', async () => {
    seedReviewComment('s1', 'fix the null deref')
    spies.sendMessage.mockRejectedValue(new Error('router offline'))

    await act(async () => {
      await capturedSubmit!()
    })

    // No phantom user card and no phantom running state.
    expect(userMessages('s1')).toHaveLength(0)
    expect(useChatStore.getState().taskActive['s1']).toBe(false)

    // The review loop flag is cleared (no spurious reopen on task_complete).
    expect(useReviewStore.getState().reviewLoopActive['s1']).toBeUndefined()

    // The review buffer survives the failure so the user can retry.
    expect(useReviewStore.getState().bySession['s1']?.generalComment).toBe('fix the null deref')
    expect(spies.clearReviewComments).not.toHaveBeenCalled()
    expect(spies.setReviewStatus).not.toHaveBeenCalled()
  })

  it('restores the pre-send unfinished-task overlay when the send fails', async () => {
    seedReviewComment('s1', 'fix the null deref')
    // A session with a resumable failed task: the optimistic activation pins the
    // overlay to '', so the rollback must put the original value back — losing
    // it would render the still-unfinished session idle and drop its busy guard.
    act(() => {
      useChatStore.setState({ unfinishedTaskStatus: { s1: 'failed' } })
    })
    spies.sendMessage.mockRejectedValue(new Error('router offline'))

    await act(async () => {
      await capturedSubmit!()
    })

    expect(useChatStore.getState().unfinishedTaskStatus['s1']).toBe('failed')
  })

  it('clears a stale E2S snapshot once the review task starts', async () => {
    seedReviewComment('s1', 'fix the null deref')
    useE2SStore.getState().applySnapshot('s1', { state: { objective: 'old' }, turn: 3 })

    await act(async () => {
      await capturedSubmit!()
    })

    // The fresh, non-E2S review task supersedes the prior E2S run: its panel
    // must not shadow the plan view.
    expect(useE2SStore.getState().snapshots['s1']).toBeUndefined()
  })

  it('keeps the E2S snapshot when the review send is rejected', async () => {
    seedReviewComment('s1', 'fix the null deref')
    useE2SStore.getState().applySnapshot('s1', { state: { objective: 'old' }, turn: 3 })
    spies.sendMessage.mockRejectedValue(new Error('router offline'))

    await act(async () => {
      await capturedSubmit!()
    })

    // The clear runs only after a successful send, so a rejected send leaves
    // the previous (finished) run's Σ snapshot intact.
    expect(useE2SStore.getState().snapshots['s1']).toBeDefined()
  })

  it('mirrors a live interjection when a task is already running', async () => {
    seedReviewComment('s1', 'also handle the empty-input case')
    act(() => {
      useChatStore.setState({ taskActive: { s1: true } })
    })

    await act(async () => {
      await capturedSubmit!()
    })

    // The message carries the is_nudge marker (the backend persists the row
    // with is_nudge: true for a live send / nudge-resume), and the session
    // stays in the running state — the running task is untouched.
    const msgs = userMessages('s1')
    expect(msgs).toHaveLength(1)
    expect(msgs[0]!.metadata?.['is_nudge']).toBe(true)
    expect(useChatStore.getState().taskActive['s1']).toBe(true)
  })

  it('closes the review UI and prevents a double send when the post-send teardown fails', async () => {
    // The feedback RPC succeeded (the task is running on it), but the
    // follow-up buffer RPCs fail (e.g. SQLite busy). The teardown is
    // best-effort: the UI must still close (leaving the auto-reopen loop armed
    // per specs/domains/review.md), and a re-invoked submit must be a no-op —
    // otherwise the identical feedback would be delivered to the running task a
    // second time.
    seedReviewComment('s1', 'fix the null deref')
    spies.clearReviewComments.mockRejectedValue(new Error('sqlite busy'))
    act(() => {
      useReviewStore.setState({ reviewPageOpen: true, activeReviewSession: 's1' })
    })

    await act(async () => {
      await capturedSubmit!()
    })

    // Delivered exactly once; the teardown failure only warns.
    expect(spies.sendMessage).toHaveBeenCalledTimes(1)
    expect(vi.mocked(logger.warn)).toHaveBeenCalled()

    // The UI is closed regardless, and the auto-reopen loop stays ARMED (the
    // next task_complete reopens the review page with the agent's fixes; the
    // loop is disarmed only on Approve)…
    expect(useReviewStore.getState().reviewPageOpen).toBe(false)
    expect(useReviewStore.getState().activeReviewSession).toBeNull()
    expect(useReviewStore.getState().reviewLoopActive['s1']).toBe(true)
    // …and the client-side buffer is gone, so a re-invoked handleSubmit
    // finds no reviewState and bails before sending.
    expect(useReviewStore.getState().bySession['s1']).toBeUndefined()

    await act(async () => {
      await capturedSubmit!()
    })
    expect(spies.sendMessage).toHaveBeenCalledTimes(1)
  })
})
