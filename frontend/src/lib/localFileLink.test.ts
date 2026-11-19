import { describe, it, expect } from 'vitest'
import {
  isLocalFileHref,
  isExternalUrl,
  parseLocalFileHref,
  isAbsolutePath,
  normalizePath,
  relativePath,
  resolveFileRefPath,
  candidateLinkPaths,
} from './localFileLink'

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

describe('windows drive prefixes are never external URLs', () => {
  // A one-letter + colon prefix (`C:`) is a Windows drive, not an RFC 3986
  // scheme. Both dispatch sites hand a link to the system browser exactly
  // when isExternalUrl(href) is true (the markdownConfig anchor handler and
  // the PaperHtmlView click handler → openExternalURL → BrowserOpenURL), so
  // isExternalUrl being false for every ^[A-Za-z]: href guarantees that no
  // drive path can ever reach openExternalURL.
  const driveHrefs = [
    'C:\\Documents',
    'c:\\c:\\Documents',
    'C:/Documents/file.md',
    'c:\\ws\\src\\main.ts#L42',
    'Z:\\deep\\nested\\path.md',
    'C:', // bare drive — still letter + colon, still not a scheme
    'C:foo', // drive-relative — same guard
  ]

  it('never classifies a drive href as external (no openExternalURL dispatch)', () => {
    for (const href of driveHrefs) {
      expect(isExternalUrl(href), href).toBe(false)
    }
  })

  it('classifies every drive href as a local file link', () => {
    for (const href of driveHrefs) {
      expect(isLocalFileHref(href), href).toBe(true)
    }
  })

  it('guard is exactly one letter + colon: multi-letter schemes stay external', () => {
    expect(isExternalUrl('ab:rest-of-scheme')).toBe(true)
    expect(isLocalFileHref('ab:rest-of-scheme')).toBe(false)
  })
})

describe('isAbsolutePath', () => {
  it('returns true for POSIX absolute paths', () => {
    expect(isAbsolutePath('/workspace/project')).toBe(true)
    expect(isAbsolutePath('/a.ts')).toBe(true)
  })

  it('returns true for Windows drive-rooted paths, both separators', () => {
    expect(isAbsolutePath('C:\\ws\\project')).toBe(true)
    expect(isAbsolutePath('C:/ws/project')).toBe(true)
    expect(isAbsolutePath('c:\\ws')).toBe(true)
    expect(isAbsolutePath('Z:/file.md')).toBe(true)
  })

  it('returns true for UNC paths, both separators', () => {
    expect(isAbsolutePath('\\\\srv\\share\\doc.md')).toBe(true)
    expect(isAbsolutePath('//srv/share/doc.md')).toBe(true)
  })

  it('returns false for relative paths', () => {
    expect(isAbsolutePath('src/main.ts')).toBe(false)
    expect(isAbsolutePath('./a.ts')).toBe(false)
    expect(isAbsolutePath('../a.ts')).toBe(false)
    expect(isAbsolutePath('')).toBe(false)
  })

  it('returns false for drive-relative paths (colon without separator)', () => {
    // `C:foo` is drive-relative on Windows (Go filepath.IsAbs semantics) — not absolute
    expect(isAbsolutePath('C:foo')).toBe(false)
    expect(isAbsolutePath('C:')).toBe(false)
  })

  it('returns false for single-backslash rooted Windows paths (ADR-095 decision 7)', () => {
    // `\x` is rooted against the current drive's root on Windows; one leading
    // separator is below the UNC threshold, so it stays "relative" and joins
    // the workspace root — the accepted, documented limitation (the join
    // canonicalizes the backslash away, so the rooted meaning is silently
    // lost). Mirrors the "single leading backslash" row of TestIsAbsolutePath
    // (core/pathabs_test.go).
    expect(isAbsolutePath('\\x')).toBe(false)
    expect(isAbsolutePath('\\ws\\f.md')).toBe(false)
    expect(normalizePath('/repo', '\\x\\f.md')).toBe('/repo/x/f.md')
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

  describe('windows paths', () => {
    it('normalizes drive-absolute input without joining the workspace root', () => {
      expect(normalizePath('C:\\ws', 'C:\\Documents\\f.md')).toBe('C:/Documents/f.md')
      expect(normalizePath('C:\\ws', 'C:/Documents/f.md')).toBe('C:/Documents/f.md')
      expect(normalizePath('C:\\ws', 'D:\\other\\file.ts')).toBe('D:/other/file.ts')
    })

    it('joins relative windows paths with the workspace root', () => {
      expect(normalizePath('C:\\ws', 'src\\a.ts')).toBe('C:/ws/src/a.ts')
      expect(normalizePath('C:/ws', './src\\.\\b.md')).toBe('C:/ws/src/b.md')
    })

    it('collapses parent references within a drive path', () => {
      expect(normalizePath('C:\\ws', 'C:\\ws\\sub\\..\\lib\\u.ts')).toBe('C:/ws/lib/u.ts')
    })

    it('clamps ".." at the drive root', () => {
      expect(normalizePath('C:\\ws', 'C:\\..\\..\\etc\\f.md')).toBe('C:/etc/f.md')
      expect(normalizePath('C:\\ws', '..\\..\\..\\outside.txt')).toBe('C:/outside.txt')
    })

    it('normalizes UNC absolute input without joining the workspace root', () => {
      expect(normalizePath('C:\\ws', '\\\\srv\\share\\docs\\f.md')).toBe(
        '//srv/share/docs/f.md',
      )
      expect(normalizePath('\\\\srv\\share', '//srv/share/docs/f.md')).toBe(
        '//srv/share/docs/f.md',
      )
    })

    it('clamps ".." at the UNC share root', () => {
      expect(normalizePath('//srv/share', '//srv/share/docs/../../x.md')).toBe(
        '//srv/share/x.md',
      )
    })

    it('preserves POSIX absolute input without joining the windows root', () => {
      expect(normalizePath('C:\\ws', '/etc/passwd')).toBe('/etc/passwd')
    })
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

  it('returns drive-form path without leading slash when not under the windows root (regression)', () => {
    expect(relativePath('C:\\ws', 'C:\\other\\f.md')).toBe('C:/other/f.md')
    expect(relativePath('/posix/root', 'D:\\data\\f.md')).toBe('D:/data/f.md')
  })
})

describe('resolveFileRefPath', () => {
  it('normalizes a drive-absolute @file path without joining the workspace root', () => {
    expect(resolveFileRefPath('C:\\ws', 'C:\\Doc\\f.md')).toBe('C:/Doc/f.md')
    // The root dialect is irrelevant for absolute inputs — never joined.
    expect(resolveFileRefPath('/home/u/proj', 'C:\\Doc\\f.md')).toBe('C:/Doc/f.md')
  })

  it('normalizes UNC and POSIX absolute paths in place', () => {
    expect(resolveFileRefPath('C:\\ws', '\\\\srv\\share\\docs\\f.md')).toBe('//srv/share/docs/f.md')
    expect(resolveFileRefPath('/repo', '/repo/src/a/../b.ts')).toBe('/repo/src/b.ts')
  })

  it('joins relative paths with the workspace root (both dialects)', () => {
    expect(resolveFileRefPath('C:\\ws', 'src\\main.ts')).toBe('C:/ws/src/main.ts')
    expect(resolveFileRefPath('/repo', 'src/main.ts')).toBe('/repo/src/main.ts')
  })

  it('returns relative paths unchanged when no root is available', () => {
    expect(resolveFileRefPath(null, 'src/main.ts')).toBe('src/main.ts')
    expect(resolveFileRefPath(undefined, 'src\\main.ts')).toBe('src\\main.ts')
    expect(resolveFileRefPath(null, '')).toBe('')
  })

  it('normalizes absolute paths without any root (No-Project markdown links)', () => {
    // The MarkdownLink no-root path (ADR-095 decision 6): absolute inputs —
    // POSIX, drive, UNC — resolve without a workspace root; only the drive
    // href never arrives (cleared upstream by react-markdown's URL transform),
    // so the pin guards the resolver the component delegates to.
    expect(resolveFileRefPath(null, '/abs/dir/a.ts')).toBe('/abs/dir/a.ts')
    expect(resolveFileRefPath(null, 'C:\\Doc\\f.md')).toBe('C:/Doc/f.md')
    expect(resolveFileRefPath(undefined, '\\\\srv\\share\\f.md')).toBe('//srv/share/f.md')
  })
})

describe('candidateLinkPaths', () => {
  it('yields the single normalized candidate for absolute inputs (no joining)', () => {
    expect(candidateLinkPaths('/repo/src/a.ts', '/repo/docs/readme.md', '/repo')).toEqual(['/repo/src/a.ts'])
    expect(candidateLinkPaths('C:\\Doc\\f.md', '/repo/docs/r.md', 'C:\\ws')).toEqual(['C:/Doc/f.md'])
    expect(candidateLinkPaths('\\\\srv\\share\\f.md', null, '/repo')).toEqual(['//srv/share/f.md'])
  })

  it('tries the document directory first, then the workspace root', () => {
    expect(candidateLinkPaths('b.md', '/repo/docs/a.md', '/repo')).toEqual([
      '/repo/docs/b.md',
      '/repo/b.md',
    ])
  })

  it('resolves a ../ href against the document directory, clamped at the anchor root', () => {
    expect(candidateLinkPaths('../shared/x.ts', '/repo/src/feat/a.ts', '/repo')).toEqual([
      '/repo/src/shared/x.ts',
      // The root-join arm resolves .. above the workspace (normalizePath
      // clamps at the filesystem anchor, never above it) — the viewer's own
      // uncontained read path opens it.
      '/shared/x.ts',
    ])
    // Both arms clamp .. at the filesystem anchor; equal candidates dedupe.
    expect(candidateLinkPaths('../../outside.md', '/repo/docs/a.md', '/repo')).toEqual([
      '/outside.md',
    ])
  })

  it('dedupes when the document directory IS the workspace root', () => {
    expect(candidateLinkPaths('b.md', '/repo/a.md', '/repo')).toEqual(['/repo/b.md'])
  })

  it('yields only the document candidate without a workspace root (papers)', () => {
    expect(candidateLinkPaths('b.md', '/repo/docs/a.md', null)).toEqual(['/repo/docs/b.md'])
    expect(candidateLinkPaths('b.md', '/repo/docs/a.md', undefined)).toEqual(['/repo/docs/b.md'])
  })

  it('yields only the workspace candidate without a document base (chat)', () => {
    expect(candidateLinkPaths('src/a.ts', null, '/repo')).toEqual(['/repo/src/a.ts'])
    expect(candidateLinkPaths('src/a.ts', undefined, '/repo')).toEqual(['/repo/src/a.ts'])
  })

  it('yields nothing when neither base is available (root-less chat stays inert)', () => {
    expect(candidateLinkPaths('src/a.ts', null, null)).toEqual([])
    expect(candidateLinkPaths('src/a.ts', null, undefined)).toEqual([])
  })

  it('yields nothing for an empty href', () => {
    expect(candidateLinkPaths('', '/repo/docs/a.md', '/repo')).toEqual([])
  })

  it('normalizes backslash document paths and backslash hrefs (Windows)', () => {
    expect(candidateLinkPaths('sub\\b.md', 'C:\\repo\\docs\\a.md', 'C:\\repo')).toEqual([
      'C:/repo/docs/sub/b.md',
      'C:/repo/sub/b.md',
    ])
  })
})
