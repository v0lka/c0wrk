// Pure commit-stats helpers — no React/DOM dependencies, fully unit-testable.
//
// The backend buckets each commit's changed files into a CommitStats triple
// (GetGitHistory's per-commit `stats`). These helpers mirror that bucketing
// for CLIENT-side aggregation over a commit's fetched changed-file list —
// e.g. counting a commit's own stats from its expanded file list, or summing
// a filtered selection. Status letters come from CommitFile.status (the
// `--name-status` letters: A/M/D plus scored R/C/T forms).
//
// The color mapping uses the semantic Tailwind tokens already used across
// the Git panel (GitFileEntry status letters / ChangesSummaryBar segments):
// added=success, modified=warning, deleted=destructive.

import type { CommitFile, CommitStats } from '@/types/models'

/** The three CommitStats counters. */
export type StatBucket = keyof CommitStats

/**
 * Bucket one changed file's status letter — mirrors the backend's
 * countCommitStats: A→added, D→deleted, everything else (M/R/C/T/unknown)
 * → modified. Scored forms (`R100`) bucket by their leading letter, and an
 * empty/broken status falls into the modified bucket rather than being
 * dropped (the backend ignores only tab-less broken log lines, which never
 * produce a CommitFile entry here).
 */
export function statBucket(status: string): StatBucket {
  const letter = status.charAt(0).toUpperCase()
  if (letter === 'A') return 'added'
  if (letter === 'D') return 'deleted'
  return 'modified'
}

/** Aggregate a commit's changed files into CommitStats (same bucketing as the backend's per-commit `stats`). */
export function countFileStats(files: CommitFile[]): CommitStats {
  const stats: CommitStats = { added: 0, modified: 0, deleted: 0 }
  for (const file of files) {
    stats[statBucket(file.status)]++
  }
  return stats
}

/** Semantic text-color class per bucket — mirrors the Git panel's status coloring (A=success, M=warning, D=destructive). */
export function statColorClass(bucket: StatBucket): string {
  switch (bucket) {
    case 'added':
      return 'text-success'
    case 'modified':
      return 'text-warning'
    case 'deleted':
      return 'text-destructive'
  }
}

/** Total number of changed files represented by a stats triple. */
export function totalChanges(stats: CommitStats): number {
  return stats.added + stats.modified + stats.deleted
}
