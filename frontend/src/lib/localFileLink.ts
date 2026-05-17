// Pure utility functions for detecting, parsing, and validating local file link hrefs
// in markdown output. No React or store dependencies.

const PROTOCOL_RE = /^[a-z][a-z0-9+.-]*:/i

/**
 * Returns true if the href should be treated as a local file link
 * (i.e., not an external URL, anchor, or known protocol).
 */
export function isLocalFileHref(href: string | undefined): boolean {
  if (!href) return false
  if (href.startsWith('#')) return false
  if (PROTOCOL_RE.test(href)) return false
  return true
}

const LINE_SUFFIX_RE = /#L(\d+)(?:-\d+)?$/

/**
 * Extracts the file path and optional line number from a local file href.
 * Supports formats: `path/file.ts#L42` and `path/file.ts#L5-10` (uses start line).
 * Decodes URI-encoded characters in the path.
 */
export function parseLocalFileHref(href: string): { path: string; line?: number } {
  const match = LINE_SUFFIX_RE.exec(href)
  let path: string
  let line: number | undefined

  if (match) {
    path = href.slice(0, href.lastIndexOf('#'))
    line = parseInt(match[1]!, 10)
  } else {
    path = href
  }

  try {
    path = decodeURIComponent(path)
  } catch {
    // If decoding fails (malformed URI), use the raw path
  }

  return line !== undefined ? { path, line } : { path }
}

/**
 * Resolves a file path against the workspace root, normalizes `.` and `..` segments,
 * and validates that the result stays within the workspace boundary.
 * Returns the normalized absolute path, or null if validation fails.
 */
export function normalizePath(rootPath: string, filePath: string): string | null {
  const abs = filePath.startsWith('/') ? filePath : rootPath + '/' + filePath

  const parts = abs.split('/')
  const segments: string[] = []

  for (const part of parts) {
    if (part === '' || part === '.') continue
    if (part === '..') {
      segments.pop()
    } else {
      segments.push(part)
    }
  }

  const normalized = '/' + segments.join('/')

  if (normalized === rootPath || normalized.startsWith(rootPath + '/')) {
    return normalized
  }

  return null
}
