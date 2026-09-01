import { describe, it, expect, beforeEach } from 'vitest'
import {
  hasUnresolvedHITL,
  deriveSessionIndicatorStatus,
  isSessionBusy,
} from './useSessionStatusIndicator'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { HITL_PROMPT_TYPES } from '@/lib/hitlTypes'
import type { ChatMessageUI, MessageType } from '@/types/messages'
import type { SessionInfo } from '@/types/models'

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
  it('returns idle when not running, not paused, and no messages', () => {
    expect(deriveSessionIndicatorStatus(false, false, [])).toBe('idle')
  })

  it('returns active when a task is running and no HITL prompt is pending', () => {
    expect(deriveSessionIndicatorStatus(true, false, [
      makeMsg({ type: 'user' }),
      makeMsg({ type: 'assistant' }),
    ])).toBe('active')
  })

  it('returns pending when an unresolved HITL prompt exists, even if task is active', () => {
    // A task blocked on a HITL prompt is not "actively processing" — the
    // awaiting-reaction state takes precedence over the running flag.
    expect(deriveSessionIndicatorStatus(true, false, [
      makeMsg({ type: 'user' }),
      makeMsg({ type: 'tool_confirm', metadata: { confirm_id: 'c1' } }),
    ])).toBe('pending')
  })

  it('returns active again once the HITL prompt is resolved', () => {
    expect(deriveSessionIndicatorStatus(true, false, [
      makeMsg({ type: 'tool_confirm', metadata: { resolved: true, decision: 'confirmed' } }),
    ])).toBe('active')
  })

  it('returns idle when the task finished and no HITL prompt remains', () => {
    expect(deriveSessionIndicatorStatus(false, false, [
      makeMsg({ type: 'user' }),
      makeMsg({ type: 'assistant' }),
    ])).toBe('idle')
  })

  it('returns pending for a non-running session with an unresolved ask_user', () => {
    // Simulates the background-session watcher path: a HITL event for a
    // session the user is not viewing still produces a pending message the
    // indicator must reflect — even though taskActive may have been reset.
    expect(deriveSessionIndicatorStatus(false, false, [
      makeMsg({ type: 'ask_user', metadata: { request_id: 'r1' } }),
    ])).toBe('pending')
  })

  it('returns pending for each of the four tracked HITL event types', () => {
    const types: MessageType[] = ['tool_confirm', 'ask_user', 'step_limit', 'plan_review']
    for (const type of types) {
      expect(deriveSessionIndicatorStatus(false, false, [makeMsg({ type })])).toBe('pending')
    }
  })

  it('returns paused when the task is cooperatively suspended', () => {
    // A paused task has taskActive=false; the gray dot distinguishes it from
    // a genuinely idle session.
    expect(deriveSessionIndicatorStatus(false, true, [])).toBe('paused')
  })

  it('returns paused even when the task was running (paused flag wins over running)', () => {
    // Defensive: even if both flags were momentarily true, the suspended
    // state is the more informative signal.
    expect(deriveSessionIndicatorStatus(true, true, [])).toBe('paused')
  })

  it('returns pending over paused when an unresolved HITL prompt exists', () => {
    // A suspended task with a lingering prompt awaiting the user: the
    // awaiting-reaction state is the most urgent signal.
    expect(deriveSessionIndicatorStatus(false, true, [
      makeMsg({ type: 'ask_user', metadata: { request_id: 'r1' } }),
    ])).toBe('pending')
  })
})

function makeSessionInfo(overrides: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id: 'sess-1',
    project_id: 'p1',
    name: 'Session',
    created_at: new Date(0).toISOString(),
    last_active_at: new Date(0).toISOString(),
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: '',
    family: '',
    has_unfinished_task: false,
    unfinished_task_status: '',
    ...overrides,
  }
}

describe('isSessionBusy', () => {
  beforeEach(() => {
    useChatStore.setState({ taskActive: {}, paused: {}, messages: {}, messageOrder: {} })
    useSessionStore.setState({ sessions: null })
  })

  it('returns false for an idle session with no unfinished task', () => {
    useSessionStore.setState({ sessions: [makeSessionInfo()] })
    expect(isSessionBusy('sess-1')).toBe(false)
  })

  it('returns true when a task is running', () => {
    useSessionStore.setState({ sessions: [makeSessionInfo()] })
    useChatStore.setState({ taskActive: { 'sess-1': true } })
    expect(isSessionBusy('sess-1')).toBe(true)
  })

  it('returns true when a task is paused', () => {
    useSessionStore.setState({ sessions: [makeSessionInfo()] })
    useChatStore.setState({ paused: { 'sess-1': true } })
    expect(isSessionBusy('sess-1')).toBe(true)
  })

  it('returns true for a session with an unfinished task', () => {
    useSessionStore.setState({ sessions: [makeSessionInfo({ has_unfinished_task: true })] })
    expect(isSessionBusy('sess-1')).toBe(true)
  })

  it('returns false when a running task is blocked on an unresolved HITL prompt (pending)', () => {
    const m = makeMsg({ type: 'tool_confirm' })
    useSessionStore.setState({ sessions: [makeSessionInfo()] })
    useChatStore.setState({
      taskActive: { 'sess-1': true },
      messages: { 'sess-1': { [m.id]: m } },
      messageOrder: { 'sess-1': [m.id] },
    })
    expect(isSessionBusy('sess-1')).toBe(false)
  })
})
