// Pure mapping from a structured diff hunk to the backend HunkRange.
//
// Extracted from DiffHunkStageBar so the component file exports only a
// component (react-refresh) and the mapping is independently unit-testable.
//
// Accepts HunkDiffInfo (the backend's structured hunk type) which carries
// the same old-file line coordinates that the backend's hunkInRange expects.

import type { HunkDiffInfo } from '@/types/models'
import type { HunkRange } from '@/types/models'

/** Map a structured diff hunk to the backend HunkRange (old-file lines).
 *  EndLine is StartLine + oldCount - 1, matching the backend's hunkInRange
 *  derivation. For a pure-addition hunk (oldCount == 0) EndLine == StartLine - 1,
 *  which is the value the backend expects — do NOT clamp end >= start here. */
export function hunkToRange(hunk: HunkDiffInfo): HunkRange {
  const startLine = hunk.old_start
  const endLine = hunk.old_start + Math.max(hunk.old_count, 0) - 1
  return { start_line: startLine, end_line: endLine }
}
