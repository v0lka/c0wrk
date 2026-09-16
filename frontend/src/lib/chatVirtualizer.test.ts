// Unit tests for the virtualized-transcript navigation index resolution.
//
// The virtualizer mounts only visible rows, so navigation must map a step id /
// bookmark key to the TOP-LEVEL row index it lives in (a bookmark may anchor a
// nested child). These pure helpers do that mapping.
import { describe, it, expect } from 'vitest'
import { indexOfKey, indexOfStep } from './chatVirtualizer'
import type { DisplayItem, ChatMessageUI } from '@/types/messages'

function message(id: string): ChatMessageUI {
  return { id, sessionId: 's1', type: 'assistant', content: 'x', metadata: {}, timestamp: 0 }
}
function assistant(id: string): DisplayItem {
  return { kind: 'assistant', message: message(id) }
}
function planStep(id: string, stepId: string, children: DisplayItem[] = []): DisplayItem {
  return { kind: 'plan_step', id, stepId, stepNum: 1, title: 't', status: 'completed', children }
}

describe('indexOfStep', () => {
  it('finds the top-level plan-step row for a step id', () => {
    const items = [assistant('m0'), planStep('p1', 'step_1'), assistant('m2')]
    expect(indexOfStep(items, 'step_1')).toBe(1)
  })

  it('returns -1 for an unknown step id', () => {
    const items = [assistant('m0'), planStep('p1', 'step_1')]
    expect(indexOfStep(items, 'step_9')).toBe(-1)
  })
})

describe('indexOfKey', () => {
  it('finds the top-level row carrying the key', () => {
    const items = [assistant('m0'), planStep('p1', 'step_1'), assistant('m2')]
    expect(indexOfKey(items, 'm2')).toBe(2)
  })

  it('returns the top-level row for a NESTED child key', () => {
    const child = assistant('child-1')
    const items = [planStep('p1', 'step_1', [child]), assistant('m2')]
    // The child mounts inside the plan-step row, so navigation must target the
    // parent row (index 0) — a nested DOM node has no own virtualizer row.
    expect(indexOfKey(items, 'child-1')).toBe(0)
  })

  it('returns -1 for an unknown key', () => {
    expect(indexOfKey([assistant('m0')], 'nope')).toBe(-1)
  })
})
