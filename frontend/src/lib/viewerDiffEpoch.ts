/**
 * Monotonic epoch of git-state mutations visible to the file viewer's diff
 * pipeline.
 *
 * The backend emits `git:status_changed` after EVERY mutating git RPC (the
 * emitGitStatusChanged funnel: stage/unstage/stage-all/commit/checkout/
 * stash/merge/rebase/reset/discard/…). useFileViewerData subscribes once and
 * bumps this epoch on each such event, recording the epoch at which each
 * open file's diff was last fetched.
 *
 * The epoch is what makes the viewer's content short-circuit safe: a staged
 * or committed change moves the index/HEAD without touching the worktree
 * bytes, so byte-identical content alone cannot prove the cached hunks are
 * current — but a moved epoch can prove they are not.
 *
 * Module-scope singleton on purpose: the counter is process-global state,
 * shared by every viewer instance and test. Tests bump it directly via
 * markViewerDiffsStale.
 */
let epoch = 0

/** Invalidate every cached viewer diff (git state may have moved under them). */
export function markViewerDiffsStale(): void {
  epoch++
}

/** Current epoch, to compare against the epoch a diff was fetched at. */
export function viewerDiffEpoch(): number {
  return epoch
}
