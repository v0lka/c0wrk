// Pure helpers for the research-log rendering.
//
// No React/DOM dependencies — fully unit-testable in isolation. Kept separate
// from ResearchLog.tsx so the component file exports components only
// (fast-refresh safe), mirroring researchWorkspaceUtils.ts.

import type { ResearchLogEntry } from '@/types/models'

/** How many of the most recent log entries to surface in the dashboard. */
export const DEFAULT_LOG_LIMIT = 10

/** Return the most recent `limit` entries, newest first. Pure and unit-tested.
 *  The log is append-only (entries carry a 1-based file-order `id`), so "latest"
 *  is simply the tail of the array. */
export function latestLogEntries(
  log: ResearchLogEntry[],
  limit = DEFAULT_LOG_LIMIT,
): ResearchLogEntry[] {
  return log.slice(-limit).reverse()
}

/** Deterministic ISO→`YYYY-MM-DD HH:MM:SS` trim (no locale dependency). */
export function formatLogTime(createdAt: string): string {
  const iso = createdAt.slice(0, 19)
  return iso.includes('T') ? iso.replace('T', ' ') : iso
}
