import { describe, it, expect } from 'vitest'
import { useChatStore, groupMessages, extractPendingActions, type ChatMessageUI, type MessageType, type DisplayItem } from '@/stores/chatStore'

let msgCounter = 0
function makeUI(overrides: Partial<ChatMessageUI> & { type: MessageType }): ChatMessageUI {
  msgCounter++
  return {
    id: `msg-${msgCounter}`,
    sessionId: 'sess-1',
    content: '',
    metadata: undefined,
    timestamp: 1000 + msgCounter,
    ...overrides,
  }
}

describe('groupMessages', () => {
  it('returns empty items and pendingActions for empty input', () => {
    const result = groupMessages([])
    expect(result.items).toEqual([])
    expect(result.pendingActions).toEqual([])
  })

  it('wraps a user message as kind: user', () => {
    const msg = makeUI({ type: 'user', content: 'Hello' })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('user')
  })

  it('wraps an assistant message as kind: assistant', () => {
    const msg = makeUI({ type: 'assistant', content: 'Hi there' })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('assistant')
  })

  it('pairs tool_call with matching tool_result by tool_call_id', () => {
    const call = makeUI({
      type: 'tool_call',
      metadata: { tool: 'read_file', args: '/foo', step: 1, tool_call_id: 'tc_123_1' },
    })
    const result = makeUI({
      type: 'tool_result',
      metadata: { step: 1, result: 'file contents', result_len: 13, tool_call_id: 'tc_123_1' },
    })
    const grouped = groupMessages([call, result])
    const tools = grouped.items.filter(i => i.kind === 'tool')
    expect(tools).toHaveLength(1)
    const tool = tools[0]! as DisplayItem & { kind: 'tool' }
    expect(tool.toolName).toBe('read_file')
    expect(tool.result).toBe('file contents')
    expect(tool.status).toBe('success')
  })

  it('buffers tool_result arriving before tool_call (with tool_call_id)', () => {
    const result = makeUI({
      type: 'tool_result',
      metadata: { step: 5, result: 'early result', result_len: 12, tool_call_id: 'tc_456_1' },
    })
    const call = makeUI({
      type: 'tool_call',
      metadata: { tool: 'bash', args: 'ls', step: 5, tool_call_id: 'tc_456_1' },
    })
    const grouped = groupMessages([result, call])
    const tools = grouped.items.filter(i => i.kind === 'tool')
    expect(tools).toHaveLength(1)
    const tool = tools[0]! as DisplayItem & { kind: 'tool' }
    expect(tool.result).toBe('early result')
    expect(tool.status).toBe('success')
  })

  it('falls back to composite key when tool_call_id is absent', () => {
    const call = makeUI({
      type: 'tool_call',
      metadata: { tool: 'read_file', args: '/foo', step: 1 },
    })
    const result = makeUI({
      type: 'tool_result',
      metadata: { step: 1, result: 'old data', result_len: 8 },
    })
    const grouped = groupMessages([call, result])
    const tools = grouped.items.filter(i => i.kind === 'tool')
    expect(tools).toHaveLength(1)
    const tool = tools[0]! as DisplayItem & { kind: 'tool' }
    expect(tool.result).toBe('old data')
    expect(tool.status).toBe('success')
  })

  it('skips tool_call with tool: subagent', () => {
    const msg = makeUI({
      type: 'tool_call',
      metadata: { tool: 'subagent', step: 1 },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(0)
  })

  it('renders finish tool as step_finish', () => {
    const msg = makeUI({
      type: 'tool_call',
      metadata: { tool: 'finish', step: 1 },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('step_finish')
  })

  it('renders memory tools as tool with tool metadata', () => {
    const msg = makeUI({
      type: 'tool_call',
      metadata: { tool: 'read_evidence', args: '{"key":"val"}', step: 1 },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    const item = result.items[0]! as DisplayItem & { kind: 'tool' }
    expect(item.kind).toBe('tool')
    expect(item.toolName).toBe('read_evidence')
    expect(item.args).toBe('{"key":"val"}')
    expect(item.status).toBe('running')
  })

  it('renders store_fact as tool', () => {
    const msg = makeUI({
      type: 'tool_call',
      metadata: { tool: 'store_fact', args: '{"fact":"hello"}', step: 2 },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    const item = result.items[0]! as DisplayItem & { kind: 'tool' }
    expect(item.kind).toBe('tool')
    expect(item.toolName).toBe('store_fact')
  })

  it('renders search_facts as tool', () => {
    const msg = makeUI({
      type: 'tool_call',
      metadata: { tool: 'search_facts', args: '{"query":"test"}', step: 3 },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    const item = result.items[0]! as DisplayItem & { kind: 'tool' }
    expect(item.kind).toBe('tool')
    expect(item.toolName).toBe('search_facts')
  })

  it('pairs memory tool with tool_result via tool_call_id', () => {
    const call = makeUI({
      type: 'tool_call',
      metadata: { tool: 'search_facts', args: '{"query":"test"}', step: 4, tool_call_id: 'tc_789_1' },
    })
    const result = makeUI({
      type: 'tool_result',
      metadata: { step: 4, result: 'found facts', result_len: 11, tool_call_id: 'tc_789_1' },
    })
    const grouped = groupMessages([call, result])
    expect(grouped.items).toHaveLength(1)
    const item = grouped.items[0]! as DisplayItem & { kind: 'tool' }
    expect(item.kind).toBe('tool')
    expect(item.result).toBe('found facts')
    expect(item.status).toBe('success')
  })

  it('handles plan lifecycle: plan sets index, plan_step_start/complete manage steps', () => {
    const plan = makeUI({
      type: 'plan',
      metadata: { steps: [{ id: 'ps1', description: 'First step' }] },
    })
    const stepStart = makeUI({
      type: 'plan_step_start',
      metadata: { step_id: 'ps1', description: 'First step' },
    })
    const child = makeUI({ type: 'assistant', content: 'Working...', metadata: { plan_step_id: 'ps1' } })
    const stepComplete = makeUI({
      type: 'plan_step_complete',
      metadata: { step_id: 'ps1', success: true, duration: 500 },
    })
    const result = groupMessages([plan, stepStart, child, stepComplete])
    // Plan itself produces no item; plan_step_start produces a plan_step
    expect(result.items).toHaveLength(1)
    const step = result.items[0]! as DisplayItem & { kind: 'plan_step' }
    expect(step.kind).toBe('plan_step')
    expect(step.status).toBe('completed')
    expect(step.children).toHaveLength(1)
    expect(step.children[0]!.kind).toBe('assistant')
  })

  it('keeps a single thought as kind: thought (no grouping)', () => {
    const msg = makeUI({
      type: 'thought',
      content: 'Thinking about it...',
      metadata: { step_num: 1 },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('thought')
  })

  it('collapses consecutive thoughts into a thought_group', () => {
    const t1 = makeUI({
      type: 'thought',
      content: 'First thought',
      metadata: { step_num: 1 },
    })
    const t2 = makeUI({
      type: 'thought',
      content: 'Second thought',
      metadata: { step_num: 1 },
    })
    const result = groupMessages([t1, t2])
    expect(result.items).toHaveLength(1)
    const group = result.items[0]! as DisplayItem & { kind: 'thought_group' }
    expect(group.kind).toBe('thought_group')
    expect(group.thoughts).toHaveLength(2)
    expect(group.thoughts[0]!.content).toBe('First thought')
    expect(group.thoughts[1]!.content).toBe('Second thought')
  })

  it('puts unresolved tool_confirm in pendingActions', () => {
    const msg = makeUI({
      type: 'tool_confirm',
      metadata: { confirm_id: 'c1', tool: 'bash' },
    })
    const result = groupMessages([msg])
    expect(result.pendingActions).toHaveLength(1)
    expect(result.pendingActions[0]!.kind).toBe('tool_confirm')
    // Also an action_placeholder in items
    expect(result.items.some(i => i.kind === 'action_placeholder')).toBe(true)
  })

  it('skips resolved tool_confirm', () => {
    const msg = makeUI({
      type: 'tool_confirm',
      metadata: { confirm_id: 'c1', tool: 'bash', resolved: true },
    })
    const result = groupMessages([msg])
    expect(result.pendingActions).toHaveLength(0)
    expect(result.items).toHaveLength(0)
  })

  it('puts unresolved ask_user in pendingActions', () => {
    const msg = makeUI({
      type: 'ask_user',
      metadata: { request_id: 'r1', questions: [] },
    })
    const result = groupMessages([msg])
    expect(result.pendingActions).toHaveLength(1)
    expect(result.pendingActions[0]!.kind).toBe('ask_user')
  })

  it('skips resolved ask_user', () => {
    const msg = makeUI({
      type: 'ask_user',
      metadata: { request_id: 'r1', questions: [], resolved: true },
    })
    const result = groupMessages([msg])
    expect(result.pendingActions).toHaveLength(0)
    expect(result.items).toHaveLength(0)
  })

  it('puts unresolved task_failed_resumable in pendingActions (no placeholder)', () => {
    const msg = makeUI({
      type: 'task_failed_resumable' as MessageType,
      metadata: {},
    })
    const result = groupMessages([msg])
    expect(result.pendingActions).toHaveLength(1)
    expect(result.pendingActions[0]!.kind).toBe('resume_action')
    // No placeholder in items
    expect(result.items.filter(i => i.kind === 'action_placeholder')).toHaveLength(0)
  })

  it('puts unresolved step_limit in pendingActions with placeholder', () => {
    const msg = makeUI({
      type: 'step_limit',
      metadata: { request_id: 'sl1' },
    })
    const result = groupMessages([msg])
    expect(result.pendingActions).toHaveLength(1)
    expect(result.pendingActions[0]!.kind).toBe('step_limit')
    expect(result.items.some(i => i.kind === 'action_placeholder')).toBe(true)
  })

  it('wraps error message as kind: error', () => {
    const msg = makeUI({ type: 'error', content: 'Something broke' })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('error')
  })

  it('wraps routing as service with variant routing', () => {
    const msg = makeUI({
      type: 'routing',
      content: 'Domain: coding',
      metadata: { domain: 'coding', complexity: 'high' },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    const svc = result.items[0]! as DisplayItem & { kind: 'service' }
    expect(svc.kind).toBe('service')
    expect(svc.variant).toBe('routing')
  })

  it('wraps status as service with variant status', () => {
    const msg = makeUI({
      type: 'status',
      content: 'Processing...',
      metadata: { content: 'Processing...' },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    const svc = result.items[0]! as DisplayItem & { kind: 'service' }
    expect(svc.kind).toBe('service')
    expect(svc.variant).toBe('status')
  })

  it('handles context_compaction with rounded percentages', () => {
    const msg = makeUI({
      type: 'context_compaction',
      metadata: { before_percent: 85.5, after_percent: 42.3 },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    const cc = result.items[0]! as DisplayItem & { kind: 'context_compaction' }
    expect(cc.kind).toBe('context_compaction')
    expect(cc.beforePercent).toBe(86)
    expect(cc.afterPercent).toBe(42)
  })

  it('skips step_done, thinking, subagent_launch, subagent_complete, task_resumed', () => {
    const msgs = [
      makeUI({ type: 'step_done' }),
      makeUI({ type: 'thinking' }),
      makeUI({ type: 'subagent_launch' }),
      makeUI({ type: 'subagent_complete' }),
      makeUI({ type: 'task_resumed' }),
    ]
    const result = groupMessages(msgs)
    expect(result.items).toHaveLength(0)
  })
})

describe('extractPendingActions', () => {
  it('returns empty array for empty input', () => {
    const result = extractPendingActions([])
    expect(result).toHaveLength(0)
  })

  it('returns same reference for consecutive calls with no actions', () => {
    const r1 = extractPendingActions([])
    const r2 = extractPendingActions([makeUI({ type: 'user', content: 'hi' })])
    expect(r1).toBe(r2) // same EMPTY_PENDING array reference
  })

  it('collects unresolved tool_confirm', () => {
    const msg = makeUI({ type: 'tool_confirm', metadata: { confirm_id: 'c1' } })
    const result = extractPendingActions([msg])
    expect(result).toHaveLength(1)
    expect(result[0]!.kind).toBe('tool_confirm')
  })

  it('collects unresolved ask_user', () => {
    const msg = makeUI({ type: 'ask_user', metadata: { request_id: 'r1' } })
    const result = extractPendingActions([msg])
    expect(result).toHaveLength(1)
    expect(result[0]!.kind).toBe('ask_user')
  })

  it('collects unresolved task_failed_resumable', () => {
    const msg = makeUI({ type: 'task_failed_resumable' as MessageType, metadata: {} })
    const result = extractPendingActions([msg])
    expect(result).toHaveLength(1)
    expect(result[0]!.kind).toBe('resume_action')
  })

  it('collects unresolved step_limit', () => {
    const msg = makeUI({ type: 'step_limit', metadata: { request_id: 'sl1' } })
    const result = extractPendingActions([msg])
    expect(result).toHaveLength(1)
    expect(result[0]!.kind).toBe('step_limit')
  })

  it('excludes resolved items', () => {
    const msgs = [
      makeUI({ type: 'tool_confirm', metadata: { confirm_id: 'c1', resolved: true } }),
      makeUI({ type: 'ask_user', metadata: { request_id: 'r1', resolved: true } }),
    ]
    const result = extractPendingActions(msgs)
    expect(result).toHaveLength(0)
  })

  it('collects only unresolved from mixed set', () => {
    const msgs = [
      makeUI({ type: 'tool_confirm', metadata: { confirm_id: 'c1', resolved: true } }),
      makeUI({ type: 'tool_confirm', metadata: { confirm_id: 'c2' } }),
      makeUI({ type: 'user', content: 'hello' }),
      makeUI({ type: 'ask_user', metadata: { request_id: 'r1' } }),
    ]
    const result = extractPendingActions(msgs)
    expect(result).toHaveLength(2)
  })

  it('excludes plan_review when most recent is resolved (accepted)', () => {
    // Simulates completed session with plan review: plan_review_ready followed by plan_review_accepted
    const msgs = [
      makeUI({ type: 'plan_review', metadata: { planPath: '/tmp/plan.md', resolved: false } }),
      makeUI({ type: 'plan_review', metadata: { resolved: true, decision: 'accepted' } }),
    ]
    const result = extractPendingActions(msgs)
    expect(result).toHaveLength(0)
  })

  it('excludes plan_review when most recent is resolved (rejected)', () => {
    const msgs = [
      makeUI({ type: 'plan_review', metadata: { planPath: '/tmp/plan.md', resolved: false } }),
      makeUI({ type: 'plan_review', metadata: { resolved: true, decision: 'rejected' } }),
    ]
    const result = extractPendingActions(msgs)
    expect(result).toHaveLength(0)
  })

  it('includes plan_review when not resolved (normal flow)', () => {
    const msgs = [
      makeUI({ type: 'plan_review', metadata: { planPath: '/tmp/plan.md', resolved: false } }),
    ]
    const result = extractPendingActions(msgs)
    expect(result).toHaveLength(1)
    expect(result[0]!.kind).toBe('plan_review')
  })

  it('handles plan_review after replanning: only last matters', () => {
    // Simulates: plan → reject → replan → new plan ready (not yet accepted)
    const msgs = [
      makeUI({ type: 'plan_review', metadata: { planPath: '/tmp/plan1.md', resolved: false } }),
      makeUI({ type: 'plan_review', metadata: { resolved: true, decision: 'rejected' } }),
      makeUI({ type: 'plan_review', metadata: { planPath: '/tmp/plan2.md', resolved: false } }),
    ]
    const result = extractPendingActions(msgs)
    expect(result).toHaveLength(1)
    expect((result[0]! as Extract<DisplayItem, { kind: 'plan_review' }>).message.metadata?.planPath).toBe('/tmp/plan2.md')
  })
})

describe('mergeHistoryMessages', () => {
  const SESSION = 'merge-sess'

  function resetStore(): void {
    useChatStore.setState({ messages: {}, messageOrder: {} })
  }

  function liveMsg(id: string, timestamp: number, type: MessageType = 'assistant'): ChatMessageUI {
    return { id, sessionId: SESSION, type, content: `live-${id}`, timestamp }
  }

  it('replaces messages with history when no live messages arrived during load', () => {
    resetStore()
    const store = useChatStore.getState()
    store.addMessage(SESSION, liveMsg('old-1', 500))
    const history = [liveMsg('h-1', 100), liveMsg('h-2', 200)]

    useChatStore.getState().mergeHistoryMessages(SESSION, history, 1000)

    expect(useChatStore.getState().messageOrder[SESSION]).toEqual(['h-1', 'h-2'])
  })

  it('preserves live messages that arrived while the history RPC was in flight', () => {
    resetStore()
    const store = useChatStore.getState()
    // Error event delivered after the load started but before it resolved.
    store.addMessage(SESSION, liveMsg('live-error', 1500, 'error'))
    const history = [liveMsg('h-1', 100)]

    useChatStore.getState().mergeHistoryMessages(SESSION, history, 1000)

    expect(useChatStore.getState().messageOrder[SESSION]).toEqual(['h-1', 'live-error'])
    expect(useChatStore.getState().messages[SESSION]!['live-error']!.type).toBe('error')
  })

  it('does not duplicate messages already present in the history snapshot', () => {
    resetStore()
    const store = useChatStore.getState()
    store.addMessage(SESSION, liveMsg('shared-id', 1500))
    const history = [liveMsg('shared-id', 100), liveMsg('h-2', 200)]

    useChatStore.getState().mergeHistoryMessages(SESSION, history, 1000)

    expect(useChatStore.getState().messageOrder[SESSION]).toEqual(['shared-id', 'h-2'])
  })
})
