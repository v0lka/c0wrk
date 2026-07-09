// Unit tests for api/chat.ts — getPendingActions null-array normalization.
//
// Go's encoding/json marshals a nil slice to JSON `null` (not `[]`). The real
// backend (desktop/pending_actions.go GetPendingActions) used to return nil
// slices for any pending-action kind with no entries, producing e.g.
// {tool_confirms: null, ..., ask_user: [...]}. The frontend must treat those
// nulls as empty arrays — otherwise the shape guard rejects the whole response
// and HITL reconciliation on session switch is silently skipped, which is
// exactly how background-session prompt cards disappeared.

import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import { getPendingActions } from '@/api/chat'

describe('getPendingActions null-array normalization', () => {
  beforeEach(() => {
    delete mockApp.GetPendingActions
  })

  it('normalizes Go nil-slice nulls to empty arrays (the bug case)', async () => {
    // Mirrors the old backend output for a session with only an ask_user pending.
    mockApp.GetPendingActions = vi.fn(() => Promise.resolve({
      tool_confirms: null,
      step_limits: null,
      plan_approvals: null,
      ask_user: [{ request_id: 'r1', questions: [] }],
    }))

    const result = await getPendingActions('sess-1')

    expect(result).not.toBeNull()
    expect(result!.tool_confirms).toEqual([])
    expect(result!.step_limits).toEqual([])
    expect(result!.plan_approvals).toEqual([])
    expect(result!.ask_user).toHaveLength(1)
    expect(result!.ask_user[0]!.request_id).toBe('r1')
  })

  it('passes through well-formed arrays unchanged', async () => {
    mockApp.GetPendingActions = vi.fn(() => Promise.resolve({
      tool_confirms: [{ confirm_id: 'c1', tool: 'bash', args: '{}' }],
      step_limits: [],
      plan_approvals: [],
      ask_user: [],
    }))

    const result = await getPendingActions('sess-1')

    expect(result!.tool_confirms).toHaveLength(1)
    expect(result!.tool_confirms[0]!.confirm_id).toBe('c1')
    expect(result!.ask_user).toEqual([])
  })

  it('returns null for a genuinely malformed response', async () => {
    mockApp.GetPendingActions = vi.fn(() => Promise.resolve({ tool_confirms: 'not-an-array' }))

    const result = await getPendingActions('sess-1')

    expect(result).toBeNull()
  })
})
