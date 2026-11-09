import { describe, it, expect, beforeEach } from 'vitest'
import { useGoalStore } from '@/stores/goalStore'
import { handleGoalSuggestionEvent } from '@/hooks/events/goalHandlers'
import { isGoalSuggestionData } from '@/types/events'
import { groupMessages } from '@/lib/chatUtils'
import type { ChatMessageUI } from '@/types/messages'
import type { GoalSuggestionData } from '@/types/events'
import { useChatStore } from '@/stores/chatStore'

const suggestion: GoalSuggestionData = {
  request_id: 'req-gs-1',
  session_id: 'sess-1',
  reason: 'Запрос описывает многошаговую задачу с проверяемым критерием завершения',
}

describe('goalStore pendingSuggestion slice', () => {
  beforeEach(() => {
    useGoalStore.getState().clearAll()
  })

  it('setPendingSuggestion writes the per-session entry; the selector returns the direct reference', () => {
    useGoalStore.getState().setPendingSuggestion('sess-1', suggestion)
    expect(useGoalStore.getState().pendingSuggestion['sess-1']).toBe(suggestion)
  })

  it('clearPendingSuggestion removes only that session', () => {
    useGoalStore.getState().setPendingSuggestion('sess-1', suggestion)
    const other = { ...suggestion, request_id: 'req-2' }
    useGoalStore.getState().setPendingSuggestion('sess-2', other)
    useGoalStore.getState().clearPendingSuggestion('sess-1')
    expect(useGoalStore.getState().pendingSuggestion['sess-1']).toBeUndefined()
    expect(useGoalStore.getState().pendingSuggestion['sess-2']).toBe(other)
  })

  it('clearGoal clears the pending suggestion alongside the rest of the session state', () => {
    useGoalStore.getState().setPendingSuggestion('sess-1', suggestion)
    useGoalStore.getState().clearGoal('sess-1')
    expect(useGoalStore.getState().pendingSuggestion['sess-1']).toBeUndefined()
  })

  it('clearAll empties the slice', () => {
    useGoalStore.getState().setPendingSuggestion('sess-1', suggestion)
    useGoalStore.getState().clearAll()
    expect(useGoalStore.getState().pendingSuggestion).toEqual({})
  })
})

describe('isGoalSuggestionData', () => {
  it('accepts a well-formed payload and rejects malformed ones', () => {
    expect(isGoalSuggestionData({ request_id: 'r', session_id: 's', reason: 'why' })).toBe(true)
    expect(isGoalSuggestionData({ request_id: 'r', session_id: 's' })).toBe(false)
    expect(isGoalSuggestionData({ request_id: 1, session_id: 's', reason: 'why' })).toBe(false)
    expect(isGoalSuggestionData(null)).toBe(false)
  })
})

describe('handleGoalSuggestionEvent', () => {
  beforeEach(() => {
    useGoalStore.getState().clearAll()
    useChatStore.setState({ messages: {} })
  })

  it('writes the goal store entry AND a deterministic-id chat message', () => {
    handleGoalSuggestionEvent('sess-1', suggestion)

    expect(useGoalStore.getState().pendingSuggestion['sess-1']).toEqual(suggestion)

    const order = useChatStore.getState().messageOrder['sess-1'] ?? []
    const byId = useChatStore.getState().messages['sess-1'] ?? {}
    expect(order).toHaveLength(1)
    const added = byId[order[0]!]!
    expect(added.id).toBe(`goal-suggestion-${suggestion.request_id}`)
    expect(added.type).toBe('goal_suggestion')
    expect(added.metadata).toMatchObject({ request_id: suggestion.request_id, reason: suggestion.reason, resolved: false })
    // The re-emission of the same request is idempotent at the id level (the
    // chat store dedupes by id), so the live card and a history reload share
    // one row.
    handleGoalSuggestionEvent('sess-1', suggestion)
    expect(useChatStore.getState().messageOrder['sess-1']).toHaveLength(1)
  })

  it('re-asserts taskActive: the gate blocks the task, so no step_start can heal a stale false flip', () => {
    // The suggestion-gate wait emits NO step_start (execution never started),
    // so the arrival itself is the only live-proof site that can re-open the
    // work block a stale runtime-status snapshot settled to `interrupted`.
    useChatStore.setState({ taskActive: { ...useChatStore.getState().taskActive, 'sess-1': false } })
    handleGoalSuggestionEvent('sess-1', suggestion)
    expect(useChatStore.getState().taskActive['sess-1']).toBe(true)
  })
})

describe('groupMessages goal_suggestion rendering', () => {
  function msg(overrides: Partial<ChatMessageUI>): ChatMessageUI {
    return {
      id: 'goal-suggestion-req-gs-1',
      sessionId: 'sess-1',
      type: 'goal_suggestion',
      content: suggestion.reason,
      metadata: { request_id: suggestion.request_id, reason: suggestion.reason, resolved: false },
      timestamp: 1,
      ...overrides,
    }
  }

  it('maps a pending goal_suggestion message to a goal_suggestion DisplayItem carrying the reason', () => {
    const items = groupMessages([msg({})]).items
    const item = items.find((i) => i.kind === 'goal_suggestion')
    expect(item).toBeDefined()
    if (item?.kind !== 'goal_suggestion') return
    expect(item.reason).toBe(suggestion.reason)
    expect(item.message.metadata).toMatchObject({ request_id: suggestion.request_id })
  })

  it('a resolved suggestion keeps its decision in the item message metadata', () => {
    const items = groupMessages([msg({ metadata: { request_id: suggestion.request_id, resolved: true, decision: 'decline' } })]).items
    const item = items.find((i) => i.kind === 'goal_suggestion')
    expect(item).toBeDefined()
    if (item?.kind !== 'goal_suggestion') return
    expect(item.message.metadata).toMatchObject({ decision: 'decline', resolved: true })
  })
})
