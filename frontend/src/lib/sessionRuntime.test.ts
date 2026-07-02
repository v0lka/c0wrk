import { describe, it, expect, beforeEach } from 'vitest'
import { reconcileRuntimeStatus } from './sessionRuntime'
import { useChatStore } from '@/stores/chatStore'
import type { ChatMessageUI } from '@/types/messages'

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
})
