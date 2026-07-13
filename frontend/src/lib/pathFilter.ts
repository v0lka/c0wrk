// Shared glob/regex path-matching logic for file filters.
//
// Extracted from useFileSearch so the file-tree panel and the git history
// panel share one matching implementation. Glob uses picomatch
// (case-insensitive, substring match); regex uses a case-insensitive
// RegExp. Both modes test the full path AND its basename so patterns like
// `*.ts` (basename) and `src/.*\.ts` (full path) work regardless of depth.
//
// Returns null for an empty or invalid filter so callers can treat
// "no filter" and "broken regex" uniformly (empty → show everything,
// invalid regex → show nothing).

import picomatch from 'picomatch'

/** Filter matching mode, toggled by the FilterBar button. */
export type FilterMode = 'glob' | 'regex'

/**
 * Reports whether the filter text is a non-empty invalid regex. Returns
 * false for empty text (no filter) and for glob mode (picomatch handles
 * its own errors internally). Callers use this to show an "Invalid regex"
 * hint instead of a generic "no matches" message.
 */
export function isInvalidRegex(filterText: string, mode: FilterMode): boolean {
  const trimmed = filterText.trim()
  if (!trimmed || mode !== 'regex') return false
  try {
    new RegExp(trimmed, 'i')
    return false
  } catch {
    return true
  }
}

/**
 * Build a predicate that tests whether a file path matches the given
 * glob/regex filter text. Returns null when the filter text is empty or
 * the regex is invalid.
 */
export function createPathMatcher(
  filterText: string,
  mode: FilterMode,
): ((path: string) => boolean) | null {
  const trimmed = filterText.trim()
  if (!trimmed) return null

  if (mode === 'regex') {
    let re: RegExp
    try {
      re = new RegExp(trimmed, 'i')
    } catch {
      return null
    }
    return (path: string) => re.test(path) || re.test(basename(path))
  }

  const isMatch = picomatch(trimmed, { nocase: true, contains: true })
  return (path: string) => isMatch(path) || isMatch(basename(path))
}

/** Last path segment (after the final `/`); the full string when none. */
function basename(path: string): string {
  const idx = path.lastIndexOf('/')
  return idx === -1 ? path : path.slice(idx + 1)
}
