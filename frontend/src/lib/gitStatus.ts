// Shared helpers for converting backend git status into store entries.
//
// Extracted from GitPanel/index.tsx and useGitStatusEvents.ts so the
// GitStatusEntry → GitPanelEntry mapping lives in exactly one place.

import type { GitPanelEntry } from '@/stores/gitPanelStore'
import type { GitStatusEntry } from '@/types/models'

/**
 * Convert a backend `GitStatusEntry` map (keyed by path) into the
 * `GitPanelEntry[]` shape consumed by the git panel store.
 *
 * The mapping is intentionally a straight passthrough of the porcelain
 * status codes (`status`, `indexStatus`, `worktreeStatus`) so that
 * downstream components can classify entries precisely (e.g. untracked
 * files carry `worktreeStatus === '?'`).
 */
export function toEntries(
  statusMap: Record<string, GitStatusEntry>,
): GitPanelEntry[] {
  return Object.entries(statusMap).map(([path, entry]) => ({
    path,
    status: entry.status,
    staged: entry.staged,
    diffStat: null,
    indexStatus: entry.index_status,
    worktreeStatus: entry.worktree_status,
  }))
}
