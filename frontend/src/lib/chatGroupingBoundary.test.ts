// Boundary tests for the history-rebuild consumers in lib/ (mass-bugfix
// cluster C9): persisted metadata rows are re-read raw (the live event guards
// only run on fresh events), so a malformed row must degrade to the same
// fallback the guard's absence produces — never a TypeError inside
// groupMessages, which runs on every message-list render.

import { describe, expect, it, vi } from 'vitest'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import type { PlanGroup } from '@/types/models'
import {
  handlePlanStepStart,
  handleReflection,
  handleStepTodoUpdate,
  handleSubAgentLaunch,
  handleToolCall,
  handleToolResult,
} from './chatGroupingHandlers'
import { normalizeThoughtContent, parsePlanSteps } from './chatUtilsHelpers'
import { groupMessages, rebuildPlanFromHistory } from './chatUtils'

function msg(partial: Partial<ChatMessageUI> & { type: ChatMessageUI['type'] }): ChatMessageUI {
  return { id: 'm1', sessionId: 's1', content: '', timestamp: 0, ...partial }
}

describe('#112 parsePlanSteps (persisted plan metadata)', () => {
  it('returns [] for a missing / non-array steps field (the old || [] kept truthy non-arrays)', () => {
    expect(parsePlanSteps(undefined)).toEqual([])
    expect(parsePlanSteps({})).toEqual([])
    expect(parsePlanSteps({ steps: {} })).toEqual([])
    expect(parsePlanSteps({ steps: 'x' })).toEqual([])
    expect(parsePlanSteps({ steps: null })).toEqual([])
  })

  it('keeps well-formed steps and drops malformed elements (null element used to throw on s.id)', () => {
    const steps = parsePlanSteps({
      steps: [
        { id: 'step_1', description: 'a', status: 'pending' },
        null,
        { description: 5, status: 'pending' },
        { id: 'step_2', description: 'b', status: 'pending', summary: 'B', depends_on: null },
      ],
    })
    expect(steps.map((s) => s.id)).toEqual(['step_1', 'step_2'])
  })
})

describe('#112 groupMessages / rebuildPlanFromHistory on malformed plan metadata', () => {
  it('groupMessages does not throw on a truthy non-array steps object', () => {
    const messages = [msg({ type: 'plan', metadata: { steps: { 0: { id: 'step_1' } } } })]
    expect(() => groupMessages(messages)).not.toThrow()
  })

  it('groupMessages does not throw on a null step element and still indexes valid ones', () => {
    const messages = [msg({ type: 'plan', metadata: { steps: [null, { id: 'step_1', description: 'a', status: 'pending' }] } })]
    let items: DisplayItem[] = []
    expect(() => { items = groupMessages(messages).items }).not.toThrow()
    void items
  })

  it('rebuildPlanFromHistory degrades to an empty plan on malformed steps', () => {
    const store = { clearPlan: vi.fn(), setPlan: vi.fn() }
    rebuildPlanFromHistory(
      [msg({ type: 'plan', metadata: { steps: { a: 1 } } })],
      store,
    )
    expect(store.clearPlan).toHaveBeenCalled()
    expect(store.setPlan).not.toHaveBeenCalled()
  })

  it('rebuildPlanFromHistory still builds the group from valid steps', () => {
    const store = { clearPlan: vi.fn(), setPlan: vi.fn() }
    rebuildPlanFromHistory(
      [msg({ type: 'plan', metadata: { steps: [{ id: 'step_1', description: 'a', status: 'pending' }] } })],
      store,
    )
    expect(store.setPlan).toHaveBeenCalledTimes(1)
    const group = store.setPlan.mock.calls[0]![0] as PlanGroup
    expect(group.items).toHaveLength(1)
    expect(group.items[0]!.id).toBe('step_1')
  })
})

describe('#113 handleStepTodoUpdate on malformed persisted items', () => {
  const setup = () => ({
    openSteps: new Map(),
    items: [] as DisplayItem[],
    checklists: new Map<string, { item: DisplayItem & { kind: 'checklist' }; container: DisplayItem[] }>(),
  })

  it('ignores a truthy non-array items (the old length probe kept strings)', () => {
    const s = setup()
    expect(() =>
      handleStepTodoUpdate(msg({ type: 'step_todo_update' }), { step_id: '', items: 'abc' }, s.openSteps, s.items, s.checklists),
    ).not.toThrow()
    expect(s.items).toHaveLength(0)
  })

  it('drops malformed elements and keeps well-formed ones', () => {
    const s = setup()
    handleStepTodoUpdate(
      msg({ type: 'step_todo_update' }),
      { step_id: '', items: [{ text: 'a', checked: false }, null, { text: 5, checked: true }] },
      s.openSteps, s.items, s.checklists,
    )
    const checklist = s.items[0]
    expect(checklist).toBeDefined()
    expect(checklist && checklist.kind === 'checklist' ? checklist.items : []).toEqual([
      { text: 'a', checked: false },
    ])
  })

  it('still ignores an empty item list', () => {
    const s = setup()
    handleStepTodoUpdate(msg({ type: 'step_todo_update' }), { step_id: '', items: [] }, s.openSteps, s.items, s.checklists)
    expect(s.items).toHaveLength(0)
  })
})

describe('#122/#184 handleReflection on malformed persisted fields', () => {
  it('degrades a truthy non-array insights to [] instead of hypotheses.map throwing', () => {
    const items: DisplayItem[] = []
    handleReflection(msg({ type: 'reflection' }), { summary: 's', attempt: 1, insights: 'abc' }, new Map(), items)
    const item = items[0]
    expect(item && item.kind === 'reflection' ? item.hypotheses : undefined).toEqual([])
  })

  it('degrades non-string rendered fields to "" and keeps string insights elements only', () => {
    const items: DisplayItem[] = []
    handleReflection(
      msg({ type: 'reflection' }),
      { summary: { x: 1 }, attempt: 1, insights: ['a', 2, null], suggested_action: 3 },
      new Map(), items,
    )
    const item = items[0]
    expect(item && item.kind === 'reflection' ? item.summary : undefined).toBe('')
    expect(item && item.kind === 'reflection' ? item.suggestedAction : undefined).toBe('')
    expect(item && item.kind === 'reflection' ? item.hypotheses : undefined).toEqual(['a'])
  })
})

describe('#167/#168/#180/#126 handleToolCall / handleToolResult on malformed persisted metadata', () => {
  it('coerces a truthy non-string tool/args/source instead of throwing in the tool cards', () => {
    const items: DisplayItem[] = []
    const push = (item: DisplayItem): DisplayItem[] => {
      items.push(item)
      return items
    }
    handleToolCall(
      msg({ type: 'tool_call' }),
      { step: 1, tool: { name: 'x' }, args: { path: 'a' }, source: ['mcp:s'], result_len: 3, completed: true, result: { body: 1 } },
      undefined,
      new Map(), new Map(), new Map(), push, new Map(),
    )
    const item = items[0]
    expect(item && item.kind === 'tool' ? item.toolName : undefined).toBe('Tool')
    expect(item && item.kind === 'tool' ? item.args : undefined).toBe('')
    expect(item && item.kind === 'tool' ? item.source : undefined).toBeUndefined()
    expect(item && item.kind === 'tool' ? item.result : undefined).toBeUndefined()
  })

  it('keeps well-formed values and the subagent/finish routing intact', () => {
    const items: DisplayItem[] = []
    const push = (item: DisplayItem): DisplayItem[] => {
      items.push(item)
      return items
    }
    handleToolCall(
      msg({ type: 'tool_call' }),
      { step: 1, tool: 'read_file', args: '{}', source: 'mcp:srv', attachment_name: 'f.txt' },
      undefined,
      new Map(), new Map(), new Map(), push, new Map(),
    )
    const item = items[0]
    expect(item && item.kind === 'tool' ? item.toolName : undefined).toBe('read_file')
    expect(item && item.kind === 'tool' ? item.source : undefined).toBe('mcp:srv')
    expect(item && item.kind === 'tool' ? item.attachmentName : undefined).toBe('f.txt')
  })

  it('handleToolResult degrades a truthy non-string result to undefined', () => {
    const toolItem = { kind: 'tool', id: 't1', toolName: 'read_file', args: '', status: 'running', children: [] } as unknown as DisplayItem & { kind: 'tool' }
    const byKey = new Map([[':1', toolItem]])
    handleToolResult(
      { step: 1, result: { body: 1 }, result_len: 3, plan_step_id: undefined },
      new Map([[':1', toolItem]]),
      new Map(),
    )
    expect(byKey.get(':1')!.kind === 'tool' ? (byKey.get(':1') as { result?: string }).result : undefined).toBeUndefined()
  })
})

describe('#127/#169 plan_step_start / subagent_launch persisted strings', () => {
  it('handlePlanStepStart coerces a truthy non-string summary (used to call .trim())', () => {
    const items: DisplayItem[] = []
    handlePlanStepStart(
      msg({ type: 'plan_step_start' }),
      { step_id: 's1', description: 'd', summary: 42 },
      new Map(), new Map(), new Map(), items,
    )
    const item = items[0]
    expect(item && item.kind === 'plan_step' ? item.title : undefined).toBe('d')
  })

  it('handleSubAgentLaunch coerces a truthy non-string description', () => {
    const items: DisplayItem[] = []
    handleSubAgentLaunch(
      msg({ type: 'subagent_launch' }),
      { step_id: 's1', description: { text: 'x' } },
      new Map(), items,
    )
    const item = items[0]
    expect(item && item.kind === 'subagent' ? item.title : undefined).toBe('s1')
    expect(item && item.kind === 'subagent' ? item.description : undefined).toBeUndefined()
  })
})

describe('#123 normalizeThoughtContent defensive narrowing', () => {
  it('returns "" for a non-string input instead of throwing on .trim()', () => {
    expect(normalizeThoughtContent(42 as unknown as string)).toBe('')
    expect(normalizeThoughtContent(null as unknown as string)).toBe('')
  })

  it('still normalizes placeholder/empty strings and passes real content through', () => {
    expect(normalizeThoughtContent('')).toBe('')
    expect(normalizeThoughtContent('  (proceeding) ')).toBe('')
    expect(normalizeThoughtContent(' real ')).toBe(' real ')
  })
})

describe('#183 groupMessages narrows persisted thought reasoning', () => {
  it('drops a truthy non-string reasoning instead of handing it to ThoughtBlock', () => {
    const messages = [msg({ type: 'thought', content: 'c', metadata: { step_num: 1, reasoning: { r: 1 } } })]
    const { items } = groupMessages(messages)
    const item = items[0]
    expect(item && item.kind === 'thought' ? item.reasoning : undefined).toBeUndefined()
  })

  it('keeps a well-formed reasoning string', () => {
    const messages = [msg({ type: 'thought', content: 'c', metadata: { step_num: 1, reasoning: 'r' } })]
    const { items } = groupMessages(messages)
    const item = items[0]
    expect(item && item.kind === 'thought' ? item.reasoning : undefined).toBe('r')
  })
})
