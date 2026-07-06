import { describe, it, expect } from 'vitest'
import { shouldAddTaskCompleteOutput } from '@/hooks/events/useChatEvents'
import type { ChatMessageUI, MessageType } from '@/stores/chatStore'

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
