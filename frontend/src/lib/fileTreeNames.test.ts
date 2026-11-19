import { afterEach, describe, expect, it, vi } from 'vitest'
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
  // Node >= 21 (CI pins 24) exposes a global navigator whose platform is the
  // host's legacy value ("MacIntel" on macOS, "Win32" on Windows runners), so
  // the helpers would otherwise join with "\" on Windows CI while these tests
  // assert "/" paths. Every case pins the signal explicitly, covering both
  // separators deterministically on any runner.
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  /** Pins the platform signal treePathSep() derives the separator from. */
  function stubPlatform(platform: string | undefined): void {
    vi.stubGlobal('navigator', platform === undefined ? undefined : { platform })
  }

  it('joins with "/" on a POSIX platform signal', () => {
    stubPlatform('MacIntel')
    expect(joinTreePath('/ws', 'x.txt')).toBe('/ws/x.txt')
    expect(joinTreePath('/ws', 'sub')).toBe('/ws/sub')
    expect(joinTreePath('/ws/', 'x.txt')).toBe('/ws/x.txt')
  })

  it('falls back to "/" when no platform signal exists', () => {
    stubPlatform(undefined)
    expect(joinTreePath('/ws', 'x.txt')).toBe('/ws/x.txt')
    expect(joinTreePath('/ws', 'sub')).toBe('/ws/sub')
  })

  it('joins with "\\" on a Windows platform signal', () => {
    stubPlatform('Win32')
    expect(joinTreePath('\\ws', 'x.txt')).toBe('\\ws\\x.txt')
    expect(joinTreePath('\\ws', 'sub')).toBe('\\ws\\sub')
    expect(joinTreePath('C:\\ws', 'x.txt')).toBe('C:\\ws\\x.txt')
  })

  it('returns the parent directory of a POSIX tree path', () => {
    stubPlatform('MacIntel')
    expect(parentTreeDir('/ws/a.txt')).toBe('/ws')
    expect(parentTreeDir('/ws/sub/inner.txt')).toBe('/ws/sub')
    // A separator at index 0 carries no parent slice: the path passes through.
    expect(parentTreeDir('/a.txt')).toBe('/a.txt')
  })

  it('returns the parent directory of a Windows tree path', () => {
    stubPlatform('Win32')
    expect(parentTreeDir('\\ws\\a.txt')).toBe('\\ws')
    expect(parentTreeDir('\\ws\\sub\\inner.txt')).toBe('\\ws\\sub')
    expect(parentTreeDir('\\a.txt')).toBe('\\a.txt')
  })
})
