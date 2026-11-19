import { describe, it, expect } from 'vitest'
import { joinTreePath, parentTreeDir, validateEntryName } from './fileTreeNames'

describe('validateEntryName', () => {
  it('accepts a plain new name among siblings', () => {
    expect(validateEntryName('new.txt', ['a.txt', 'b.txt'])).toBeNull()
  })

  it('rejects empty and whitespace-only names', () => {
    expect(validateEntryName('', [])).toContain('empty')
    expect(validateEntryName('   ', [])).toContain('empty')
  })

  it('rejects leading and trailing spaces', () => {
    expect(validateEntryName(' x ', [])).toContain('spaces')
  })

  it('rejects path separators on any platform', () => {
    expect(validateEntryName('a/b', [])).toContain('path separators')
    expect(validateEntryName('a\\b', [])).toContain('path separators')
  })

  it('rejects dot names', () => {
    expect(validateEntryName('.', [])).toContain('"."')
    expect(validateEntryName('..', [])).toContain('".."')
  })

  it('rejects sibling collisions case-insensitively and names the clashing entry', () => {
    expect(validateEntryName('A.TXT', ['a.txt'])).toContain('already exists')
    expect(validateEntryName('A.TXT', ['a.txt'])).toContain('a.txt')
  })
})

describe('joinTreePath / parentTreeDir', () => {
  it('joins a directory with a name using the tree separator', () => {
    // The vitest environment has no navigator.platform — the separator
    // defaults to POSIX "/".
    expect(joinTreePath('/ws', 'x.txt')).toBe('/ws/x.txt')
    expect(joinTreePath('/ws', 'sub')).toBe('/ws/sub')
  })

  it('returns the parent directory of a tree path', () => {
    expect(parentTreeDir('/ws/a.txt')).toBe('/ws')
    expect(parentTreeDir('/ws/sub/inner.txt')).toBe('/ws/sub')
  })
})
