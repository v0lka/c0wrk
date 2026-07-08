import { describe, it, expect, beforeEach } from 'vitest'
import { reconcileRuntimeStatus, reconcilePendingActions, stalePromptMatchField } from './sessionRuntime'
import { useChatStore } from '@/stores/chatStore'
import type { ChatMessageUI } from '@/types/messages'
import type { PendingActionsResponse } from '@/api/chat'

const SESSION = 'sess-1'

function resetStore(): void {
  useChatStore.setState({
    messages: {},
    messageOrder: {},
    taskActive: {},
    streamingText: null,
    streamingSessionId: null,
    activityStatus: null,
  })
}

function addMsg(overrides: Partial<ChatMessageUI> & { id: string; type: ChatMessageUI['type'] }): void {
  useChatStore.getState().addMessage(SESSION, {
    sessionId: SESSION,
    content: '',
    timestamp: Date.now(),
    ...overrides,
  })
}

function sessionMessages(): ChatMessageUI[] {
  const s = useChatStore.getState()
  return (s.messageOrder[SESSION] ?? []).map(id => s.messages[SESSION]![id]!) 
}

describe('reconcileRuntimeStatus', () => {
  beforeEach(resetStore)

  it('restores the running flag for an active session and leaves prompts untouched', () => {
    addMsg({ id: 'sl-1', type: 'step_limit', metadata: { request_id: 'r1' } })

    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: true })

    expect(useChatStore.getState().taskActive[SESSION]).toBe(true)
    expect(sessionMessages()[0]!.metadata?.resolved).toBeUndefined()
  })

  it('injects a resume banner when an unfinished task exists and none is pending', () => {
    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true })

    expect(useChatStore.getState().taskActive[SESSION]).toBe(false)
    const msgs = sessionMessages()
    expect(msgs).toHaveLength(1)
    expect(msgs[0]!.type).toBe('task_failed_resumable')
    expect(msgs[0]!.metadata?.resolved).toBe(false)
  })

  it('does not duplicate an existing unresolved resume banner', () => {
    addMsg({ id: 'resume-1', type: 'task_failed_resumable', metadata: { resolved: false } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true })

    expect(sessionMessages().filter(m => m.type === 'task_failed_resumable')).toHaveLength(1)
  })

  it('resolves stale step_limit prompts when the task is resumable but not running', () => {
    addMsg({ id: 'sl-1', type: 'step_limit', metadata: { request_id: 'r1' } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true })

    const stepLimit = sessionMessages().find(m => m.type === 'step_limit')
    expect(stepLimit?.metadata?.resolved).toBe(true)
    expect(stepLimit?.metadata?.stale).toBe(true)
  })

  it('resolves stale resumable and step_limit prompts when nothing is unfinished', () => {
    addMsg({ id: 'resume-1', type: 'task_failed_resumable', metadata: { resolved: false } })
    addMsg({ id: 'sl-1', type: 'step_limit', metadata: { request_id: 'r1' } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false })

    for (const msg of sessionMessages()) {
      expect(msg.metadata?.resolved).toBe(true)
    }
    expect(sessionMessages()).toHaveLength(2) // no banner injected
  })

  // --- stale plan_review / tool_confirm / ask_user after reload ---
  // These interactive prompts store `resolved: true` only in the in-memory
  // Zustand store — the flag is never persisted to the backend. After an app
  // reload the history comes back without `resolved`, so reconcileRuntimeStatus
  // must dismiss them as stale when the session is no longer active.

  it('resolves stale plan_review when the session completed successfully', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1', resolved: false } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false })

    const planReview = sessionMessages().find(m => m.type === 'plan_review')
    expect(planReview?.metadata?.resolved).toBe(true)
    expect(planReview?.metadata?.stale).toBe(true)
  })

  it('resolves stale plan_review when the task is resumable but not running', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1', resolved: false } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true })

    const planReview = sessionMessages().find(m => m.type === 'plan_review')
    expect(planReview?.metadata?.resolved).toBe(true)
    expect(planReview?.metadata?.stale).toBe(true)
  })

  it('resolves stale tool_confirm and ask_user when nothing is unfinished', () => {
    addMsg({ id: 'tc-1', type: 'tool_confirm', metadata: { confirm_id: 'c1' } })
    addMsg({ id: 'au-1', type: 'ask_user', metadata: { request_id: 'r1', questions: [] } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false })

    const toolConfirm = sessionMessages().find(m => m.type === 'tool_confirm')
    expect(toolConfirm?.metadata?.resolved).toBe(true)
    expect(toolConfirm?.metadata?.stale).toBe(true)

    const askUser = sessionMessages().find(m => m.type === 'ask_user')
    expect(askUser?.metadata?.resolved).toBe(true)
    expect(askUser?.metadata?.stale).toBe(true)
  })

  it('leaves plan_review untouched when the session is still active', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1', resolved: false } })

    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: false })

    const planReview = sessionMessages().find(m => m.type === 'plan_review')
    expect(planReview?.metadata?.resolved).toBe(false)
  })

  it('does not re-resolve already-resolved plan_review', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1', resolved: true, decision: 'approve' } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false })

    const planReview = sessionMessages().find(m => m.type === 'plan_review')
    expect(planReview?.metadata?.resolved).toBe(true)
    expect(planReview?.metadata?.decision).toBe('approve')
    expect(planReview?.metadata?.stale).toBeUndefined() // not touched
  })

  it('returns the resolved stale messages so the caller can persist them', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1' } })
    addMsg({ id: 'resume-1', type: 'task_failed_resumable', metadata: { resolved: false, task_id: 't-9' } })

    const stale = reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false })

    // Both resolved messages are returned, with their identifying metadata
    // intact, so the caller can map type → match field and persist.
    expect(stale).toHaveLength(2)
    expect(stale.map(m => m.type).sort()).toEqual(['plan_review', 'task_failed_resumable'])
    const resumable = stale.find(m => m.type === 'task_failed_resumable')!
    expect(resumable.metadata?.task_id).toBe('t-9')
  })

  it('returns an empty list (and does not mutate) for an active session', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1' } })

    const stale = reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: true })

    expect(stale).toHaveLength(0)
  })
})

describe('reconcilePendingActions', () => {
  beforeEach(resetStore)

  // After an app restart the in-memory pending maps are empty, so every
  // persisted HITL prompt is reported as "not pending" and must be resolved.
  const EMPTY_PENDING: PendingActionsResponse = {
    tool_confirms: [],
    step_limits: [],
    plan_approvals: [],
    ask_user: [],
  }

  it('resolves HITL prompts absent from the pending set and returns them', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1' } })
    addMsg({ id: 'tc-1', type: 'tool_confirm', metadata: { confirm_id: 'c1' } })

    const stale = reconcilePendingActions(SESSION, EMPTY_PENDING)

    expect(stale.map(m => m.type).sort()).toEqual(['plan_review', 'tool_confirm'])
    for (const msg of sessionMessages()) {
      expect(msg.metadata?.resolved).toBe(true)
      expect(msg.metadata?.stale).toBe(true)
    }
  })

  it('leaves prompts untouched when they are still reported as pending', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1' } })
    const pending: PendingActionsResponse = {
      ...EMPTY_PENDING,
      plan_approvals: [{ request_id: 'r1', plan_path: '', plan_content: 'plan' }],
    }

    const stale = reconcilePendingActions(SESSION, pending)

    expect(stale).toHaveLength(0)
    expect(sessionMessages()[0]!.metadata?.resolved).toBeUndefined()
  })

  it('adds missing pending messages not present in the store', () => {
    const pending: PendingActionsResponse = {
      ...EMPTY_PENDING,
      ask_user: [{ request_id: 'r2', questions: [{ id: 'q1', question: 'Pick one', options: [{ label: 'A', value: 'a' }] }] }],
    }

    reconcilePendingActions(SESSION, pending)

    const added = sessionMessages().find(m => m.type === 'ask_user')
    expect(added?.id).toBe('ask-user-r2')
    expect(added?.metadata?.request_id).toBe('r2')
  })
})

describe('stalePromptMatchField', () => {
  it('maps each HITL type to its identifying metadata field', () => {
    expect(stalePromptMatchField('tool_confirm')).toBe('confirm_id')
    expect(stalePromptMatchField('ask_user')).toBe('request_id')
    expect(stalePromptMatchField('step_limit')).toBe('request_id')
    expect(stalePromptMatchField('plan_review')).toBe('request_id')
    expect(stalePromptMatchField('task_failed_resumable')).toBe('task_id')
  })

  it('returns null for non-persistable types', () => {
    expect(stalePromptMatchField('assistant')).toBeNull()
    expect(stalePromptMatchField('error')).toBeNull()
  })
})
