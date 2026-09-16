// Unit tests for api/chat.ts getSessionHistory — the paged history wrapper.
import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import { getSessionHistory } from '@/api/chat'

function row(id: number, role: string, content: string) {
  return { id, session_id: 's1', role, content, metadata: {}, created_at: '2026-01-01T00:00:00Z' }
}

describe('getSessionHistory (paged)', () => {
  beforeEach(() => {
    delete mockApp.GetSessionHistory
  })

  it('forwards (sessionId, limit, before) and returns the page', async () => {
    const spy = vi.fn(() => Promise.resolve({
      messages: [row(1, 'user', 'hi')],
      next_cursor: 'c1',
      has_more: true,
    }))
    mockApp.GetSessionHistory = spy

    const page = await getSessionHistory('s1', 200, 'c0')

    expect(spy).toHaveBeenCalledWith('s1', 200, 'c0')
    expect(page.messages).toHaveLength(1)
    expect(page.next_cursor).toBe('c1')
    expect(page.has_more).toBe(true)
  })

  it('normalizes a Go nil slice (messages: null) to an empty array', async () => {
    mockApp.GetSessionHistory = vi.fn(() => Promise.resolve({
      messages: null,
      next_cursor: '',
      has_more: false,
    }))

    const page = await getSessionHistory('s1')

    expect(page.messages).toEqual([])
    expect(page.has_more).toBe(false)
  })

  it('rejects a malformed page (missing has_more) with an empty page', async () => {
    mockApp.GetSessionHistory = vi.fn(() => Promise.resolve({ messages: [], next_cursor: '' }))

    const page = await getSessionHistory('s1')

    expect(page).toEqual({ messages: [], next_cursor: '', has_more: false })
  })
})
