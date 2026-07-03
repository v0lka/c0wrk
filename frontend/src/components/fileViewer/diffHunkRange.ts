// Pure mapping from a parsed diff hunk to the backend HunkRange (Phase 6).
//
// Extracted from DiffHunkStageBar so the component file exports only a
// component (react-refresh) and the mapping is independently unit-testable.

import type { DiffHunk } from '@/lib/diffParser'
import type { HunkRange } from '@/types/models'

/** Map a parsed diff hunk to the backend HunkRange (old-file lines). */
export function hunkToRange(hunk: DiffHunk): HunkRange {
  const startLine = hunk.oldStart
  const endLine = hunk.oldStart + Math.max(hunk.oldCount, 0) - 1
  return { start_line: startLine, end_line: Math.max(endLine, startLine) }
}
