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
