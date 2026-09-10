// Pure helpers for the research-log rendering.
//
// No React/DOM dependencies — fully unit-testable in isolation. Kept separate
// from ResearchLog.tsx so the component file exports components only
// (fast-refresh safe), mirroring researchWorkspaceUtils.ts.

import type { ResearchLogEntry } from '@/types/models'

/** Default render cap for the research log ([20]b). Logs are append-only and
 *  grow for the project's lifetime; rendering every entry puts one DOM node
 *  per entry into the panel. The log renders the newest entries up to this
 *  cap with a "show all" expansion for the rest. */
export const RESEARCH_LOG_RENDER_CAP = 100

/** Return log entries, newest first. Pure and unit-tested.
 *  The log is append-only (entries carry a 1-based file-order `id`), so
 *  "latest" is simply the tail of the array. `cap` (optional) limits the
 *  result to the newest `cap` entries — the dashboard renders capped by
 *  default and expands on demand. */
export function latestLogEntries(
  log: ResearchLogEntry[],
  cap?: number,
): ResearchLogEntry[] {
  const reversed = [...log].reverse()
  return cap === undefined ? reversed : reversed.slice(0, cap)
}

/** Deterministic ISO→`YYYY-MM-DD HH:MM:SS` trim (no locale dependency). */
export function formatLogTime(createdAt: string): string {
  const iso = createdAt.slice(0, 19)
  return iso.includes('T') ? iso.replace('T', ' ') : iso
}
