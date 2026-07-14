import { describe, it, expect } from 'vitest'
import { getReviewPromptResolution, reviewPromptResolved, unresolvedReviewPromptIds } from './messages'
import type { ChatMessageUI, MessageType } from './messages'

function makeMsg(id: string, type: MessageType, metadata?: Record<string, unknown>): ChatMessageUI {
  return { id, sessionId: 's1', type, content: '', metadata, timestamp: 0 }
}

describe('getReviewPromptResolution', () => {
  it('returns null for undefined metadata', () => {
    expect(getReviewPromptResolution(undefined)).toBeNull()
  })

  it('returns null for unresolved metadata', () => {
    expect(getReviewPromptResolution({ resolved: false })).toBeNull()
  })

  it('returns null for resolved without decision', () => {
    expect(getReviewPromptResolution({ resolved: true })).toBeNull()
  })

  it('returns null for unknown decision', () => {
    expect(getReviewPromptResolution({ resolved: true, decision: 'maybe' })).toBeNull()
  })

  it('returns "enter" for enter decision', () => {
    expect(getReviewPromptResolution({ resolved: true, decision: 'enter' })).toBe('enter')
  })

  it('returns "decline" for decline decision', () => {
    expect(getReviewPromptResolution({ resolved: true, decision: 'decline' })).toBe('decline')
  })
})

describe('reviewPromptResolved', () => {
  it('creates resolved enter metadata', () => {
    expect(reviewPromptResolved('enter')).toEqual({ resolved: true, decision: 'enter' })
  })

  it('creates resolved decline metadata', () => {
    expect(reviewPromptResolved('decline')).toEqual({ resolved: true, decision: 'decline' })
  })

  it('round-trips through getReviewPromptResolution', () => {
    const meta = reviewPromptResolved('enter')
    expect(getReviewPromptResolution(meta)).toBe('enter')
  })
})

describe('unresolvedReviewPromptIds', () => {
  it('returns an empty set when there are no review_prompt messages', () => {
    const ids = unresolvedReviewPromptIds([
      makeMsg('a', 'user'),
      makeMsg('b', 'assistant'),
    ])
    expect(ids.size).toBe(0)
  })

  it('collects IDs of unresolved review_prompt messages', () => {
    const ids = unresolvedReviewPromptIds([
      makeMsg('a', 'review_prompt'),
      makeMsg('b', 'review_prompt', { resolved: false }),
    ])
    expect(ids).toEqual(new Set(['a', 'b']))
  })

  it('excludes resolved review_prompt messages', () => {
    const ids = unresolvedReviewPromptIds([
      makeMsg('a', 'review_prompt'),
      makeMsg('b', 'review_prompt', reviewPromptResolved('enter')),
      makeMsg('c', 'review_prompt', reviewPromptResolved('decline')),
    ])
    expect(ids).toEqual(new Set(['a']))
  })

  it('ignores non-review_prompt messages', () => {
    const ids = unresolvedReviewPromptIds([
      makeMsg('a', 'plan_review'),
      makeMsg('b', 'tool_confirm'),
      makeMsg('c', 'user'),
    ])
    expect(ids.size).toBe(0)
  })
})
