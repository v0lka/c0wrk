import { describe, it, expect } from 'vitest'
import { createPathMatcher, escapeGlobLiteral, isInvalidRegex } from './pathFilter'

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

describe('escapeGlobLiteral', () => {
  it('leaves a plain path untouched', () => {
    expect(escapeGlobLiteral('src/components/GitHistoryTab.tsx')).toBe(
      'src/components/GitHistoryTab.tsx',
    )
  })

  it('escapes every picomatch metacharacter, including the escape char', () => {
    expect(escapeGlobLiteral('a*b?.ts')).toBe('a\\*b\\?.ts')
    expect(escapeGlobLiteral('report[2026].md')).toBe('report\\[2026\\].md')
    expect(escapeGlobLiteral('+(*)')).toBe('\\+\\(\\*\\)')
    expect(escapeGlobLiteral('{a|b}!c@d+e')).toBe('\\{a\\|b\\}\\!c\\@d\\+e')
    expect(escapeGlobLiteral('win\\path')).toBe('win\\\\path')
  })

  it('an escaped repo-derived filename matches literally and cannot backtrack', () => {
    // 100 bytes — fits in a SINGLE filename component (the 255-byte POSIX
    // limit), so a cloned repository can carry it. On 4.0.4 the raw pattern
    // measured 63s+ of blocked main thread against the 30-char subject below
    // (GHSA-fc5q-39xc-vjpf; advisory withdrawn 2026-11-18, behavior real).
    const evilName = '+(*)'.repeat(20) + 'b'
    const matcher = createPathMatcher(escapeGlobLiteral(evilName), 'glob')!
    expect(matcher(evilName)).toBe(true)
    expect(matcher('repo/' + evilName)).toBe(true)
    // The false arm is the regression guard: against the raw (unescaped)
    // pattern this call backtracks catastrophically and would blow vitest's
    // 5s timeout — escaping must keep it instant.
    expect(matcher('a'.repeat(30))).toBe(false)
  })

  it('an escaped directory path keeps the trailing-separator scoping', () => {
    const dirName = 'pkg+(v1)'
    const matcher = createPathMatcher(escapeGlobLiteral(dirName) + '/', 'glob')!
    expect(matcher(dirName + '/a.ts')).toBe(true)
    expect(matcher(dirName + '-extra/a.ts')).toBe(false)
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
