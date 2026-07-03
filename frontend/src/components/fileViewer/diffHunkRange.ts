// Pure mapping from a parsed diff hunk to the backend HunkRange (Phase 6).
//
// Extracted from DiffHunkStageBar so the component file exports only a
// component (react-refresh) and the mapping is independently unit-testable.

import type { DiffHunk } from '@/lib/diffParser'
import type { HunkRange } from '@/types/models'

/** Map a parsed diff hunk to the backend HunkRange (old-file lines).
 *  EndLine is StartLine + oldCount - 1, matching the backend's hunkInRange
 *  derivation. For a pure-addition hunk (oldCount == 0) EndLine == StartLine - 1,
 *  which is the value the backend expects — do NOT clamp end >= start here. */
export function hunkToRange(hunk: DiffHunk): HunkRange {
  const startLine = hunk.oldStart
  const endLine = hunk.oldStart + Math.max(hunk.oldCount, 0) - 1
  return { start_line: startLine, end_line: endLine }
}
