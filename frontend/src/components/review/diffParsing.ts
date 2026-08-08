/**
 * Pure diff-hunk parsing utilities shared by the review components.
 *
 * This module is intentionally React/DOM-free so the parsing and
 * side-by-side alignment logic can be unit-tested in isolation.
 */

export interface DiffLine {
  type: 'add' | 'del' | 'context' | 'header' | 'noNewline'
  text: string
  oldNum: number | null
  newNum: number | null
}

export function parseHunkRaw(raw: string, oldStart: number, newStart: number): DiffLine[] {
  const lines = raw.split('\n')
  const result: DiffLine[] = []
  let oldNum = oldStart
  let newNum = newStart

  for (const line of lines) {
    if (line.startsWith('@@')) {
      result.push({ type: 'header', text: line, oldNum: null, newNum: null })
    } else if (line.startsWith('+')) {
      result.push({ type: 'add', text: line.slice(1), oldNum: null, newNum: newNum++ })
    } else if (line.startsWith('-')) {
      result.push({ type: 'del', text: line.slice(1), oldNum: oldNum++, newNum: null })
    } else if (line.startsWith(' ')) {
      result.push({ type: 'context', text: line.slice(1), oldNum: oldNum++, newNum: newNum++ })
    } else if (line === '') {
      result.push({ type: 'context', text: '', oldNum: oldNum++, newNum: newNum++ })
    } else if (line.startsWith('\\')) {
      result.push({ type: 'noNewline', text: 'No newline at end of file', oldNum: null, newNum: null })
    }
  }
  return result
}

/**
 * Summary of the actual changes inside a hunk's raw body, derived by parsing.
 *
 * - `added` / `removed`: counts of `+` / `-` lines.
 * - `firstNewLine` / `lastNewLine`: the first and last *changed* line numbers
 *   in the NEW file (the green `+` lines). Both are `null` for a pure-deletion
 *   hunk (no additions), in which case the caller falls back to the hunk's
 *   `new_start` anchor for a coordinate reference.
 *
 * React/DOM-free so it is unit-testable alongside {@link parseHunkRaw}.
 */
export interface HunkSummary {
  added: number
  removed: number
  firstNewLine: number | null
  lastNewLine: number | null
}

export function summarizeHunk(
  raw: string,
  oldStart: number,
  newStart: number,
): HunkSummary {
  const lines = parseHunkRaw(raw, oldStart, newStart)
  let added = 0
  let removed = 0
  let firstNewLine: number | null = null
  let lastNewLine: number | null = null
  for (const line of lines) {
    if (line.type === 'add') {
      added++
      if (firstNewLine === null) firstNewLine = line.newNum
      // `add` lines always carry a new-file number, so this is non-null here.
      lastNewLine = line.newNum
    } else if (line.type === 'del') {
      removed++
    }
  }
  return { added, removed, firstNewLine, lastNewLine }
}

/**
 * Format a hunk's changed-line range as an `LX` / `LX-Y` label (new-file
 * coordinates). For a pure-deletion hunk (no additions) the range is undefined
 * in the new file, so `fallbackLine` (typically the hunk's `new_start`) is
 * shown as a single-line anchor.
 */
export function hunkLineRangeLabel(summary: HunkSummary, fallbackLine: number): string {
  if (summary.firstNewLine !== null && summary.lastNewLine !== null) {
    return summary.firstNewLine === summary.lastNewLine
      ? `L${summary.firstNewLine}`
      : `L${summary.firstNewLine}-${summary.lastNewLine}`
  }
  return `L${fallbackLine}`
}

/**
 * Background classes per diff line type.
 *
 * Add/del lines keep only a background tint — the text color comes from
 * the syntax-highlighting spans (hljs-* classes) so the actual code
 * language is visible. The background + line-number gutter still
 * distinguish added from deleted lines.
 */
export const LINE_BG: Record<DiffLine['type'], string> = {
  add: 'bg-success/10',
  del: 'bg-destructive/10',
  context: '',
  header: 'bg-info/10 text-info font-medium',
  noNewline: 'text-muted-foreground/50 italic',
}

/**
 * A single row in a side-by-side diff layout. The indices point back into
 * the source `DiffLine[]` (and thus into the aligned `highlightedLines[]`),
 * so the caller can look up line content and highlight markup by index.
 * A `null` index means "no line on this side" (padded empty cell).
 */
export interface SideBySideRow {
  leftIdx: number | null
  rightIdx: number | null
}

/**
 * Build left/right-aligned rows from a flat `DiffLine[]` using the standard
 * zip-flush algorithm.
 *
 * Walks the lines buffering consecutive `del` and `add` lines separately.
 * On any non-del/non-add line (context/header/noNewline) — or at end of
 * input — the two buffers are flushed by zipping them pairwise and padding
 * the remainder side with `null`. Context/header/noNewline lines are emitted
 * with `leftIdx === rightIdx === index` so both columns show the same line.
 *
 * @param lines Flat list of diff lines (e.g. from {@link parseHunkRaw}).
 * @returns Aligned rows where del lines map to the left column and add
 * lines map to the right column.
 */
export function buildSideBySidePairs(lines: DiffLine[]): SideBySideRow[] {
  const rows: SideBySideRow[] = []
  let delBuffer: number[] = []
  let addBuffer: number[] = []

  const flush = () => {
    const maxLen = Math.max(delBuffer.length, addBuffer.length)
    for (let i = 0; i < maxLen; i++) {
      rows.push({
        // When one buffer is shorter than the other, the missing index is
        // genuinely absent — coerce undefined to null (padded empty cell).
        leftIdx: delBuffer[i] ?? null,
        rightIdx: addBuffer[i] ?? null,
      })
    }
    delBuffer = []
    addBuffer = []
  }

  for (const [i, line] of lines.entries()) {
    if (line.type === 'del') {
      delBuffer.push(i)
    } else if (line.type === 'add') {
      addBuffer.push(i)
    } else {
      // context / header / noNewline — flush pending del/add buffers, then
      // emit this line with both columns pointing at the same index.
      flush()
      rows.push({ leftIdx: i, rightIdx: i })
    }
  }
  flush()

  return rows
}
