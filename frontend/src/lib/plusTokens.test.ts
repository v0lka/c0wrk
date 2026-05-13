import { describe, it, expect } from 'vitest'
import { parsePlusTokens } from '@/lib/plusTokens'

describe('parsePlusTokens', () => {
  it('returns empty tokens and stripped query when no + markers', () => {
    expect(parsePlusTokens('hello world')).toEqual({ query: 'hello world', tokens: [] })
  })

  it('extracts a single +token at start', () => {
    expect(parsePlusTokens('+alpha hello')).toEqual({ query: 'hello', tokens: ['alpha'] })
  })

  it('extracts a single +token in the middle', () => {
    expect(parsePlusTokens('foo +bar baz')).toEqual({ query: 'foo baz', tokens: ['bar'] })
  })

  it('extracts multiple +tokens and collapses whitespace', () => {
    expect(parsePlusTokens('  find +MatcherFactory  +Rule  in code '))
      .toEqual({ query: 'find in code', tokens: ['MatcherFactory', 'Rule'] })
  })

  it('ignores a bare + with no following characters', () => {
    expect(parsePlusTokens('foo + bar')).toEqual({ query: 'foo + bar', tokens: [] })
  })

  it('handles pure +token input', () => {
    expect(parsePlusTokens('+only')).toEqual({ query: '', tokens: ['only'] })
  })

  it('returns empty for empty input', () => {
    expect(parsePlusTokens('')).toEqual({ query: '', tokens: [] })
  })
})
