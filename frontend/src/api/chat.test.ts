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

import { getPendingActions, sendMessage, resumeTask } from '@/api/chat'

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

// sendMessage must forward activeSkills + activeAgents to the backend binding
// in the EXACT argument positions the Go SendMessage expects (arg index 3 and
// 4). A positional drift here silently drops #mentions / /skills before they
// reach HandleOptions.
describe('sendMessage forwards skill and agent refs', () => {
  beforeEach(() => {
    delete mockApp.SendMessage
  })

  it('passes activeAgents through in position 4 (and activeSkills in position 3)', async () => {
    const calls: unknown[][] = []
    mockApp.SendMessage = vi.fn((...args: unknown[]) => {
      calls.push(args)
      return Promise.resolve()
    })

    await sendMessage('sess-1', 'review this #code-reviewer', ['explore'], ['code-reviewer'])

    expect(mockApp.SendMessage).toHaveBeenCalledTimes(1)
    const args = calls[0]!
    expect(args[0]).toBe('sess-1')
    expect(args[1]).toBe('review this #code-reviewer')
    expect(args[2]).toEqual(['explore'])
    // activeAgents must land in position 4 — this is the #mention wiring.
    expect(args[3]).toEqual(['code-reviewer'])
  })

  it('defaults activeAgents and activeSkills to empty arrays when omitted', async () => {
    const calls: unknown[][] = []
    mockApp.SendMessage = vi.fn((...args: unknown[]) => {
      calls.push(args)
      return Promise.resolve()
    })

    await sendMessage('sess-1', 'plain message')

    const args = calls[0]!
    expect(args[2]).toEqual([])
    expect(args[3]).toEqual([])
  })
})

describe('resumeTask forwards model/reasoning overrides', () => {
  beforeEach(() => {
    delete mockApp.ResumeTask
  })

  it('passes modelOverride and reasoningOverride to the binding', async () => {
    const calls: unknown[][] = []
    mockApp.ResumeTask = vi.fn((...args: unknown[]) => {
      calls.push(args)
      return Promise.resolve()
    })

    await resumeTask('sess-1', 'provider/gpt-4o', 'high')

    expect(mockApp.ResumeTask).toHaveBeenCalledTimes(1)
    const args = calls[0]!
    expect(args[0]).toBe('sess-1')
    expect(args[1]).toBe('provider/gpt-4o')
    expect(args[2]).toBe('high')
  })

  it('defaults overrides to empty strings when omitted', async () => {
    const calls: unknown[][] = []
    mockApp.ResumeTask = vi.fn((...args: unknown[]) => {
      calls.push(args)
      return Promise.resolve()
    })

    await resumeTask('sess-1')

    const args = calls[0]!
    expect(args[1]).toBe('')
    expect(args[2]).toBe('')
  })
})
