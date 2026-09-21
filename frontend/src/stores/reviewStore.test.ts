// Unit tests for reviewStore helpers and the persist migration
//
// jsdom environment: the persistence-migration test exercises zustand's
// persist middleware, which resolves its default storage
// (`createJSONStorage(() => window.localStorage)`) at store-creation time.
// In the plain node environment `window` is undefined, persist bails out
// before attaching the `api.persist` handle, and `persist.rehydrate()` is
// unreachable — same reason gitPanelStore.test.ts runs under jsdom.
// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { totalCommentCount, hunkCommentKey, useReviewStore, type SessionReviewState } from './reviewStore'

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

describe('persistence migration', () => {
  it('strips the removed promptShownForTask key from a legacy localStorage snapshot', async () => {
    // Legacy persisted shape (pre-ADR-060): the now-removed post-task review
    // prompt flag sat alongside the surviving keys. zustand's default shallow
    // rehydrate merge would re-inject it into state; the migrate hook must
    // drop it so nothing stale lands in the store.
    localStorage.setItem(
      'c0wrk-review',
      JSON.stringify({
        state: {
          promptShownForTask: 'task-1',
          reviewLoopActive: { s1: true },
          diffViewMode: 'split',
        },
        version: 0,
      }),
    )
    await useReviewStore.persist.rehydrate()

    const state = useReviewStore.getState() as unknown as Record<string, unknown>
    expect(state.promptShownForTask).toBeUndefined()
    expect(useReviewStore.getState().reviewLoopActive).toEqual({ s1: true })
    expect(useReviewStore.getState().diffViewMode).toBe('split')
  })
})
