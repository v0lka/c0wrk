import { describe, it, expect } from 'vitest'
import { extractSkillRefs } from './parseReferences'
import { fuzzyMatch, fuzzyFilter } from './fuzzyMatch'

describe('extractSkillRefs', () => {
  it('extracts a single skill ref', () => {
    expect(extractSkillRefs('Fix using /commit approach')).toEqual(['commit'])
  })

  it('extracts multiple skill refs', () => {
    expect(extractSkillRefs('/go-error-handling and /go-concurrency')).toEqual([
      'go-error-handling',
      'go-concurrency',
    ])
  })

  it('deduplicates skill refs', () => {
    expect(extractSkillRefs('/commit and again /commit')).toEqual(['commit'])
  })

  it('does not match mid-word slashes', () => {
    expect(extractSkillRefs('http://example.com')).toEqual([])
  })

  it('matches at start of text', () => {
    expect(extractSkillRefs('/my-skill is great')).toEqual(['my-skill'])
  })

  it('matches after newline', () => {
    expect(extractSkillRefs('line one\n/skill-two')).toEqual(['skill-two'])
  })

  it('returns empty array for no refs', () => {
    expect(extractSkillRefs('just plain text')).toEqual([])
  })
})

describe('fuzzyMatch', () => {
  it('matches exact substring', () => {
    const result = fuzzyMatch('chat', 'chat.ts')
    expect(result.match).toBe(true)
    expect(result.score).toBeGreaterThan(0)
  })

  it('matches subsequence', () => {
    const result = fuzzyMatch('cht', 'chat.ts')
    expect(result.match).toBe(true)
  })

  it('does not match impossible subsequence', () => {
    const result = fuzzyMatch('xyz', 'chat.ts')
    expect(result.match).toBe(false)
  })

  it('empty query matches everything', () => {
    const result = fuzzyMatch('', 'anything')
    expect(result.match).toBe(true)
  })

  it('scores word boundary matches higher', () => {
    const boundary = fuzzyMatch('c', 'src/components/chat.ts')
    const mid = fuzzyMatch('o', 'src/components/chat.ts')
    // 'c' at component boundary should score higher
    expect(boundary.score).toBeGreaterThanOrEqual(mid.score)
  })

  it('scores consecutive matches higher', () => {
    const consecutive = fuzzyMatch('cha', 'chat.ts')
    const spread = fuzzyMatch('c_a', 'c_hat_a.ts')
    expect(consecutive.score).toBeGreaterThan(spread.score)
  })
})

describe('fuzzyFilter', () => {
  const items = [
    { path: 'src/api/chat.ts' },
    { path: 'src/api/sessions.ts' },
    { path: 'src/components/chat/ChatInput.tsx' },
    { path: 'src/lib/utils.ts' },
  ]

  it('filters by subsequence', () => {
    const result = fuzzyFilter('chat', items, (i) => i.path)
    expect(result.length).toBe(2)
    expect(result.map((r) => r.path)).toContain('src/api/chat.ts')
    expect(result.map((r) => r.path)).toContain('src/components/chat/ChatInput.tsx')
  })

  it('returns all items for empty query (up to limit)', () => {
    const result = fuzzyFilter('', items, (i) => i.path, 2)
    expect(result.length).toBe(2)
  })

  it('respects limit', () => {
    const result = fuzzyFilter('s', items, (i) => i.path, 2)
    expect(result.length).toBeLessThanOrEqual(2)
  })

  it('returns empty for no matches', () => {
    const result = fuzzyFilter('zzz', items, (i) => i.path)
    expect(result.length).toBe(0)
  })
})
