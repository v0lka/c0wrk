import { describe, it, expect } from 'vitest'
import { createPathMatcher, isInvalidRegex } from './pathFilter'

describe('createPathMatcher', () => {
  it('returns null for empty / whitespace-only text', () => {
    expect(createPathMatcher('', 'glob')).toBeNull()
    expect(createPathMatcher('   ', 'glob')).toBeNull()
    expect(createPathMatcher('', 'regex')).toBeNull()
  })

  it('returns null for an invalid regex', () => {
    expect(createPathMatcher('(', 'regex')).toBeNull()
    expect(createPathMatcher('[', 'regex')).toBeNull()
    expect(createPathMatcher('*[', 'regex')).toBeNull()
  })

  describe('glob mode', () => {
    it('matches by basename', () => {
      const matcher = createPathMatcher('*.ts', 'glob')!
      expect(matcher('src/a.ts')).toBe(true)
      expect(matcher('a.ts')).toBe(true)
    })

    it('matches by full path', () => {
      const matcher = createPathMatcher('src/**', 'glob')!
      expect(matcher('src/a.ts')).toBe(true)
      expect(matcher('src/deep/b.ts')).toBe(true)
      expect(matcher('lib/a.ts')).toBe(false)
    })

    it('does not match non-matching files', () => {
      const matcher = createPathMatcher('*.ts', 'glob')!
      expect(matcher('src/a.go')).toBe(false)
      expect(matcher('README.md')).toBe(false)
    })

    it('is case-insensitive', () => {
      const matcher = createPathMatcher('*.TS', 'glob')!
      expect(matcher('a.ts')).toBe(true)
    })
  })

  describe('regex mode', () => {
    it('matches by basename (anchored)', () => {
      const matcher = createPathMatcher('^a\\.ts$', 'regex')!
      expect(matcher('src/a.ts')).toBe(true)
      expect(matcher('a.ts')).toBe(true)
      expect(matcher('src/b.ts')).toBe(false)
    })

    it('matches by full path', () => {
      const matcher = createPathMatcher('src/.*\\.ts', 'regex')!
      expect(matcher('src/a.ts')).toBe(true)
      expect(matcher('lib/a.ts')).toBe(false)
    })

    it('is case-insensitive', () => {
      const matcher = createPathMatcher('readme', 'regex')!
      expect(matcher('README.md')).toBe(true)
    })
  })
})

describe('isInvalidRegex', () => {
  it('returns false for empty / whitespace-only text', () => {
    expect(isInvalidRegex('', 'regex')).toBe(false)
    expect(isInvalidRegex('   ', 'regex')).toBe(false)
  })

  it('returns false for glob mode regardless of text', () => {
    expect(isInvalidRegex('(', 'glob')).toBe(false)
    expect(isInvalidRegex('[', 'glob')).toBe(false)
    expect(isInvalidRegex('*.ts', 'glob')).toBe(false)
  })

  it('returns false for a valid regex', () => {
    expect(isInvalidRegex('src/.*\\.ts', 'regex')).toBe(false)
    expect(isInvalidRegex('^a\\.ts$', 'regex')).toBe(false)
  })

  it('returns true for an invalid regex', () => {
    expect(isInvalidRegex('(', 'regex')).toBe(true)
    expect(isInvalidRegex('[', 'regex')).toBe(true)
    expect(isInvalidRegex('*[', 'regex')).toBe(true)
  })
})
