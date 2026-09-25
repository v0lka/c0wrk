import { describe, it, expect, beforeEach } from 'vitest'
import { useChatStore, groupMessages, type ChatMessageUI, type MessageType, type DisplayItem } from '@/stores/chatStore'

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
  it('returns empty items for empty input', () => {
    const result = groupMessages([])
    expect(result.items).toEqual([])
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

  it('skips plan_step_start for ad-hoc step_id not in declared plan (e.g. Conductor "main")', () => {
    const stepStart = makeUI({
      type: 'plan_step_start',
      metadata: { step_id: 'main', description: '', summary: '' },
    })
    const result = groupMessages([stepStart])
    expect(result.items).toHaveLength(0)
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

  it('suppresses empty thought content but keeps reasoning', () => {
    const msg = makeUI({
      type: 'thought',
      content: '',
      metadata: { step_num: 1, reasoning: 'I need to inspect the file first.' },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    const t = result.items[0]! as DisplayItem & { kind: 'thought' }
    expect(t.kind).toBe('thought')
    expect(t.content).toBe('')
    expect(t.reasoning).toBe('I need to inspect the file first.')
  })

  it('suppresses "(proceeding)" content but keeps reasoning', () => {
    const msg = makeUI({
      type: 'thought',
      content: '(proceeding)',
      metadata: { step_num: 1, reasoning: 'First I will read the code.' },
    })
    const result = groupMessages([msg])
    const t = result.items[0]! as DisplayItem & { kind: 'thought' }
    expect(t.kind).toBe('thought')
    expect(t.content).toBe('')
    expect(t.reasoning).toBe('First I will read the code.')
  })

  it('keeps reasoning as a separate card when a thought content is meaningful', () => {
    const msg = makeUI({
      type: 'thought',
      content: 'Let me think about this.',
      metadata: { step_num: 1, reasoning: 'The answer should consider edge cases.' },
    })
    const result = groupMessages([msg])
    const t = result.items[0]! as DisplayItem & { kind: 'thought' }
    expect(t.kind).toBe('thought')
    expect(t.content).toBe('Let me think about this.')
    expect(t.reasoning).toBe('The answer should consider edge cases.')
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

  it('drops a thought whose content duplicates the following assistant answer (no reasoning)', () => {
    // Explicit-finish path: the agent writes the answer as the finish step's
    // text content (→ thought) AND delivers it via finish (→ task_complete
    // assistant). The duplicating thought must be dropped so the answer
    // appears only as the canonical assistant message.
    const answer = 'Here is the final answer.'
    const thought = makeUI({ type: 'thought', content: answer, metadata: { step_num: 1 } })
    const assistant = makeUI({ type: 'assistant', content: answer })
    const result = groupMessages([thought, assistant])
    expect(result.items.map(i => i.kind)).toEqual(['assistant'])
  })

  it('clears a duplicating thought content but keeps its reasoning', () => {
    const answer = 'The answer is 42.'
    const thought = makeUI({ type: 'thought', content: answer, metadata: { step_num: 1, reasoning: 'deduced from inputs' } })
    const assistant = makeUI({ type: 'assistant', content: answer })
    const result = groupMessages([thought, assistant])
    expect(result.items.map(i => i.kind)).toEqual(['thought', 'assistant'])
    const t = result.items[0]! as DisplayItem & { kind: 'thought' }
    expect(t.content).toBe('')
    expect(t.reasoning).toBe('deduced from inputs')
  })

  it('keeps a thought whose content differs from the following assistant answer', () => {
    const thought = makeUI({ type: 'thought', content: 'Let me verify...', metadata: { step_num: 1 } })
    const assistant = makeUI({ type: 'assistant', content: 'The verified answer.' })
    const result = groupMessages([thought, assistant])
    expect(result.items.map(i => i.kind)).toEqual(['thought', 'assistant'])
  })

  it('does not suppress a thought that matches an earlier (not later) assistant', () => {
    const answer = 'same answer'
    const firstAssistant = makeUI({ type: 'assistant', content: answer })
    const thought = makeUI({ type: 'thought', content: answer, metadata: { step_num: 1 } })
    const result = groupMessages([firstAssistant, thought])
    expect(result.items.map(i => i.kind)).toEqual(['assistant', 'thought'])
  })

  it('dedups a thought that duplicates the task_complete answer (explicit finish path)', () => {
    const answer = 'The final deliverable.'
    const user = makeUI({ type: 'user', content: 'do the task' })
    const thought = makeUI({ type: 'thought', content: answer, metadata: { step_num: 3 } })
    const assistant = makeUI({ type: 'assistant', content: answer })
    const result = groupMessages([user, thought, assistant])
    expect(result.items.map(i => i.kind)).toEqual(['user', 'assistant'])
  })

  it('renders unresolved tool_confirm in items (sinks to end)', () => {
    const msg = makeUI({
      type: 'tool_confirm',
      metadata: { confirm_id: 'c1', tool: 'bash' },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('tool_confirm')
  })

  it('keeps resolved tool_confirm in items at stream position', () => {
    const msg = makeUI({
      type: 'tool_confirm',
      metadata: { confirm_id: 'c1', tool: 'bash', resolved: true, decision: 'confirmed' },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('tool_confirm')
  })

  it('renders unresolved ask_user in items', () => {
    const msg = makeUI({
      type: 'ask_user',
      metadata: { request_id: 'r1', questions: [] },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('ask_user')
  })

  it('keeps resolved ask_user in items at stream position', () => {
    const msg = makeUI({
      type: 'ask_user',
      metadata: { request_id: 'r1', questions: [], resolved: true, answer: 'yes' },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('ask_user')
  })

  it('renders unresolved task_failed_resumable as resume_action in items', () => {
    const msg = makeUI({
      type: 'task_failed_resumable' as MessageType,
      metadata: {},
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('resume_action')
  })

  it('renders unresolved step_limit in items', () => {
    const msg = makeUI({
      type: 'step_limit',
      metadata: { request_id: 'sl1' },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.kind).toBe('step_limit')
  })

  it('sinks unresolved pending actions to the end of items', () => {
    const user = makeUI({ type: 'user', content: 'hi' })
    const confirm = makeUI({ type: 'tool_confirm', metadata: { confirm_id: 'c1', tool: 'bash' } })
    const result = groupMessages([confirm, user])
    expect(result.items).toHaveLength(2)
    expect(result.items[0]!.kind).toBe('user')
    expect(result.items[1]!.kind).toBe('tool_confirm')
  })

  it('keeps resolved pending actions at their stream position (no sinking)', () => {
    const user = makeUI({ type: 'user', content: 'hi' })
    const confirm = makeUI({ type: 'tool_confirm', metadata: { confirm_id: 'c1', tool: 'bash', resolved: true, decision: 'confirmed' } })
    const result = groupMessages([confirm, user])
    expect(result.items).toHaveLength(2)
    expect(result.items[0]!.kind).toBe('tool_confirm')
    expect(result.items[1]!.kind).toBe('user')
  })

  it('anchors a resolved tool_confirm directly under its triggering tool call', () => {
    const call = makeUI({
      id: 'tool-call-1',
      type: 'tool_call',
      metadata: { tool: 'write_file', args: '/foo', step: 1, tool_call_id: 'tc_1' },
    })
    const confirm = makeUI({
      type: 'tool_confirm',
      metadata: {
        confirm_id: 'c1', tool: 'write_file', tool_msg_id: 'tool-call-1',
        resolved: true, decision: 'confirmed',
      },
    })
    const user = makeUI({ type: 'user', content: 'later' })
    const result = groupMessages([call, confirm, user])
    // Tool card first, decision card immediately after it, user last.
    expect(result.items.map(i => i.kind)).toEqual(['tool', 'tool_confirm', 'user'])
  })

  it('falls back to stream position when a resolved tool_confirm has no matching tool call', () => {
    const confirm = makeUI({
      type: 'tool_confirm',
      metadata: {
        confirm_id: 'c1', tool: 'bash', tool_msg_id: 'missing-tool',
        resolved: true, decision: 'denied',
      },
    })
    const user = makeUI({ type: 'user', content: 'hi' })
    const result = groupMessages([confirm, user])
    // No matching tool card → stays at its stream position.
    expect(result.items.map(i => i.kind)).toEqual(['tool_confirm', 'user'])
  })

  it('still sinks an unresolved tool_confirm to the bottom even with a tool_msg_id', () => {
    const call = makeUI({
      id: 'tool-call-2',
      type: 'tool_call',
      metadata: { tool: 'bash', args: 'rm -rf /', step: 1, tool_call_id: 'tc_2' },
    })
    const confirm = makeUI({
      type: 'tool_confirm',
      metadata: { confirm_id: 'c2', tool: 'bash', tool_msg_id: 'tool-call-2' },
    })
    const user = makeUI({ type: 'user', content: 'after' })
    const result = groupMessages([call, confirm, user])
    // Pending confirmation sinks below the user message (stays visible at bottom).
    expect(result.items.map(i => i.kind)).toEqual(['tool', 'user', 'tool_confirm'])
  })

  it('anchors a resolved tool_confirm under a tool call nested in a plan step', () => {
    const plan = makeUI({ type: 'plan', metadata: { steps: [{ id: 'ps1', description: 'Do thing' }] } })
    const stepStart = makeUI({ type: 'plan_step_start', metadata: { step_id: 'ps1', description: 'Do thing' } })
    const call = makeUI({
      id: 'tool-call-3',
      type: 'tool_call',
      metadata: { tool: 'write_file', args: '/x', step: 1, tool_call_id: 'tc_3', plan_step_id: 'ps1' },
    })
    const confirm = makeUI({
      type: 'tool_confirm',
      metadata: {
        confirm_id: 'c3', tool: 'write_file', tool_msg_id: 'tool-call-3', plan_step_id: 'ps1',
        resolved: true, decision: 'confirmed',
      },
    })
    const stepComplete = makeUI({ type: 'plan_step_complete', metadata: { step_id: 'ps1', success: true, duration: 10 } })
    const result = groupMessages([plan, stepStart, call, confirm, stepComplete])
    const step = result.items.find(i => i.kind === 'plan_step') as (DisplayItem & { kind: 'plan_step' }) | undefined
    expect(step).toBeDefined()
    // Decision card nests under the step, right after the tool card.
    expect(step!.children.map(i => i.kind)).toEqual(['tool', 'tool_confirm'])
  })

  it('renders pending action in root even when message carries a plan_step_id', () => {
    const plan = makeUI({
      type: 'plan',
      metadata: { steps: [{ id: 'ps1', description: 'First step' }] },
    })
    const stepStart = makeUI({
      type: 'plan_step_start',
      metadata: { step_id: 'ps1', description: 'First step' },
    })
    const confirm = makeUI({
      type: 'tool_confirm',
      metadata: { confirm_id: 'c1', tool: 'bash', plan_step_id: 'ps1' },
    })
    const result = groupMessages([plan, stepStart, confirm])
    const step = result.items.find(i => i.kind === 'plan_step')
    expect(step).toBeDefined()
    const confirmItem = result.items.find(i => i.kind === 'tool_confirm')
    expect(confirmItem).toBeDefined()
    if (step && step.kind === 'plan_step') {
      expect(step.children.some(i => i.kind === 'tool_confirm')).toBe(false)
    }
  })

  it('keeps only the last unresolved plan_review (replan cycle)', () => {
    const pr1 = makeUI({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1' } })
    const pr2 = makeUI({ id: 'pr-2', type: 'plan_review', metadata: { request_id: 'r2' } })
    const result = groupMessages([pr1, pr2])
    const planReviews = result.items.filter(i => i.kind === 'plan_review')
    expect(planReviews).toHaveLength(1)
    expect((planReviews[0]! as { message: ChatMessageUI }).message.id).toBe('pr-2')
  })

  it('keeps resolved plan_reviews at stream position alongside the last unresolved one', () => {
    const pr1 = makeUI({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1', resolved: true, decision: 'approve' } })
    const pr2 = makeUI({ id: 'pr-2', type: 'plan_review', metadata: { request_id: 'r2' } })
    const result = groupMessages([pr1, pr2])
    const planReviews = result.items.filter(i => i.kind === 'plan_review')
    expect(planReviews).toHaveLength(2)
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

  it('skips step_done, thinking, subagent_complete, task_resumed', () => {
    const msgs = [
      makeUI({ type: 'step_done' }),
      makeUI({ type: 'thinking' }),
      makeUI({ type: 'subagent_complete', metadata: { step_id: 'sa1', success: true, duration: 100 } }),
      makeUI({ type: 'task_resumed' }),
    ]
    const result = groupMessages(msgs)
    expect(result.items).toHaveLength(0)
  })

  it('renders subagent_launch as a subagent block', () => {
    const msg = makeUI({
      type: 'subagent_launch',
      metadata: { step_id: 'sa1', description: 'Research code' },
    })
    const result = groupMessages([msg])
    expect(result.items).toHaveLength(1)
    const sub = result.items[0]! as DisplayItem & { kind: 'subagent' }
    expect(sub.kind).toBe('subagent')
    expect(sub.stepId).toBe('sa1')
    expect(sub.title).toBe('Research code')
    expect(sub.status).toBe('running')
    expect(sub.children).toEqual([])
  })

  it('nests subagent children under the subagent block', () => {
    const launch = makeUI({
      type: 'subagent_launch',
      metadata: { step_id: 'sa1', description: 'Research code' },
    })
    const child = makeUI({
      type: 'assistant',
      content: 'Working...',
      metadata: { plan_step_id: 'sa1' },
    })
    const result = groupMessages([launch, child])
    expect(result.items).toHaveLength(1)
    const sub = result.items[0]! as DisplayItem & { kind: 'subagent' }
    expect(sub.children).toHaveLength(1)
    expect(sub.children[0]!.kind).toBe('assistant')
  })

  it('nests an autonomy_decision notice under the subagent block (silent-mode audit rows)', () => {
    const launch = makeUI({
      type: 'subagent_launch',
      metadata: { step_id: 'sa1', description: 'Research code' },
    })
    // A silent-mode automatic decision taken by the subagent's executor: the
    // backend scopes it to the delegation's block via plan_step_id (the same
    // nesting key the subagent's own tool_call events carry).
    const notice = makeUI({
      type: 'autonomy_decision',
      content: 'Silent mode («Allow» policy): allowed bash_exec',
      metadata: {
        kind: 'tool_confirm', mode: 'silent', policy: 'allow', verdict: 'allow',
        tool: 'bash_exec', justification: 'ran unattended: no hard safety reason',
        plan_step_id: 'sa1',
      },
    })
    // A root-level decision carries no plan_step_id and stays in the main stream.
    const rootNotice = makeUI({
      type: 'autonomy_decision',
      content: 'Silent mode («Allow» policy): allowed bash_exec',
      metadata: { kind: 'tool_confirm', mode: 'silent', policy: 'allow', verdict: 'allow', tool: 'bash_exec' },
    })
    const result = groupMessages([launch, notice, rootNotice])
    expect(result.items).toHaveLength(2)
    const sub = result.items[0]! as DisplayItem & { kind: 'subagent' }
    expect(sub.children).toHaveLength(1)
    expect(sub.children[0]!.kind).toBe('autonomy_decision')
    const rootDecision = result.items[1]! as DisplayItem & { kind: 'autonomy_decision' }
    expect(rootDecision.kind).toBe('autonomy_decision')
  })

  it('updates subagent status on subagent_complete', () => {
    const launch = makeUI({
      type: 'subagent_launch',
      metadata: { step_id: 'sa1', description: 'Research code' },
    })
    const complete = makeUI({
      type: 'subagent_complete',
      metadata: { step_id: 'sa1', success: false, duration: 5000, error: 'subagent exited with code 1' },
    })
    const result = groupMessages([launch, complete])
    expect(result.items).toHaveLength(1)
    const sub = result.items[0]! as DisplayItem & { kind: 'subagent' }
    expect(sub.status).toBe('failed')
    expect(sub.duration).toBe(5000)
    expect(sub.error).toBe('subagent exited with code 1')
  })

  it('does not attach an error to a successful subagent_complete', () => {
    const launch = makeUI({
      type: 'subagent_launch',
      metadata: { step_id: 'sa1', description: 'Research code' },
    })
    const complete = makeUI({
      type: 'subagent_complete',
      metadata: { step_id: 'sa1', success: true, duration: 3000, error: 'stale reason' },
    })
    const result = groupMessages([launch, complete])
    const sub = result.items[0]! as DisplayItem & { kind: 'subagent' }
    expect(sub.status).toBe('completed')
    expect(sub.error).toBeUndefined()
  })

  it('preserves subagent duration from subagent_complete', () => {
    const launch = makeUI({
      type: 'subagent_launch',
      metadata: { step_id: 'sa1', description: 'Research code' },
    })
    const complete = makeUI({
      type: 'subagent_complete',
      metadata: { step_id: 'sa1', success: true, duration: 3000 },
    })
    const result = groupMessages([launch, complete])
    expect(result.items).toHaveLength(1)
    const sub = result.items[0]! as DisplayItem & { kind: 'subagent' }
    expect(sub.status).toBe('completed')
    expect(sub.duration).toBe(3000)
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

  it('preserves an unresolved HITL prompt (ask_user) added by the background watcher even though it predates the switch', () => {
    resetStore()
    const store = useChatStore.getState()
    // Background watcher added the ask_user card at t=500; the user switches at
    // t=1000. The combined emit delivers the event to the UI before persisting
    // (backend/application.go), so in the race window (or if the best-effort
    // persist failed) the card is absent from the loaded history. Without
    // preservation it would be dropped here and only reappear after the async
    // GetPendingActions reconcile — a visible flicker, or lost entirely if
    // reconcile is skipped.
    store.addMessage(SESSION, {
      id: 'ask-user-req-1', sessionId: SESSION, type: 'ask_user',
      content: 'Pick one', metadata: { request_id: 'req-1', questions: [] }, timestamp: 500,
    })
    const history = [liveMsg('h-1', 100)]

    useChatStore.getState().mergeHistoryMessages(SESSION, history, 1000)

    const order = useChatStore.getState().messageOrder[SESSION]
    expect(order).toContain('ask-user-req-1')
    expect(useChatStore.getState().messages[SESSION]!['ask-user-req-1']!.type).toBe('ask_user')
  })

  it('preserves all four unpersisted HITL types when unresolved', () => {
    resetStore()
    const store = useChatStore.getState()
    const types: MessageType[] = ['tool_confirm', 'ask_user', 'step_limit', 'plan_review']
    for (const t of types) {
      store.addMessage(SESSION, {
        id: `${t}-1`, sessionId: SESSION, type: t, content: t,
        metadata: { request_id: 'r1', confirm_id: 'c1' }, timestamp: 500,
      })
    }

    useChatStore.getState().mergeHistoryMessages(SESSION, [liveMsg('h-1', 100)], 1000)

    const order = useChatStore.getState().messageOrder[SESSION]
    for (const t of types) {
      expect(order).toContain(`${t}-1`)
    }
  })

  it('drops a resolved HITL prompt that predates the switch (it was answered)', () => {
    resetStore()
    const store = useChatStore.getState()
    store.addMessage(SESSION, {
      id: 'ask-user-req-1', sessionId: SESSION, type: 'ask_user',
      content: 'Pick one', metadata: { request_id: 'req-1', resolved: true, answer: 'A' }, timestamp: 500,
    })

    useChatStore.getState().mergeHistoryMessages(SESSION, [liveMsg('h-1', 100)], 1000)

    expect(useChatStore.getState().messageOrder[SESSION]).not.toContain('ask-user-req-1')
  })
})

describe('addMessage idempotency', () => {
  const SESSION = 'sess-add'

  beforeEach(() => {
    useChatStore.setState({ messages: {}, messageOrder: {} })
  })

  it('re-adding the same id updates content without duplicating the order entry', () => {
    const store = useChatStore.getState()
    store.addMessage(SESSION, { id: 'goal-status-2-paused', sessionId: SESSION, type: 'status', content: 'v1', timestamp: 100 })
    store.addMessage(SESSION, { id: 'other', sessionId: SESSION, type: 'status', content: 'x', timestamp: 200 })
    // Re-emit the same deterministic id (e.g. a re-sent goal_status snapshot).
    store.addMessage(SESSION, { id: 'goal-status-2-paused', sessionId: SESSION, type: 'status', content: 'v2', timestamp: 300 })

    const order = useChatStore.getState().messageOrder[SESSION]!
    // No duplicate id in the render order.
    expect(order.filter(id => id === 'goal-status-2-paused')).toHaveLength(1)
    // First-insertion position is preserved (not moved to the end).
    expect(order).toEqual(['goal-status-2-paused', 'other'])
    // Content is updated to the latest snapshot.
    expect(useChatStore.getState().messages[SESSION]!['goal-status-2-paused']!.content).toBe('v2')
  })
})

describe('upsertChecklistMessage', () => {
  const SESSION = 'sess-checklist'

  beforeEach(() => {
    useChatStore.setState({ messages: {}, messageOrder: {} })
  })

  it('collapses repeated step_todo_update rows for the same step_id to one', () => {
    const store = useChatStore.getState()
    store.upsertChecklistMessage(SESSION, { id: 'cl-1', sessionId: SESSION, type: 'step_todo_update', content: '', metadata: { step_id: '', items: [{ text: 'A', checked: false }] }, timestamp: 100 })
    store.upsertChecklistMessage(SESSION, { id: 'cl-2', sessionId: SESSION, type: 'step_todo_update', content: '', metadata: { step_id: '', items: [{ text: 'A', checked: true }] }, timestamp: 200 })

    const order = useChatStore.getState().messageOrder[SESSION]!
    expect(order).toEqual(['cl-2'])
    expect(useChatStore.getState().messages[SESSION]!['cl-1']).toBeUndefined()
    expect(useChatStore.getState().messages[SESSION]!['cl-2']).toBeDefined()
  })

  it('keeps checklists for different step_ids separate (store-level)', () => {
    const store = useChatStore.getState()
    store.upsertChecklistMessage(SESSION, { id: 'cl-root', sessionId: SESSION, type: 'step_todo_update', content: '', metadata: { step_id: '', items: [{ text: 'Root', checked: false }] }, timestamp: 100 })
    store.upsertChecklistMessage(SESSION, { id: 'cl-step', sessionId: SESSION, type: 'step_todo_update', content: '', metadata: { step_id: 'step_1', items: [{ text: 'Step', checked: false }] }, timestamp: 200 })

    const order = useChatStore.getState().messageOrder[SESSION]!
    expect(order).toEqual(['cl-root', 'cl-step'])
  })

  it('leaves non-checklist messages untouched', () => {
    const store = useChatStore.getState()
    store.addMessage(SESSION, { id: 'user-1', sessionId: SESSION, type: 'user', content: 'hi', timestamp: 50 })
    store.upsertChecklistMessage(SESSION, { id: 'cl-1', sessionId: SESSION, type: 'step_todo_update', content: '', metadata: { step_id: '', items: [{ text: 'A', checked: false }] }, timestamp: 100 })

    const order = useChatStore.getState().messageOrder[SESSION]!
    expect(order).toEqual(['user-1', 'cl-1'])
    expect(useChatStore.getState().messages[SESSION]!['user-1']).toBeDefined()
  })
})

describe('setPaused', () => {
  const SESSION = 'sess-pause'

  beforeEach(() => {
    useChatStore.setState({ paused: {} })
  })

  it('sets paused=true for a session', () => {
    useChatStore.getState().setPaused(SESSION, true)
    expect(useChatStore.getState().paused[SESSION]).toBe(true)
  })

  it('clears the paused entry when set to false (absent key encodes "not paused")', () => {
    useChatStore.getState().setPaused(SESSION, true)
    useChatStore.getState().setPaused(SESSION, false)
    expect(useChatStore.getState().paused[SESSION]).toBeUndefined()
  })

  it('is a no-op when clearing a session that was never paused', () => {
    const before = useChatStore.getState().paused
    useChatStore.getState().setPaused(SESSION, false)
    // Same reference — no state change emitted.
    expect(useChatStore.getState().paused).toBe(before)
  })
})

describe('setPausing', () => {
  const SESSION = 'sess-pausing'

  beforeEach(() => {
    useChatStore.setState({ pausing: {} })
  })

  it('sets pausing=true for a session', () => {
    useChatStore.getState().setPausing(SESSION, true)
    expect(useChatStore.getState().pausing[SESSION]).toBe(true)
  })

  it('clears the pausing entry when set to false (absent key encodes "not pausing")', () => {
    useChatStore.getState().setPausing(SESSION, true)
    useChatStore.getState().setPausing(SESSION, false)
    expect(useChatStore.getState().pausing[SESSION]).toBeUndefined()
  })

  it('is a no-op when clearing a session that was never pausing', () => {
    const before = useChatStore.getState().pausing
    useChatStore.getState().setPausing(SESSION, false)
    // Same reference — no state change emitted.
    expect(useChatStore.getState().pausing).toBe(before)
  })

  it('is independent of the paused flag (in-flight vs landed pause)', () => {
    useChatStore.getState().setPausing(SESSION, true)
    useChatStore.getState().setPaused(SESSION, true)
    useChatStore.getState().setPausing(SESSION, false)
    // session_paused clearing the in-flight flag must not un-pause the task.
    expect(useChatStore.getState().pausing[SESSION]).toBeUndefined()
    expect(useChatStore.getState().paused[SESSION]).toBe(true)
  })
})

describe('setUnfinishedTaskStatus (the single live unfinished-task overlay)', () => {
  const SESSION = 'sess-unfinished'

  beforeEach(() => {
    useChatStore.setState({ unfinishedTaskStatus: {}, taskActive: {} })
  })

  it('stores the status verbatim (including the empty-string "settled" value)', () => {
    useChatStore.getState().setUnfinishedTaskStatus(SESSION, 'failed')
    expect(useChatStore.getState().unfinishedTaskStatus[SESSION]).toBe('failed')

    useChatStore.getState().setUnfinishedTaskStatus(SESSION, '')
    // '' is stored (not deleted) — it is the "settled" signal that overrides a
    // stale DB snapshot.
    expect(useChatStore.getState().unfinishedTaskStatus[SESSION]).toBe('')
    expect(SESSION in useChatStore.getState().unfinishedTaskStatus).toBe(true)
  })

  it('no-ops (same map reference) when the value already matches', () => {
    useChatStore.getState().setUnfinishedTaskStatus(SESSION, 'failed')
    const before = useChatStore.getState().unfinishedTaskStatus
    useChatStore.getState().setUnfinishedTaskStatus(SESSION, 'failed')
    expect(useChatStore.getState().unfinishedTaskStatus).toBe(before)
  })

  it('deletes the entry when passed undefined (restores "no live knowledge")', () => {
    // An optimistic-send rollback whose pre-send overlay was absent must DELETE
    // the key rather than write a defined '' that would outrank the DB snapshot.
    useChatStore.getState().setUnfinishedTaskStatus(SESSION, 'failed')
    useChatStore.getState().setUnfinishedTaskStatus(SESSION, undefined)
    expect(SESSION in useChatStore.getState().unfinishedTaskStatus).toBe(false)
  })

  it('no-ops (same map reference) when deleting an already-absent entry', () => {
    const before = useChatStore.getState().unfinishedTaskStatus
    useChatStore.getState().setUnfinishedTaskStatus(SESSION, undefined)
    expect(useChatStore.getState().unfinishedTaskStatus).toBe(before)
  })

  it('setTaskActive(true) pins the overlay to "" so a stale DB status cannot stick', () => {
    useChatStore.getState().setUnfinishedTaskStatus(SESSION, 'failed')
    useChatStore.getState().setTaskActive(SESSION, true)
    expect(useChatStore.getState().unfinishedTaskStatus[SESSION]).toBe('')
    expect(useChatStore.getState().taskActive[SESSION]).toBe(true)
  })

  it('setTaskActive(false) leaves the overlay untouched (the terminal event owns it)', () => {
    useChatStore.getState().setUnfinishedTaskStatus(SESSION, 'failed')
    useChatStore.getState().setTaskActive(SESSION, false)
    expect(useChatStore.getState().unfinishedTaskStatus[SESSION]).toBe('failed')
  })
})

describe('scroll position save/clear (session-switch reading positions)', () => {
  beforeEach(() => {
    useChatStore.setState({ scrollPositions: {} })
  })

  it('saves a position per session and overwrites it on a later save', () => {
    const store = useChatStore.getState()
    store.saveScrollPosition('s1', { scrollTop: 4200, scrollHeight: 10000 })
    store.saveScrollPosition('s2', { scrollTop: 0, scrollHeight: 500 })
    store.saveScrollPosition('s1', { scrollTop: 5000, scrollHeight: 12000 })
    const positions = useChatStore.getState().scrollPositions
    expect(positions['s1']).toEqual({ scrollTop: 5000, scrollHeight: 12000 })
    expect(positions['s2']).toEqual({ scrollTop: 0, scrollHeight: 500 })
  })

  it('keeps the map reference when the saved position is unchanged (React #185)', () => {
    useChatStore.getState().saveScrollPosition('s1', { scrollTop: 4200, scrollHeight: 10000 })
    const before = useChatStore.getState().scrollPositions
    useChatStore.getState().saveScrollPosition('s1', { scrollTop: 4200, scrollHeight: 10000 })
    expect(useChatStore.getState().scrollPositions).toBe(before)
  })

  it('clear removes only the targeted session entry', () => {
    const store = useChatStore.getState()
    store.saveScrollPosition('s1', { scrollTop: 1, scrollHeight: 10 })
    store.saveScrollPosition('s2', { scrollTop: 2, scrollHeight: 20 })
    store.clearScrollPosition('s1')
    const positions = useChatStore.getState().scrollPositions
    expect(positions['s1']).toBeUndefined()
    expect(positions['s2']).toEqual({ scrollTop: 2, scrollHeight: 20 })
  })

  it('clear on a session without a saved position is a no-op (same map reference)', () => {
    useChatStore.getState().saveScrollPosition('s1', { scrollTop: 1, scrollHeight: 10 })
    const before = useChatStore.getState().scrollPositions
    useChatStore.getState().clearScrollPosition('missing')
    expect(useChatStore.getState().scrollPositions).toBe(before)
  })
})

describe('step context tokens (per-step context_fill used/max totals)', () => {
  const STEP_TOKENS = { used_tokens: 8500, max_tokens: 20000 }

  beforeEach(() => {
    useChatStore.setState({ stepContextFill: {}, stepContextTokens: {} })
  })

  it('setStepContextTokens writes a step entry without touching fills', () => {
    const store = useChatStore.getState()
    store.setStepContextFill('s1', 'step_1', 42.5)
    store.setStepContextTokens('s1', 'step_1', STEP_TOKENS)
    expect(useChatStore.getState().stepContextTokens).toEqual({
      s1: { step_1: { used_tokens: 8500, max_tokens: 20000 } },
    })
    expect(useChatStore.getState().stepContextFill).toEqual({ s1: { step_1: 42.5 } })
  })

  it('setStepContextTokens merges partial totals without coercing absent fields', () => {
    // The optional-spread guard in handleContextFill only forwards fields that
    // are actually present; merge semantics must keep the previous value for
    // the absent one (no 0/undefined coercion — same pattern as setSessionTokens).
    const store = useChatStore.getState()
    store.setStepContextTokens('s1', 'step_1', STEP_TOKENS)
    store.setStepContextTokens('s1', 'step_1', { used_tokens: 9000 })
    expect(useChatStore.getState().stepContextTokens).toEqual({
      s1: { step_1: { used_tokens: 9000, max_tokens: 20000 } },
    })
  })

  it('setStepContextTokens keeps other sessions/steps isolated', () => {
    const store = useChatStore.getState()
    store.setStepContextTokens('s1', 'step_1', STEP_TOKENS)
    store.setStepContextTokens('s2', 'step_1', { used_tokens: 1, max_tokens: 2 })
    store.setStepContextTokens('s1', 'step_2', { used_tokens: 3, max_tokens: 4 })
    expect(useChatStore.getState().stepContextTokens).toEqual({
      s1: {
        step_1: { used_tokens: 8500, max_tokens: 20000 },
        step_2: { used_tokens: 3, max_tokens: 4 },
      },
      s2: { step_1: { used_tokens: 1, max_tokens: 2 } },
    })
  })

  it('clearStepContextFill clears both the fill and the token maps for the session', () => {
    const store = useChatStore.getState()
    store.setStepContextFill('s1', 'step_1', 42.5)
    store.setStepContextTokens('s1', 'step_1', STEP_TOKENS)
    store.setStepContextFill('s2', 'step_1', 10)
    store.setStepContextTokens('s2', 'step_1', { used_tokens: 1, max_tokens: 2 })
    useChatStore.getState().clearStepContextFill('s1')
    expect(useChatStore.getState().stepContextFill).toEqual({ s2: { step_1: 10 } })
    expect(useChatStore.getState().stepContextTokens).toEqual({
      s2: { step_1: { used_tokens: 1, max_tokens: 2 } },
    })
  })

  it('clearStepContextFill still clears the token map when no fills are recorded', () => {
    // The maps can diverge (a session whose steps only ever reported
    // used/max): the shared clear must not bail out on the empty fill map.
    const store = useChatStore.getState()
    store.setStepContextTokens('s1', 'step_1', STEP_TOKENS)
    useChatStore.getState().clearStepContextFill('s1')
    expect(useChatStore.getState().stepContextFill).toEqual({})
    expect(useChatStore.getState().stepContextTokens).toEqual({})
  })

  it('clearStepContextFill on a session with neither map populated is a no-op', () => {
    const store = useChatStore.getState()
    store.setStepContextFill('s2', 'step_1', 10)
    store.setStepContextTokens('s2', 'step_1', STEP_TOKENS)
    const before = useChatStore.getState()
    useChatStore.getState().clearStepContextFill('missing')
    // State identity preserved — no new state object, no subscriber sweep.
    expect(useChatStore.getState()).toBe(before)
  })

  it('dropSessions removes several sessions from both maps in one update and leaves others intact', () => {
    const store = useChatStore.getState()
    store.setStepContextFill('s1', 'step_1', 42.5)
    store.setStepContextTokens('s1', 'step_1', STEP_TOKENS)
    store.setStepContextFill('s2', 'step_1', 10)
    store.setStepContextTokens('s2', 'step_1', { used_tokens: 1, max_tokens: 2 })
    store.setStepContextFill('s3', 'step_1', 90)
    store.setStepContextTokens('s3', 'step_1', STEP_TOKENS)
    useChatStore.getState().dropSessions(['s1', 's3'])
    expect(useChatStore.getState().stepContextFill).toEqual({ s2: { step_1: 10 } })
    expect(useChatStore.getState().stepContextTokens).toEqual({
      s2: { step_1: { used_tokens: 1, max_tokens: 2 } },
    })
  })

  it('dropSessions with a mixed batch drops only the known ids', () => {
    const store = useChatStore.getState()
    store.setStepContextFill('s1', 'step_1', 42.5)
    store.setStepContextFill('s2', 'step_1', 10)
    useChatStore.getState().dropSessions(['s1', 'unknown-a'])
    expect(useChatStore.getState().stepContextFill).toEqual({ s2: { step_1: 10 } })
  })

  it('dropSessions is a no-op for an all-unknown batch (state identity preserved)', () => {
    const store = useChatStore.getState()
    store.setStepContextFill('s1', 'step_1', 42.5)
    store.setStepContextTokens('s1', 'step_1', STEP_TOKENS)
    const before = useChatStore.getState()
    useChatStore.getState().dropSessions(['unknown-a', 'unknown-b'])
    expect(useChatStore.getState()).toBe(before)
  })
})
