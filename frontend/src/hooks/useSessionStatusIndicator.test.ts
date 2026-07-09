import { describe, it, expect } from 'vitest'
import {
  hasUnresolvedHITL,
  deriveSessionIndicatorStatus,
} from './useSessionStatusIndicator'
import { HITL_PROMPT_TYPES } from '@/lib/hitlTypes'
import type { ChatMessageUI, MessageType } from '@/types/messages'

let counter = 0
function makeMsg(overrides: Partial<ChatMessageUI> & { type: MessageType }): ChatMessageUI {
  counter++
  return {
    id: `msg-${counter}`,
    sessionId: 'sess-1',
    content: '',
    timestamp: 1000 + counter,
    ...overrides,
  }
}

describe('hasUnresolvedHITL', () => {
  it('returns false for an empty message list', () => {
    expect(hasUnresolvedHITL([])).toBe(false)
  })

  it('returns false when only non-HITL messages exist', () => {
    expect(hasUnresolvedHITL([
      makeMsg({ type: 'user' }),
      makeMsg({ type: 'assistant' }),
      makeMsg({ type: 'tool_call' }),
    ])).toBe(false)
  })

  it('returns true for an unresolved tool_confirm', () => {
    expect(hasUnresolvedHITL([makeMsg({ type: 'tool_confirm' })])).toBe(true)
  })

  it('returns true for an unresolved ask_user', () => {
    expect(hasUnresolvedHITL([makeMsg({ type: 'ask_user' })])).toBe(true)
  })

  it('returns true for an unresolved step_limit', () => {
    expect(hasUnresolvedHITL([makeMsg({ type: 'step_limit' })])).toBe(true)
  })

  it('returns true for an unresolved plan_review', () => {
    expect(hasUnresolvedHITL([makeMsg({ type: 'plan_review' })])).toBe(true)
  })

  it('returns false when a HITL prompt is resolved', () => {
    expect(hasUnresolvedHITL([
      makeMsg({ type: 'tool_confirm', metadata: { resolved: true, decision: 'confirmed' } }),
      makeMsg({ type: 'ask_user', metadata: { resolved: true, answer: 'yes' } }),
    ])).toBe(false)
  })

  it('returns true when at least one HITL prompt is unresolved among many', () => {
    expect(hasUnresolvedHITL([
      makeMsg({ type: 'user' }),
      makeMsg({ type: 'tool_confirm', metadata: { resolved: true, decision: 'denied' } }),
      makeMsg({ type: 'assistant' }),
      makeMsg({ type: 'step_limit' }),
    ])).toBe(true)
  })

  it('does not treat task_failed_resumable as a HITL prompt', () => {
    // task_failed_resumable is a resume affordance, not one of the four HITL
    // event types the sidebar indicator tracks.
    expect(hasUnresolvedHITL([
      makeMsg({ type: 'task_failed_resumable', metadata: { resolved: false } }),
    ])).toBe(false)
    expect(HITL_PROMPT_TYPES.has('task_failed_resumable')).toBe(false)
  })
})

describe('deriveSessionIndicatorStatus', () => {
  it('returns idle when not running and no messages', () => {
    expect(deriveSessionIndicatorStatus(false, [])).toBe('idle')
  })

  it('returns active when a task is running and no HITL prompt is pending', () => {
    expect(deriveSessionIndicatorStatus(true, [
      makeMsg({ type: 'user' }),
      makeMsg({ type: 'assistant' }),
    ])).toBe('active')
  })

  it('returns pending when an unresolved HITL prompt exists, even if task is active', () => {
    // A task blocked on a HITL prompt is not "actively processing" — the
    // awaiting-reaction state takes precedence over the running flag.
    expect(deriveSessionIndicatorStatus(true, [
      makeMsg({ type: 'user' }),
      makeMsg({ type: 'tool_confirm', metadata: { confirm_id: 'c1' } }),
    ])).toBe('pending')
  })

  it('returns active again once the HITL prompt is resolved', () => {
    expect(deriveSessionIndicatorStatus(true, [
      makeMsg({ type: 'tool_confirm', metadata: { resolved: true, decision: 'confirmed' } }),
    ])).toBe('active')
  })

  it('returns idle when the task finished and no HITL prompt remains', () => {
    expect(deriveSessionIndicatorStatus(false, [
      makeMsg({ type: 'user' }),
      makeMsg({ type: 'assistant' }),
    ])).toBe('idle')
  })

  it('returns pending for a non-running session with an unresolved ask_user', () => {
    // Simulates the background-session watcher path: a HITL event for a
    // session the user is not viewing still produces a pending message the
    // indicator must reflect — even though taskActive may have been reset.
    expect(deriveSessionIndicatorStatus(false, [
      makeMsg({ type: 'ask_user', metadata: { request_id: 'r1' } }),
    ])).toBe('pending')
  })

  it('returns pending for each of the four tracked HITL event types', () => {
    const types: MessageType[] = ['tool_confirm', 'ask_user', 'step_limit', 'plan_review']
    for (const type of types) {
      expect(deriveSessionIndicatorStatus(false, [makeMsg({ type })])).toBe('pending')
    }
  })
})
