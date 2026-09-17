// Block segmentation of a paper's extracted source text (source.md).
//
// The Extracted sub-view of the Source section renders the source as REAL
// markdown (headings, tables, code, …) rather than raw lines, but its anchors
// resolve to 0-based LINE indexes (see lib/paperAnchors). Splitting the
// document into blank-line-separated blocks keeps both: every block records
// the source line range it was built from, so a resolved line maps to exactly
// one block, and each block is a self-contained markdown document for the
// renderer. Fenced code blocks are kept intact — a blank line inside a ``` or
// ~~~ fence does not split a block (it would break the code rendering).
//
// Pure over strings: no React, no DOM, no I/O.

/** One blank-line-separated markdown block with its source line range. */
export interface PaperSourceBlock {
  /** The block's markdown text (its lines, joined back with '\n'). */
  text: string
  /** 0-based source line index of the block's first line. */
  startLine: number
  /** 0-based source line index of the block's last line. */
  endLine: number
}

/** An opening (or closing) code fence: ≤3 leading spaces, then ``` or ~~~. */
const FENCE_RE = /^\s{0,3}(?:```|~~~)/

/**
 * Split a source document into markdown blocks. Blocks are separated by one
 * or more blank lines; blank lines INSIDE a fenced code block belong to the
 * fence's block. Every non-blank line lands in exactly one block, and the
 * blocks' line ranges tile the document's non-blank lines in order.
 */
export function splitSourceBlocks(source: string): PaperSourceBlock[] {
  if (source === '') return []
  const lines = source.split('\n')
  const blocks: PaperSourceBlock[] = []
  let start = -1
  let inFence = false
  const close = (end: number): void => {
    blocks.push({
      text: lines.slice(start, end + 1).join('\n'),
      startLine: start,
      endLine: end,
    })
    start = -1
  }
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!
    if (inFence) {
      // Only a matching fence line closes the block; everything else (blank
      // lines included) is part of it.
      if (FENCE_RE.test(line)) inFence = false
      continue
    }
    if (FENCE_RE.test(line)) {
      inFence = true
      if (start < 0) start = i
      continue
    }
    if (line.trim() === '') {
      if (start >= 0) close(i - 1)
      continue
    }
    if (start < 0) start = i
  }
  if (start >= 0) close(lines.length - 1)
  return blocks
}

/**
 * The block whose line range contains `line`, or null when the line falls
 * outside every block (a blank separator line, or beyond the document).
 */
export function blockContainingLine(
  blocks: PaperSourceBlock[],
  line: number,
): PaperSourceBlock | null {
  for (const block of blocks) {
    if (line >= block.startLine && line <= block.endLine) return block
  }
  return null
}
