import { describe, it, expect } from 'vitest'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import type { ChatMessageUI, MessageType } from '@/stores/chatStore'
import { handleToolConfirmEvent, handleToolJudgeStartedEvent, handleToolJudgeFinishedEvent } from './hitlHandlers'

let idc = 0
function makeUI(overrides: Partial<ChatMessageUI> & { type: MessageType }): ChatMessageUI {
  idc++
  return {
    id: `msg-${idc}`,
    sessionId: 'sess-1',
    content: '',
    metadata: undefined,
    timestamp: 1000 + idc,
    ...overrides,
  }
}

/** Read back the tool_confirm message created by handleToolConfirmEvent. */
function findConfirm(sessionId: string) {
  const msgs = selectSessionMessages(useChatStore.getState(), sessionId)
  return msgs.find(m => m.type === 'tool_confirm')
}

describe('handleToolConfirmEvent — tool_call_id correlation', () => {
  it('links the confirmation to the tool_call matching tool_call_id, not the most-recent same-name call', () => {
    // Two same-name tool calls. The OLDER one (tc_a) is the one being confirmed.
    const sessionId = `sess-precise-${idc}`
    useChatStore.getState().setMessages(sessionId, [
      makeUI({
        id: 'tool-tc_a',
        type: 'tool_call',
        content: 'bash_exec({"command":"ls"})',
        metadata: { tool: 'bash_exec', args: '{"command":"ls"}', step: 1, tool_call_id: 'tc_a' },
      }),
      makeUI({
        id: 'tool-tc_b',
        type: 'tool_call',
        content: 'bash_exec({"command":"pwd"})',
        metadata: { tool: 'bash_exec', args: '{"command":"pwd"}', step: 2, tool_call_id: 'tc_b' },
      }),
    ])

    handleToolConfirmEvent(sessionId, {
      confirm_id: 'c1',
      tool: 'bash_exec',
      args: '{"command":"ls"}',
      tool_call_id: 'tc_a',
    })

    const confirm = findConfirm(sessionId)
    expect(confirm).toBeDefined()
    // Precise match: must anchor to tc_a even though tc_b is more recent and
    // shares the tool name.
    expect(confirm!.metadata?.tool_msg_id).toBe('tool-tc_a')
    expect(confirm!.metadata?.tool_call_id).toBe('tc_a')

    // awaiting_confirmation is set on the EXACT tool_call being confirmed.
    const msgs = selectSessionMessages(useChatStore.getState(), sessionId)
    const a = msgs.find(m => m.id === 'tool-tc_a')
    const b = msgs.find(m => m.id === 'tool-tc_b')
    expect(a?.metadata?.awaiting_confirmation).toBe(true)
    expect(b?.metadata?.awaiting_confirmation).not.toBe(true)
  })

  it('falls back to tool-name matching when tool_call_id is absent', () => {
    const sessionId = `sess-fallback-${idc}`
    useChatStore.getState().setMessages(sessionId, [
      makeUI({
        id: 'tool-x',
        type: 'tool_call',
        content: 'edit_file(...)',
        metadata: { tool: 'edit_file', args: '{}', step: 1 },
      }),
    ])

    handleToolConfirmEvent(sessionId, {
      confirm_id: 'c2',
      tool: 'edit_file',
      args: '{}',
    })

    const confirm = findConfirm(sessionId)
    expect(confirm).toBeDefined()
    expect(confirm!.metadata?.tool_msg_id).toBe('tool-x')
  })

  it('falls back to tool-name matching when tool_call_id matches no tool_call', () => {
    const sessionId = `sess-miss-${idc}`
    useChatStore.getState().setMessages(sessionId, [
      makeUI({
        id: 'tool-y',
        type: 'tool_call',
        content: 'bash_exec(...)',
        metadata: { tool: 'bash_exec', args: '{}', step: 1, tool_call_id: 'tc_real' },
      }),
    ])

    // tool_call_id does not correspond to any known tool_call → fall back.
    handleToolConfirmEvent(sessionId, {
      confirm_id: 'c3',
      tool: 'bash_exec',
      args: '{}',
      tool_call_id: 'tc_orphan',
    })

    const confirm = findConfirm(sessionId)
    expect(confirm).toBeDefined()
    expect(confirm!.metadata?.tool_msg_id).toBe('tool-y')
  })
})

describe('judge phase handlers — strict judge (Smart Approve) activity labels', () => {
  it('says the judge is working while the strict judge runs (before any card exists)', () => {
    const sessionId = `sess-judge-${idc}`
    useChatStore.getState().setActivityStatus(sessionId, 'Running tool: bash_exec...')

    handleToolJudgeStartedEvent(sessionId, { tool: 'bash_exec' })

    // Honest status: the judge is evaluating — NOT "waiting for your response",
    // which would mislead while no confirmation card is in the chat yet.
    expect(useChatStore.getState().activityStatus[sessionId]).toBe('Safety judge evaluating...')
  })

  it('clears the judge label when the judge finishes (no verdict in the payload — no guessing)', () => {
    const sessionId = `sess-judge-done-${idc}`
    useChatStore.getState().setActivityStatus(sessionId, 'Safety judge evaluating...')

    handleToolJudgeFinishedEvent(sessionId, { tool: 'bash_exec' })

    // The payload carries no verdict, so the handler must not fabricate a
    // "Running tool" label (wrong when the verdict is CONFIRM and the user
    // denies). The next factual event sets the accurate label.
    expect(useChatStore.getState().activityStatus[sessionId]).toBeUndefined()

    // CONFIRM path: the tool_confirm card that lands right after sets the
    // honest waiting label.
    handleToolConfirmEvent(sessionId, {
      confirm_id: 'c9',
      tool: 'bash_exec',
      args: '{}',
    })
    expect(useChatStore.getState().activityStatus[sessionId]).toBe('Awaiting confirmation...')
  })
})
