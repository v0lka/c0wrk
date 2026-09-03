import { describe, it, expect } from 'vitest'
import { bookmarkKey, bookmarkDefaultTitle, flattenDisplayItems, indexDisplayItems } from './bookmarks'
import type { DisplayItem } from '@/types/messages'

const user: DisplayItem = {
  kind: 'user',
  message: { id: 'user-1', sessionId: 's1', type: 'user', content: 'Hello world', timestamp: 0 },
}

const tool: DisplayItem = {
  kind: 'tool',
  id: 'tool-1',
  toolName: 'bash_exec',
  args: '{}',
  status: 'success',
}

const planStep: DisplayItem = {
  kind: 'plan_step',
  id: 'ps-1',
  stepId: 'step_1',
  stepNum: 1,
  title: 'Implement auth',
  status: 'running',
  children: [tool],
}

describe('bookmarkKey', () => {
  it('uses the message id for message-backed items', () => {
    expect(bookmarkKey(user)).toBe('user-1')
  })

  it('uses the item id for synthetic items', () => {
    expect(bookmarkKey(tool)).toBe('tool-1')
  })
})

describe('bookmarkDefaultTitle', () => {
  it('uses message content for user messages', () => {
    expect(bookmarkDefaultTitle(user)).toBe('Hello world')
  })

  it('uses the tool name for tool cards', () => {
    expect(bookmarkDefaultTitle(tool)).toBe('bash_exec')
  })

  it('formats plan steps', () => {
    expect(bookmarkDefaultTitle(planStep)).toBe('Step 1: Implement auth')
  })

  it('collapses whitespace without a fixed-length cap (truncation is CSS-based)', () => {
    const long = 'a'.repeat(200)
    const title = bookmarkDefaultTitle({ kind: 'user', message: { id: 'u', sessionId: 's', type: 'user', content: `  ${long}  `, timestamp: 0 } })
    expect(title).toBe(long)
    expect(title).not.toContain('  ')
    expect(title).not.toContain('…')
  })
})

describe('flattenDisplayItems / indexDisplayItems', () => {
  it('recurses into plan_step and subagent children', () => {
    const flat = flattenDisplayItems([planStep])
    expect(flat.map((i) => bookmarkKey(i))).toEqual(['ps-1', 'tool-1'])
  })

  it('indexes every item by its bookmark key', () => {
    const index = indexDisplayItems([planStep])
    expect(index.get('ps-1')).toBe(planStep)
    expect(index.get('tool-1')).toBe(tool)
  })
})
