import { describe, it, expect } from 'vitest'
import { isLocalFileHref, isExternalUrl, parseLocalFileHref, normalizePath, relativePath } from './localFileLink'

describe('isLocalFileHref', () => {
  it('returns false for http URLs', () => {
    expect(isLocalFileHref('http://example.com')).toBe(false)
    expect(isLocalFileHref('https://example.com/path')).toBe(false)
  })

  it('returns false for mailto links', () => {
    expect(isLocalFileHref('mailto:user@example.com')).toBe(false)
  })

  it('returns false for tel links', () => {
    expect(isLocalFileHref('tel:+1234567890')).toBe(false)
  })

  it('returns false for anchor links', () => {
    expect(isLocalFileHref('#section-id')).toBe(false)
    expect(isLocalFileHref('#')).toBe(false)
  })

  it('returns false for javascript protocol', () => {
    expect(isLocalFileHref('javascript:void(0)')).toBe(false)
  })

  it('returns false for data URIs', () => {
    expect(isLocalFileHref('data:text/plain;base64,abc')).toBe(false)
  })

  it('returns false for ftp', () => {
    expect(isLocalFileHref('ftp://server.com/file')).toBe(false)
  })

  it('returns false for undefined or empty href', () => {
    expect(isLocalFileHref(undefined)).toBe(false)
    expect(isLocalFileHref('')).toBe(false)
  })

  it('returns true for relative paths', () => {
    expect(isLocalFileHref('src/main.ts')).toBe(true)
    expect(isLocalFileHref('./relative.ts')).toBe(true)
    expect(isLocalFileHref('../parent/file.ts')).toBe(true)
  })

  it('returns true for absolute paths', () => {
    expect(isLocalFileHref('/absolute/path.ts')).toBe(true)
  })

  it('returns true for paths with line number suffix', () => {
    expect(isLocalFileHref('src/main.ts#L42')).toBe(true)
    expect(isLocalFileHref('src/main.ts#L5-10')).toBe(true)
  })
})

describe('isExternalUrl', () => {
  it('returns true for http/https URLs', () => {
    expect(isExternalUrl('http://example.com')).toBe(true)
    expect(isExternalUrl('https://example.com/path?q=1')).toBe(true)
  })

  it('returns true for mailto/tel/ftp', () => {
    expect(isExternalUrl('mailto:user@example.com')).toBe(true)
    expect(isExternalUrl('tel:+1234567890')).toBe(true)
    expect(isExternalUrl('ftp://server.com/file')).toBe(true)
  })

  it('returns true for data URIs', () => {
    expect(isExternalUrl('data:text/plain;base64,abc')).toBe(true)
  })

  it('returns false for anchor links', () => {
    expect(isExternalUrl('#section-id')).toBe(false)
    expect(isExternalUrl('#')).toBe(false)
  })

  it('returns false for relative and absolute file paths', () => {
    expect(isExternalUrl('src/main.ts')).toBe(false)
    expect(isExternalUrl('./relative.ts')).toBe(false)
    expect(isExternalUrl('../parent/file.ts')).toBe(false)
    expect(isExternalUrl('/absolute/path.ts')).toBe(false)
    expect(isExternalUrl('src/main.ts#L42')).toBe(false)
  })

  it('returns false for undefined or empty href', () => {
    expect(isExternalUrl(undefined)).toBe(false)
    expect(isExternalUrl('')).toBe(false)
  })
})

describe('parseLocalFileHref', () => {
  it('parses a plain path without line number', () => {
    expect(parseLocalFileHref('src/main.ts')).toEqual({ path: 'src/main.ts' })
  })

  it('parses path with single line number', () => {
    expect(parseLocalFileHref('src/main.ts#L42')).toEqual({ path: 'src/main.ts', line: 42 })
  })

  it('parses path with line range, uses start line', () => {
    expect(parseLocalFileHref('src/main.ts#L5-10')).toEqual({ path: 'src/main.ts', line: 5 })
  })

  it('parses GitHub canonical range form #L5-L10, uses start line', () => {
    expect(parseLocalFileHref('src/main.ts#L5-L10')).toEqual({ path: 'src/main.ts', line: 5 })
  })

  it('parses GitHub canonical range form #L20-L36, uses start line', () => {
    expect(parseLocalFileHref('desktop/x.go#L20-L36')).toEqual({ path: 'desktop/x.go', line: 20 })
  })

  it('handles absolute paths with line numbers', () => {
    expect(parseLocalFileHref('/abs/path.go#L100')).toEqual({ path: '/abs/path.go', line: 100 })
  })

  it('treats non-matching hash as part of path', () => {
    expect(parseLocalFileHref('src/file#name.ts')).toEqual({ path: 'src/file#name.ts' })
    expect(parseLocalFileHref('path/to/file.ts#Lnotanumber')).toEqual({
      path: 'path/to/file.ts#Lnotanumber',
    })
  })

  it('decodes URI-encoded characters', () => {
    expect(parseLocalFileHref('path%20with%20spaces/file.ts')).toEqual({
      path: 'path with spaces/file.ts',
    })
    expect(parseLocalFileHref('path%20name.ts#L10')).toEqual({
      path: 'path name.ts',
      line: 10,
    })
  })

  it('handles malformed URI encoding gracefully', () => {
    // %ZZ is not valid percent encoding — should fall through without throwing
    expect(parseLocalFileHref('%ZZinvalid')).toEqual({ path: '%ZZinvalid' })
  })
})

describe('normalizePath', () => {
  const root = '/workspace/project'

  it('resolves relative paths', () => {
    expect(normalizePath(root, 'src/main.ts')).toBe('/workspace/project/src/main.ts')
  })

  it('returns normalized absolute path for path traversal above workspace root', () => {
    expect(normalizePath(root, '../../etc/passwd')).toBe('/etc/passwd')
    expect(normalizePath(root, '../../../root/.ssh/id_rsa')).toBe('/root/.ssh/id_rsa')
  })

  it('resolves paths with dot segments', () => {
    expect(normalizePath(root, './src/./main.ts')).toBe('/workspace/project/src/main.ts')
  })

  it('collapses parent references within workspace', () => {
    expect(normalizePath(root, 'src/../lib/utils.ts')).toBe('/workspace/project/lib/utils.ts')
  })

  it('accepts absolute paths within workspace', () => {
    expect(normalizePath(root, '/workspace/project/src/a.ts')).toBe(
      '/workspace/project/src/a.ts',
    )
  })

  it('returns normalized absolute path for out-of-workspace paths', () => {
    expect(normalizePath(root, '/other/project/file.ts')).toBe('/other/project/file.ts')
    expect(normalizePath(root, '/workspace/project-other/file.ts')).toBe(
      '/workspace/project-other/file.ts',
    )
  })

  it('handles path equal to root', () => {
    expect(normalizePath(root, '/workspace/project')).toBe('/workspace/project')
  })

  it('normalizes multiple slashes', () => {
    expect(normalizePath(root, 'src///main.ts')).toBe('/workspace/project/src/main.ts')
  })

  it('handles empty file path as root', () => {
    expect(normalizePath(root, '')).toBe('/workspace/project')
  })
})

describe('relativePath', () => {
  const root = '/workspace/project'

  it('computes relative path from workspace root', () => {
    expect(relativePath(root, '/workspace/project/src/main.ts')).toBe('src/main.ts')
    expect(relativePath(root, '/workspace/project/lib/utils/helper.go')).toBe('lib/utils/helper.go')
  })

  it('returns "." when path equals workspace root', () => {
    expect(relativePath(root, '/workspace/project')).toBe('.')
  })

  it('handles root path with trailing slash', () => {
    expect(relativePath('/workspace/project/', '/workspace/project/src/a.ts')).toBe('src/a.ts')
  })

  it('handles nested deep paths', () => {
    expect(relativePath(root, '/workspace/project/a/b/c/d/e/file.txt')).toBe('a/b/c/d/e/file.txt')
  })

  it('converts backslashes to forward slashes', () => {
    expect(relativePath(root, '\\workspace\\project\\src\\main.ts')).toBe('src/main.ts')
    expect(relativePath('C:\\Users\\dev\\repo', 'C:\\Users\\dev\\repo\\src\\lib.ts')).toBe('src/lib.ts')
  })

  it('handles root with backslashes', () => {
    expect(relativePath('C:\\Users\\dev\\repo', 'C:\\Users\\dev\\repo')).toBe('.')
    expect(relativePath('C:\\Users\\dev\\repo\\', 'C:\\Users\\dev\\repo\\src\\a.ts')).toBe('src/a.ts')
  })

  it('returns path without leading slash when not under workspace root', () => {
    expect(relativePath(root, '/other/project/file.ts')).toBe('other/project/file.ts')
    expect(relativePath(root, '/workspace/project-other/file.ts')).toBe('workspace/project-other/file.ts')
  })

  it('handles empty workspace root gracefully', () => {
    expect(relativePath('', '/absolute/path.ts')).toBe('absolute/path.ts')
    expect(relativePath('', '')).toBe('.')
  })

  it('handles relative paths as input (not under root)', () => {
    expect(relativePath(root, 'src/main.ts')).toBe('src/main.ts')
  })

  it('handles mixed slashes in input', () => {
    expect(relativePath(root, '/workspace\\project/src\\lib.ts')).toBe('src/lib.ts')
  })
})
