import { describe, it, expect } from 'vitest'
import { totalCommentCount, hunkCommentKey, type SessionReviewState } from './reviewStore'

function makeState(overrides: Partial<SessionReviewState> = {}): SessionReviewState {
  return {
    status: 'active',
    generalComment: '',
    hunkComments: {},
    fileComments: {},
    loaded: true,
    ...overrides,
  }
}

describe('totalCommentCount', () => {
  it('returns 0 for empty state', () => {
    expect(totalCommentCount(makeState())).toBe(0)
  })

  it('counts general comment as 1', () => {
    expect(totalCommentCount(makeState({ generalComment: 'fix this' }))).toBe(1)
  })

  it('counts whitespace-only general as 0', () => {
    expect(totalCommentCount(makeState({ generalComment: '   ' }))).toBe(0)
  })

  it('counts hunk comments', () => {
    const state = makeState({
      hunkComments: {
        'a.go::hunk-0': 'bad naming',
        'b.go::hunk-1': 'missing test',
      },
    })
    expect(totalCommentCount(state)).toBe(2)
  })

  it('excludes whitespace-only hunk comments', () => {
    const state = makeState({
      hunkComments: {
        'a.go::hunk-0': 'bad naming',
        'b.go::hunk-1': '  ',
      },
    })
    expect(totalCommentCount(state)).toBe(1)
  })

  it('counts general + hunks together', () => {
    const state = makeState({
      generalComment: 'overall',
      hunkComments: { 'a.go::hunk-0': 'fix' },
    })
    expect(totalCommentCount(state)).toBe(2)
  })
})

describe('hunkCommentKey', () => {
  it('joins filePath and hunkId with ::', () => {
    expect(hunkCommentKey('src/main.go', 'hunk-3')).toBe('src/main.go::hunk-3')
  })

  it('handles paths with special chars', () => {
    expect(hunkCommentKey('src/a-b.go', 'hunk-0')).toBe('src/a-b.go::hunk-0')
  })
})
