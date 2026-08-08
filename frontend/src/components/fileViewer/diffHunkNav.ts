// Pure helpers for the file-viewer hunk navigation bar.
//
// Extracted from DiffHunkNavBar so the component file exports only a component
// (react-refresh/only-export-components) and the index/entry logic is
// independently unit-testable — mirroring the diffHunkRange / diffParsing split.
//
// Reuses the review module's diff-parsing primitives (summarizeHunk,
// hunkLineRangeLabel) since they are framework-free and operate on the same
// unified-diff block format the backend emits for both features.

import type { HunkDiffInfo } from '@/types/models'
import {
  summarizeHunk,
  hunkLineRangeLabel,
  type HunkSummary,
} from '../review/diffParsing'

/**
 * Clamp a hunk index to the valid `[0, total-1]` range.
 *
 * Returns `0` for an empty hunk set (callers short-circuit before rendering in
 * that case, but the guard keeps the indicator honest during transient states).
 */
export function clampHunkIndex(index: number, total: number): number {
  if (total <= 0) return 0
  if (index < 0) return 0
  if (index > total - 1) return total - 1
  return index
}

/**
 * Display model for a single hunk in the file-viewer navigation combobox.
 *
 * - `index`: the hunk's position within the file (matches the flat nav order).
 * - `staged`: whether this hunk is already staged (informational only — the
 *   nav bar has no stage/unstage actions).
 * - `summary`: parsed add/remove counts + changed-line span in the new file.
 * - `rangeLabel`: `LX` / `LX-Y` label (new-file coordinates) for the trigger
 *   and dropdown rows.
 */
export interface FileHunkEntry {
  index: number
  staged: boolean
  summary: HunkSummary
  rangeLabel: string
}

/**
 * Build the combobox display entries from a file's structured hunks.
 *
 * The summary/range are derived by parsing each hunk's raw unified-diff block
 * (header + body). `old_start` / `new_start` anchor the line-number computation
 * exactly as the review combobox does.
 */
export function buildFileHunkEntries(hunks: HunkDiffInfo[]): FileHunkEntry[] {
  return hunks.map((hunk, index) => {
    const summary = summarizeHunk(hunk.diff, hunk.old_start, hunk.new_start)
    return {
      index,
      staged: hunk.staged,
      summary,
      rangeLabel: hunkLineRangeLabel(summary, hunk.new_start),
    }
  })
}
