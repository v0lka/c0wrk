import { describe, it, expect, vi } from 'vitest'
import { roleToType, chatMessageToUI, rebuildPlanFromHistory, extractPendingActions, groupMessages } from './chatUtils'
import type { ChatMessage } from '@/types/models'
import type { ChatMessageUI } from '@/types/messages'

/**
 * Helper to create ChatMessage-like objects.
 * The real ChatMessage from Wails has a constructor + createFrom,
 * but chatMessageToUI only reads plain properties, so a POJO suffices.
 */
function makeMsg(overrides: Partial<{
  id: number
  session_id: string
  role: string
  content: string
  metadata: string
  created_at: string
}>): ChatMessage {
  return {
    id: 1,
    session_id: 'sess-1',
    role: 'user',
    content: '',
    metadata: '',
    created_at: '2025-01-01T00:00:00Z',
    ...overrides,
  } as unknown as ChatMessage
}

// ---------------------------------------------------------------------------
// 1. roleToType mapping
// ---------------------------------------------------------------------------
describe('roleToType', () => {
  it('maps user → user', () => {
    expect(roleToType['user']).toBe('user')
  })

  it('maps assistant → assistant', () => {
    expect(roleToType['assistant']).toBe('assistant')
  })

  it('maps tool_call → tool_call', () => {
    expect(roleToType['tool_call']).toBe('tool_call')
  })

  it('maps task_cancelled → error (remapped)', () => {
    expect(roleToType['task_cancelled']).toBe('error')
  })

  it('unknown role falls back to assistant via chatMessageToUI', () => {
    const result = chatMessageToUI(makeMsg({ role: 'unknown_role_xyz' }))
    expect(result.type).toBe('assistant')
  })
})

// ---------------------------------------------------------------------------
// 2. reconstructContent (tested through chatMessageToUI .content)
// ---------------------------------------------------------------------------
describe('reconstructContent (via chatMessageToUI)', () => {
  it('routing with domain and complexity', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'routing',
      metadata: JSON.stringify({ domain: 'coding', complexity: 'high' }),
    }))
    expect(result.content).toContain('Domain: coding')
    expect(result.content).toContain('Complexity: high')
  })

  it('tool_call with tool and args', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'tool_call',
      metadata: JSON.stringify({ tool: 'read_file', args: '/path' }),
    }))
    expect(result.content).toBe('read_file(/path)')
  })

  it('thought passes rawContent through', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'thought',
      content: 'my thought content',
      metadata: JSON.stringify({ step_num: 1 }),
    }))
    expect(result.content).toBe('my thought content')
  })

  it('thinking with step_num', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'thinking',
      metadata: JSON.stringify({ step_num: 3 }),
    }))
    expect(result.content).toBe('Step 3...')
  })

  it('error with error string in metadata', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'error',
      metadata: JSON.stringify({ error: 'something broke' }),
    }))
    expect(result.content).toBe('something broke')
  })

  it('plan_step_start with description', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'plan_step_start',
      metadata: JSON.stringify({ description: 'Init setup' }),
    }))
    expect(result.content).toBe('Init setup')
  })

  it('plan_step_complete returns empty string', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'plan_step_complete',
      metadata: JSON.stringify({}),
    }))
    expect(result.content).toBe('')
  })

  it('plan returns empty string', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'plan',
      metadata: JSON.stringify({}),
    }))
    expect(result.content).toBe('')
  })

  it('retry with attempt and max_attempts', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'retry',
      metadata: JSON.stringify({ attempt: 2, max_attempts: 3 }),
    }))
    expect(result.content).toBe('Retry attempt 2/3')
  })

  it('step_retry with attempt and max_attempts', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'step_retry',
      metadata: JSON.stringify({ attempt: 1, max_attempts: 5 }),
    }))
    expect(result.content).toBe('Retrying step 1/5...')
  })

  it('subagent_launch with description', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'subagent_launch',
      metadata: JSON.stringify({ description: 'Research code' }),
    }))
    expect(result.content).toBe('SubAgent: Research code')
  })

  it('tool_confirm with tool name', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'tool_confirm',
      metadata: JSON.stringify({ tool: 'bash' }),
    }))
    expect(result.content).toBe('Confirm: bash')
  })

  it('ask_user with question', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'ask_user',
      metadata: JSON.stringify({ question: 'Choose option' }),
    }))
    expect(result.content).toBe('Choose option')
  })

  it('task_cancelled returns fixed string', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'task_cancelled',
      metadata: JSON.stringify({}),
    }))
    expect(result.content).toBe('Task was cancelled')
  })

  it('status with content in metadata', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'status',
      metadata: JSON.stringify({ content: 'Processing...' }),
    }))
    expect(result.content).toBe('Processing...')
  })

  it('status skills_activated reconstructs human-readable text from raw JSON content', () => {
    // Mirrors what backend/session/event_persister.go persists for a
    // "skills_activated" event: role "status", content left empty so the
    // persister writes the raw JSON payload ({"skills":[...]}) as content.
    const result = chatMessageToUI(makeMsg({
      role: 'status',
      content: '{"skills":["go-control-flow"]}',
      metadata: JSON.stringify({ skills: ['go-control-flow'] }),
    }))
    // Must match the live useLifecycleEvents handler output exactly.
    expect(result.content).toBe('Skills activated: go-control-flow')
  })

  it('status skills_activated joins multiple skills with commas', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'status',
      content: '{"skills":["go-control-flow","commit"]}',
      metadata: JSON.stringify({ skills: ['go-control-flow', 'commit'] }),
    }))
    expect(result.content).toBe('Skills activated: go-control-flow, commit')
  })

  it('status skills_activated with empty skills list', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'status',
      content: '{"skills":[]}',
      metadata: JSON.stringify({ skills: [] }),
    }))
    expect(result.content).toBe('Skills activated: ')
  })

  it('task_resumed passes rawContent through', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'task_resumed',
      content: 'Resumed',
      metadata: JSON.stringify({}),
    }))
    expect(result.content).toBe('Resumed')
  })

  it('default (user) passes rawContent through', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'user',
      content: 'Hello world',
      metadata: JSON.stringify({}),
    }))
    expect(result.content).toBe('Hello world')
  })

  it('default (assistant) passes rawContent through', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'assistant',
      content: 'Some reply',
      metadata: JSON.stringify({}),
    }))
    expect(result.content).toBe('Some reply')
  })
})

// ---------------------------------------------------------------------------
// 3. buildHistoryId (tested through chatMessageToUI .id)
// ---------------------------------------------------------------------------
describe('buildHistoryId (via chatMessageToUI)', () => {
  it('routing → id starts with "routing-"', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'routing',
      metadata: JSON.stringify({ domain: 'coding' }),
    }))
    expect(result.id).toMatch(/^routing-/)
  })

  it('thinking with step_num → "step-{n}"', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'thinking',
      metadata: JSON.stringify({ step_num: 2 }),
    }))
    expect(result.id).toBe('step-2')
  })

  it('tool_call with step → id starts with "tool-"', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'tool_call',
      metadata: JSON.stringify({ step: 1 }),
    }))
    expect(result.id).toMatch(/^tool-/)
  })

  it('tool_call with tool_call_id → "tool-{tool_call_id}"', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'tool_call',
      metadata: JSON.stringify({ tool_call_id: 'tc_999_5', step: 1, tool: 'bash' }),
    }))
    expect(result.id).toBe('tool-tc_999_5')
  })

  it('tool_call with plan_step_id, step, call_idx → "tool-ps1-1-0" (no tool_call_id)', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'tool_call',
      metadata: JSON.stringify({ plan_step_id: 'ps1', step: 1, call_idx: 0 }),
    }))
    expect(result.id).toBe('tool-ps1-1-0')
  })

  it('plan → id starts with "plan-"', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'plan',
      metadata: JSON.stringify({}),
    }))
    expect(result.id).toMatch(/^plan-/)
  })

  it('subagent_launch with step_id → "subagent-{id}-launch"', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'subagent_launch',
      metadata: JSON.stringify({ step_id: 'sa1' }),
    }))
    expect(result.id).toBe('subagent-sa1-launch')
  })

  it('subagent_complete with step_id → "subagent-{id}-complete"', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'subagent_complete',
      metadata: JSON.stringify({ step_id: 'sa1' }),
    }))
    expect(result.id).toBe('subagent-sa1-complete')
  })

  it('tool_confirm with confirm_id → "tool-confirm-{id}"', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'tool_confirm',
      metadata: JSON.stringify({ confirm_id: 'c1' }),
    }))
    expect(result.id).toBe('tool-confirm-c1')
  })

  it('ask_user with request_id → "ask-user-{id}"', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'ask_user',
      metadata: JSON.stringify({ request_id: 'r1' }),
    }))
    expect(result.id).toBe('ask-user-r1')
  })

  it('no metadata → id starts with "history-"', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'tool_call',
      metadata: '',
    }))
    expect(result.id).toMatch(/^history-/)
  })
})

// ---------------------------------------------------------------------------
// 4. chatMessageToUI end-to-end
// ---------------------------------------------------------------------------
describe('chatMessageToUI end-to-end', () => {
  it('parses metadata from JSON string', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'error',
      metadata: JSON.stringify({ error: 'oops' }),
    }))
    expect(result.metadata).toEqual({ error: 'oops' })
  })

  it('uses metadata object directly if not a string', () => {
    // In practice metadata is a string, but the code handles objects too
    const result = chatMessageToUI(makeMsg({
      role: 'error',
      metadata: { error: 'oops' } as unknown as string,
    }))
    expect(result.metadata).toEqual({ error: 'oops' })
  })

  it('metadata null → metadata field is undefined', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'user',
      content: 'hi',
      metadata: null as unknown as string,
    }))
    expect(result.metadata).toBeUndefined()
  })

  it('metadata undefined → metadata field is undefined', () => {
    const msg = makeMsg({ role: 'user', content: 'hi' })
      ; (msg as unknown as Record<string, unknown>).metadata = undefined
    const result = chatMessageToUI(msg)
    expect(result.metadata).toBeUndefined()
  })

  it('invalid JSON in metadata → metadata is undefined, no error', () => {
    const result = chatMessageToUI(makeMsg({
      role: 'user',
      content: 'hi',
      metadata: '{bad json',
    }))
    expect(result.metadata).toBeUndefined()
  })

  it('timestamp conversion from created_at', () => {
    const result = chatMessageToUI(makeMsg({
      created_at: '2025-06-15T10:30:00Z',
    }))
    expect(result.timestamp).toBe(new Date('2025-06-15T10:30:00Z').getTime())
  })

  it('missing created_at → timestamp is 0', () => {
    const msg = makeMsg({ created_at: '' })
    const result = chatMessageToUI(msg)
    expect(result.timestamp).toBe(0)
  })

  it('unknown role → type defaults to assistant', () => {
    const result = chatMessageToUI(makeMsg({ role: 'nonexistent' }))
    expect(result.type).toBe('assistant')
  })

  it('sessionId is mapped from session_id', () => {
    const result = chatMessageToUI(makeMsg({ session_id: 'abc-123' }))
    expect(result.sessionId).toBe('abc-123')
  })
})

// ---------------------------------------------------------------------------
// 5. rebuildPlanFromHistory
// ---------------------------------------------------------------------------
describe('rebuildPlanFromHistory', () => {
  function makeUI(overrides: Partial<ChatMessageUI>): ChatMessageUI {
    return {
      id: 'msg-1',
      sessionId: 'sess-1',
      type: 'user',
      content: '',
      timestamp: Date.now(),
      ...overrides,
    }
  }

  it('calls clearPlan and setPlan with reconstructed plan group', () => {
    const clearPlan = vi.fn()
    const setPlan = vi.fn()

    const messages: ChatMessageUI[] = [
      makeUI({ type: 'user', content: 'Build something' }),
      makeUI({
        id: 'plan-1',
        type: 'plan',
        content: '',
        metadata: {
          steps: [
            { id: 'step-0', description: 'Setup project', summary: 'Setup' },
            { id: 'step-1', description: 'Implement feature', summary: 'Implement' },
          ],
        },
      }),
      makeUI({
        type: 'plan_step_start',
        metadata: { step_id: 'step-0' },
      }),
      makeUI({
        type: 'plan_step_complete',
        metadata: { step_id: 'step-0', success: true, duration: 1200 },
      }),
    ]

    rebuildPlanFromHistory(messages, { clearPlan, setPlan })

    expect(clearPlan).toHaveBeenCalledOnce()
    expect(setPlan).toHaveBeenCalledOnce()

    const group = setPlan.mock.calls[0]![0]
    expect(group.items).toHaveLength(2)
    expect(group.items[0].id).toBe('step-0')
    expect(group.items[0].status).toBe('completed')
    expect(group.items[0].duration).toBe(1200)
    expect(group.items[1].id).toBe('step-1')
    expect(group.items[1].status).toBe('pending')
    expect(group.completedCount).toBe(1)
    expect(group.totalCount).toBe(2)
  })

  it('calls clearPlan but not setPlan when no plan message exists', () => {
    const clearPlan = vi.fn()
    const setPlan = vi.fn()

    const messages: ChatMessageUI[] = [
      makeUI({ type: 'user', content: 'Hello' }),
      makeUI({ type: 'assistant', content: 'Hi there' }),
    ]

    rebuildPlanFromHistory(messages, { clearPlan, setPlan })

    expect(clearPlan).toHaveBeenCalledOnce()
    expect(setPlan).not.toHaveBeenCalled()
  })

  it('ignores step_todo_update messages (checklists are handled by groupMessages, not planStore)', () => {
    const clearPlan = vi.fn()
    const setPlan = vi.fn()

    const messages: ChatMessageUI[] = [
      makeUI({
        id: 'plan-1',
        type: 'plan',
        content: '',
        metadata: {
          steps: [{ id: 'step-0', description: 'Do work', summary: 'Work' }],
        },
      }),
      makeUI({
        type: 'step_todo_update',
        metadata: {
          step_id: 'step-0',
          items: [
            { text: 'Task A', checked: true },
            { text: 'Task B', checked: false },
          ],
        },
      }),
    ]

    rebuildPlanFromHistory(messages, { clearPlan, setPlan })

    const group = setPlan.mock.calls[0]![0]
    // PlanItem no longer has todoItems — checklists are DisplayItem.kind='checklist'
    expect(group.items[0].todoItems).toBeUndefined()
  })
})

describe('groupMessages — checklist', () => {
  function makeUI(overrides: Partial<ChatMessageUI>): ChatMessageUI {
    return {
      id: 'msg-1',
      sessionId: 'sess-1',
      type: 'step_todo_update',
      content: '',
      metadata: {},
      timestamp: Date.now(),
      ...overrides,
    }
  }

  it('creates a checklist DisplayItem from step_todo_update', () => {
    const messages: ChatMessageUI[] = [
      makeUI({
        id: 'cl-1',
        type: 'step_todo_update',
        metadata: {
          step_id: 'step_1',
          items: [
            { text: 'Task A', checked: false },
            { text: 'Task B', checked: false },
          ],
        },
      }),
    ]

    const result = groupMessages(messages)
    const checklist = result.items.find(i => i.kind === 'checklist')
    expect(checklist).toBeDefined()
    expect(checklist!.kind).toBe('checklist')
    if (checklist!.kind === 'checklist') {
      expect(checklist!.stepId).toBe('step_1')
      expect(checklist!.items).toHaveLength(2)
      expect(checklist!.active).toBe(true)
    }
  })

  it('marks checklist as settled (active=false) when all items are checked', () => {
    const messages: ChatMessageUI[] = [
      makeUI({
        id: 'cl-1',
        type: 'step_todo_update',
        metadata: {
          step_id: 'step_1',
          items: [
            { text: 'Task A', checked: true },
            { text: 'Task B', checked: true },
          ],
        },
      }),
    ]

    const result = groupMessages(messages)
    const checklist = result.items.find(i => i.kind === 'checklist')
    if (checklist?.kind === 'checklist') {
      expect(checklist.active).toBe(false)
    }
  })

  it('creates a standalone checklist when step_id is empty', () => {
    const messages: ChatMessageUI[] = [
      makeUI({
        id: 'cl-1',
        type: 'step_todo_update',
        metadata: {
          step_id: '',
          items: [{ text: 'Standalone task', checked: false }],
        },
      }),
    ]

    const result = groupMessages(messages)
    const checklist = result.items.find(i => i.kind === 'checklist')
    if (checklist?.kind === 'checklist') {
      expect(checklist.stepId).toBeNull()
      expect(checklist.active).toBe(true)
    }
  })

  it('supersedes previous checklist for the same step', () => {
    const messages: ChatMessageUI[] = [
      makeUI({
        id: 'cl-1',
        type: 'step_todo_update',
        metadata: {
          step_id: 'step_1',
          items: [{ text: 'Task A', checked: false }],
        },
      }),
      makeUI({
        id: 'cl-2',
        type: 'step_todo_update',
        metadata: {
          step_id: 'step_1',
          items: [{ text: 'Task A', checked: true }],
        },
      }),
    ]

    const result = groupMessages(messages)
    const checklists = result.items.filter(i => i.kind === 'checklist')
    expect(checklists).toHaveLength(1)
    if (checklists[0]!.kind === 'checklist') {
      expect(checklists[0]!.id).toBe('cl-2')
      expect(checklists[0]!.items[0]!.checked).toBe(true)
    }
  })

  it('sinks active checklist to the end of the root container', () => {
    const messages: ChatMessageUI[] = [
      makeUI({
        id: 'cl-1',
        type: 'step_todo_update',
        metadata: {
          step_id: '',
          items: [{ text: 'Active task', checked: false }],
        },
      }),
      makeUI({
        id: 'msg-after',
        type: 'assistant',
        content: 'Working on it...',
        metadata: {},
      }),
    ]

    const result = groupMessages(messages)
    // Active checklist should be last (sunk below the assistant message)
    const lastItem = result.items[result.items.length - 1]
    expect(lastItem?.kind).toBe('checklist')
  })
})

describe('extractPendingActions — plan_review', () => {
  function makeUI(overrides: Partial<ChatMessageUI>): ChatMessageUI {
    return {
      id: 'msg-1',
      sessionId: 'sess-1',
      type: 'plan_review',
      content: '',
      metadata: {},
      timestamp: Date.now(),
      ...overrides,
    }
  }

  it('includes unresolved plan_review', () => {
    const actions = extractPendingActions([
      makeUI({ metadata: { resolved: false } }),
    ])
    expect(actions).toHaveLength(1)
    expect(actions[0]!.kind).toBe('plan_review')
  })

  it('excludes resolved plan_review', () => {
    const actions = extractPendingActions([
      makeUI({ metadata: { resolved: true, decision: 'approve' } }),
    ])
    expect(actions).toHaveLength(0)
  })

  it('includes multiple concurrent unresolved plan_review items', () => {
    const actions = extractPendingActions([
      makeUI({ id: 'pr-1', metadata: { resolved: false } }),
      makeUI({ id: 'pr-2', metadata: { resolved: false } }),
    ])
    expect(actions).toHaveLength(2)
    expect(actions.every(a => a.kind === 'plan_review')).toBe(true)
  })

  it('filters out resolved items among unresolved ones', () => {
    const actions = extractPendingActions([
      makeUI({ id: 'pr-1', metadata: { resolved: true, decision: 'approve' } }),
      makeUI({ id: 'pr-2', metadata: { resolved: false } }),
    ])
    expect(actions).toHaveLength(1)
    expect((actions[0]! as { message: ChatMessageUI }).message.id).toBe('pr-2')
  })
})
