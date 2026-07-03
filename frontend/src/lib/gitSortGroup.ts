import type { GitPanelEntry, SortBy, GroupBy } from '@/stores/gitPanelStore'

// ──────────────────────────────── Helpers ────────────────────────────────────

/**
 * Extract the file extension (without the dot, lowercase) from a path.
 * Files with no extension or a leading-dot basename (e.g. `.gitignore`)
 * return an empty string so they sort together at the top of an extension sort.
 */
export function getExtension(path: string): string {
  const base = path.slice(path.lastIndexOf('/') + 1)
  const dot = base.lastIndexOf('.')
  if (dot <= 0) return ''
  return base.slice(dot + 1).toLowerCase()
}

/**
 * Parent directory of a path (without trailing slash). Root-level files
 * (no separator) are grouped under the sentinel label `(root)`.
 */
export function parentDir(path: string): string {
  const lastSep = path.lastIndexOf('/')
  if (lastSep === -1) return '(root)'
  return path.slice(0, lastSep)
}

/** Human-readable label for a git status code. */
export function statusLabel(status: string): string {
  switch (status) {
    case 'M':
      return 'Modified'
    case 'A':
      return 'Added'
    case 'D':
      return 'Deleted'
    case 'R':
      return 'Renamed'
    case 'C':
      return 'Copied'
    case 'U':
      return 'Unmerged'
    case '?':
      return 'Untracked'
    default:
      return status || 'Unknown'
  }
}

// ───────────────────────────────── Sorting ───────────────────────────────────

/**
 * Comparator for two entries by the given sort criterion, with the file path
 * as a stable tiebreak so ordering is always deterministic.
 *
 * - `path`: alphabetical by full path.
 * - `status`: by git status letter, then path.
 * - `extension`: by file extension (case-insensitive), then path.
 */
export function compareEntries(
  a: GitPanelEntry,
  b: GitPanelEntry,
  sortBy: SortBy,
): number {
  let cmp = 0
  if (sortBy === 'status') {
    cmp = a.status.localeCompare(b.status)
  } else if (sortBy === 'extension') {
    cmp = getExtension(a.path).localeCompare(getExtension(b.path))
  }
  if (cmp === 0) cmp = a.path.localeCompare(b.path)
  return cmp
}

/**
 * Return a **new** array of entries sorted by the given criterion.
 *
 * Pure: does not mutate the input array or any entry object.
 */
export function sortEntries(
  entries: GitPanelEntry[],
  sortBy: SortBy,
): GitPanelEntry[] {
  return [...entries].sort((a, b) => compareEntries(a, b, sortBy))
}

// ──────────────────────────────── Grouping ───────────────────────────────────

/**
 * Group entries by the given criterion into a `Map` of group-label → entries.
 *
 * - `none`: a single group keyed `''` containing every entry (order preserved).
 * - `status`: grouped by human-readable status label.
 * - `directory`: grouped by parent directory (`(root)` for root-level files).
 *
 * Group labels are sorted alphabetically for deterministic iteration. The
 * order of entries **within** each group follows the input order — apply
 * `sortEntries` beforehand to control intra-group ordering.
 *
 * Pure: does not mutate the input array or any entry object.
 */
export function groupEntries(
  entries: GitPanelEntry[],
  groupBy: GroupBy,
): Map<string, GitPanelEntry[]> {
  if (groupBy === 'none') {
    return new Map<string, GitPanelEntry[]>([['', [...entries]]])
  }

  const groups = new Map<string, GitPanelEntry[]>()
  for (const entry of entries) {
    const key =
      groupBy === 'status' ? statusLabel(entry.status) : parentDir(entry.path)
    const list = groups.get(key)
    if (list) {
      list.push(entry)
    } else {
      groups.set(key, [entry])
    }
  }

  // Sort group labels for a stable, predictable render order.
  return new Map(
    [...groups.entries()].sort(([a], [b]) => a.localeCompare(b)),
  )
}
