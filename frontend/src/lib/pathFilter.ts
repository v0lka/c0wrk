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
 * Every character picomatch treats as pattern syntax (extglob operators,
 * classes, quantifiers, alternation, the escape character itself). `.` is
 * deliberately NOT in the set: picomatch already reads it literally, it
 * cannot join a catastrophic-backtracking construct on its own, and leaving
 * it unescaped keeps the auto-filled filter text readable.
 */
const GLOB_METACHARS = /[*?[\](){}|!@+\\]/g

/**
 * Escape a string so picomatch matches it LITERALLY, as a sequence of
 * characters rather than as glob syntax.
 *
 * This is the gate for every repo-derived path that flows into a filter.
 * "View History" feeds a workspace file path into the picomatch matcher as
 * a pattern — and a workspace path is attacker-influenceable data (a cloned
 * repository, an agent-written file, a dropped file): its bytes are chosen
 * off-machine. Interpreted raw, glob metachars both break the literal
 * intent (a file named `report[2026].md` compiles to a character class and
 * never matches itself) and expose picomatch's parse-time ReDoS
 * (GHSA-fc5q-39xc-vjpf, advisory withdrawn 2026-11-18 but the behavior is
 * real: a single 100-byte `+(*)`-repeated filename measured 63s+ of
 * blocked main thread against a 30-char subject on 4.0.4). Escaped, the
 * parser never enters the extglob machinery at all — every byte matches
 * itself. The inserted backslashes also literalize the text if the
 * operator later flips the FilterBar to regex mode (`\*` is a literal `*`
 * in a JS RegExp), so the neutralization holds across both modes.
 */
export function escapeGlobLiteral(value: string): string {
  return value.replace(GLOB_METACHARS, '\\$&')
}

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
