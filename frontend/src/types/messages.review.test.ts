import { describe, it, expect } from 'vitest'
import { getReviewPromptResolution, reviewPromptResolved } from './messages'

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
