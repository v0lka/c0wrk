// Unit tests for api/chat.ts getSessionHistory — the single-request history
// wrapper.
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

describe('getSessionHistory', () => {
  beforeEach(() => {
    delete mockApp.GetSessionHistory
  })

  it('forwards the session id and returns the full message array', async () => {
    const spy = vi.fn(() => Promise.resolve([row(1, 'user', 'hi')]))
    mockApp.GetSessionHistory = spy

    const messages = await getSessionHistory('s1')

    expect(spy).toHaveBeenCalledWith('s1')
    expect(messages).toHaveLength(1)
    expect(messages[0]!.content).toBe('hi')
  })

  it('normalizes a Go nil slice (null) to an empty array', async () => {
    mockApp.GetSessionHistory = vi.fn(() => Promise.resolve(null))

    expect(await getSessionHistory('s1')).toEqual([])
  })

  it('returns an empty array for a malformed response', async () => {
    mockApp.GetSessionHistory = vi.fn(() => Promise.resolve({ nope: true }))

    expect(await getSessionHistory('s1')).toEqual([])
  })
})
