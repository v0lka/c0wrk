// Pure helpers for the research-log rendering.
//
// No React/DOM dependencies — fully unit-testable in isolation. Kept separate
// from ResearchLog.tsx so the component file exports components only
// (fast-refresh safe), mirroring researchWorkspaceUtils.ts.

import type { ResearchLogEntry } from '@/types/models'

/** Return all log entries, newest first. Pure and unit-tested.
 *  The log is append-only (entries carry a 1-based file-order `id`), so "latest"
 *  is simply the tail of the array. The dashboard shows every entry — the list
 *  scrolls inside its own block — so no cap is applied here. */
export function latestLogEntries(log: ResearchLogEntry[]): ResearchLogEntry[] {
  return [...log].reverse()
}

/** Deterministic ISO→`YYYY-MM-DD HH:MM:SS` trim (no locale dependency). */
export function formatLogTime(createdAt: string): string {
  const iso = createdAt.slice(0, 19)
  return iso.includes('T') ? iso.replace('T', ' ') : iso
}
