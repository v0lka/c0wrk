import { describe, it, expect } from 'vitest'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import { areDisplayItemsEqual, stabilizeDisplayItems } from './displayItemStability'

function msg(
  id: string,
  content: string,
  type: ChatMessageUI['type'] = 'assistant',
  metadata: Record<string, unknown> = {},
): ChatMessageUI {
  return { id, sessionId: 's1', type, content, metadata, timestamp: 0 }
}

type AssistantItem = Extract<DisplayItem, { kind: 'assistant' }>

function assistant(id: string, content: string): AssistantItem {
  return { kind: 'assistant', message: msg(id, content) }
}

/** A fresh wrapper around an existing message (as a re-group would produce). */
function wrapAssistant(message: ChatMessageUI): AssistantItem {
  return { kind: 'assistant', message }
}

describe('areDisplayItemsEqual', () => {
  it('treats a fresh wrapper around an unchanged message as equal (identity of message, not item)', () => {
    const message = msg('a1', 'hello')
    const a: DisplayItem = { kind: 'assistant', message }
    const b: DisplayItem = { kind: 'assistant', message }
    expect(a === b).toBe(false) // different wrapper objects…
    expect(areDisplayItemsEqual(a, b)).toBe(true) // …but equal content
  })

  it('is false when the underlying message changed (a new message object)', () => {
    const a = assistant('a1', 'v1')
    const b = assistant('a1', 'v2')
    expect(areDisplayItemsEqual(a, b)).toBe(false)
  })

  it('is false across different kinds', () => {
    const a = assistant('x', 'same text')
    const b: DisplayItem = { kind: 'error', message: msg('x', 'same text', 'error') }
    expect(areDisplayItemsEqual(a, b)).toBe(false)
  })

  it('compares tool cards field-by-field (a late result flips equality)', () => {
    const base = {
      kind: 'tool' as const,
      id: 't1',
      toolName: 'read_file',
      args: '{"path":"a"}',
      status: 'running' as const,
    }
    const running: DisplayItem = { ...base }
    const runningCopy: DisplayItem = { ...base }
    const done: DisplayItem = { ...base, status: 'success', result: 'contents' }
    expect(areDisplayItemsEqual(running, runningCopy)).toBe(true)
    expect(areDisplayItemsEqual(running, done)).toBe(false)
  })

  it('recurses into plan-step children (a changed child breaks equality)', () => {
    const childMessage = msg('c1', 'child output')
    const plan = (child: DisplayItem): DisplayItem => ({
      kind: 'plan_step',
      id: 'p1',
      stepId: 'step_1',
      stepNum: 1,
      title: 'Do the thing',
      status: 'running',
      children: [child],
    })
    const a = plan(wrapAssistant(childMessage))
    const b = plan(wrapAssistant(childMessage))
    const c = plan(wrapAssistant(msg('c1', 'changed output')))
    expect(areDisplayItemsEqual(a, b)).toBe(true)
    expect(areDisplayItemsEqual(a, c)).toBe(false)
  })
})

describe('stabilizeDisplayItems', () => {
  it('reuses the previous item object when nothing changed', () => {
    const a1 = assistant('a1', 'one')
    const a2 = assistant('a2', 'two')
    const prev: DisplayItem[] = [a1, a2]
    // A re-grouped rebuild: brand-new wrappers around the SAME message objects.
    const next: DisplayItem[] = [
      wrapAssistant(a1.message),
      wrapAssistant(a2.message),
    ]
    const out = stabilizeDisplayItems(prev, next)
    expect(out[0]).toBe(a1)
    expect(out[1]).toBe(a2)
  })

  it('keeps the changed item fresh while reusing its unchanged siblings', () => {
    const a1 = assistant('a1', 'one')
    const a2 = assistant('a2', 'two')
    const prev: DisplayItem[] = [a1, a2]
    const changed = assistant('a1', 'one edited')
    const out = stabilizeDisplayItems(prev, [wrapAssistant(changed.message), wrapAssistant(a2.message)])
    expect(out[0]).not.toBe(a1)
    expect(out[0]).toEqual(changed)
    expect(out[1]).toBe(a2)
  })

  it('passes through items whose key is new (appended message)', () => {
    const a1 = assistant('a1', 'one')
    const prev: DisplayItem[] = [a1]
    const a3 = assistant('a3', 'three')
    const out = stabilizeDisplayItems(prev, [wrapAssistant(a1.message), a3])
    expect(out[0]).toBe(a1)
    expect(out[1]).toBe(a3)
  })

  it('reuses unchanged children of a plan step whose status changed', () => {
    const child = assistant('c1', 'child output')
    const prev: DisplayItem[] = [{
      kind: 'plan_step', id: 'p1', stepId: 'step_1', stepNum: 1, title: 'T', status: 'running', children: [child],
    }]
    const next: DisplayItem[] = [{
      kind: 'plan_step', id: 'p1', stepId: 'step_1', stepNum: 1, title: 'T', status: 'completed',
      children: [wrapAssistant(child.message)],
    }]
    const out = stabilizeDisplayItems(prev, next)
    expect(out[0]).not.toBe(prev[0]) // status changed → fresh wrapper
    expect((out[0] as Extract<DisplayItem, { kind: 'plan_step' }>).children[0]).toBe(child)
  })

  it('returns the input untouched for an empty previous tree', () => {
    const next = [assistant('a1', 'one')]
    expect(stabilizeDisplayItems([], next)).toBe(next)
  })
})
