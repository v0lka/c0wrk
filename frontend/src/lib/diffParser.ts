import { diffWords } from 'diff'

/**
 * Diff parser and line classifier for rendering inline git diffs.
 *
 * parseUnifiedDiff: parses unified diff output (from `git diff`) into
 * structured hunks with line-level detail.
 *
 * classifyLines: maps file lines to their diff status (added/removed/normal)
 * with hunk IDs for forward-compatible interactive features (stage/discard).
 *
 * buildDisplayLines: produces an ordered list of display lines that includes
 * removed lines and character-level diffs for modified lines.
 */

// -- Types -------------------------------------------------------------------

export interface DiffLine {
  type: 'context' | 'added' | 'removed'
  oldLineNo?: number // line number in old file (removed/context lines)
  newLineNo?: number // line number in new file (added/context lines)
  content: string // line content without +/-
}

export interface DiffHunk {
  id: string // unique hunk identifier for data-hunk-id
  oldStart: number // starting line in old file
  oldCount: number
  newStart: number // starting line in new file
  newCount: number
  lines: DiffLine[]
}

export interface ParseResult {
  hunks: DiffHunk[]
}

export interface LineInfo {
  lineNumber: number // 1-based line number in the working tree file
  type: 'normal' | 'added' | 'removed'
  hunkId?: string // reference to the hunk this line belongs to
}

export interface CharDiffPart {
  type: 'equal' | 'added' | 'removed'
  value: string
}

export interface DisplayLine {
  type: 'normal' | 'added' | 'removed' | 'modified'
  /** 1-based line number in the new (working tree) file; absent for pure removed lines */
  lineNumber?: number
  /** 1-based line number in the old file; present for removed/modified lines */
  oldLineNumber?: number
  /** Line content from the new file (for normal/added/modified); from the old file (for removed) */
  content: string
  /** Original content from the old file, only for modified lines */
  oldContent?: string
  hunkId?: string
  /** Character-level diff segments, only for modified lines */
  charDiff?: CharDiffPart[]
}

// -- Parser ------------------------------------------------------------------

// Matches: @@ -oldStart,oldCount +newStart,newCount @@
const HUNK_HEADER_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/

/**
 * Parse unified diff output into structured hunks.
 *
 * Handles output from `git diff` and `git diff --cached`.
 * Returns an empty hunks array for empty input.
 */
export function parseUnifiedDiff(diffText: string): ParseResult {
  if (!diffText) return { hunks: [] }

  const lines = diffText.split('\n')
  const hunks: DiffHunk[] = []
  let currentHunk: DiffHunk | null = null

  let oldLine = 0
  let newLine = 0

  for (const line of lines) {
    // Try to match a hunk header
    const match = line.match(HUNK_HEADER_RE)
    if (match) {
      // Flush previous hunk
      if (currentHunk) {
        currentHunk.oldCount = oldLine - currentHunk.oldStart
        currentHunk.newCount = newLine - currentHunk.newStart
      }

      const oStart = parseInt(match[1]!, 10)
      const nStart = parseInt(match[3]!, 10)
      oldLine = oStart
      newLine = nStart

      currentHunk = {
        id: `hunk-${oStart}-${nStart}`,
        oldStart: oStart,
        oldCount: 0,
        newStart: nStart,
        newCount: 0,
        lines: [],
      }
      hunks.push(currentHunk)
      continue
    }

    // Skip diff header lines (---, +++, diff --git, index, etc.)
    if (!currentHunk) continue
    if (line.startsWith('---') || line.startsWith('+++') || line.startsWith('diff ') || line.startsWith('index ') || line.startsWith('Binary')) {
      continue
    }

    // Parse diff lines within a hunk
    if (line.startsWith('+')) {
      currentHunk.lines.push({
        type: 'added',
        newLineNo: newLine,
        content: line.slice(1),
      })
      newLine++
    } else if (line.startsWith('-')) {
      currentHunk.lines.push({
        type: 'removed',
        oldLineNo: oldLine,
        content: line.slice(1),
      })
      oldLine++
    } else if (line.startsWith(' ')) {
      currentHunk.lines.push({
        type: 'context',
        oldLineNo: oldLine,
        newLineNo: newLine,
        content: line.slice(1),
      })
      oldLine++
      newLine++
    } else if (line.startsWith('\\')) {
      // "\ No newline at end of file" — skip
      continue
    }
    // Any other line is ignored (shouldn't happen in valid diff)
  }

  // Flush last hunk
  if (currentHunk) {
    currentHunk.oldCount = oldLine - currentHunk.oldStart
    currentHunk.newCount = newLine - currentHunk.newStart
  }

  return { hunks }
}

// -- Line classifier ---------------------------------------------------------

/**
 * Classify each line of the working tree file based on parsed diff hunks.
 *
 * For "added" lines, `lineNumber` corresponds to the line in the working tree.
 * For "removed" lines, `lineNumber` is 0 (they don't exist in the working tree)
 * and the line is NOT included in the result — removed lines are shown via
 * the diff overlay on the original content.
 *
 * The algorithm walks through the file line-by-line, advancing through
 * context/added/removed diff lines to determine each file line's status.
 */
export function classifyLines(totalLines: number, hunks: DiffHunk[]): LineInfo[] {
  const result: LineInfo[] = []

  // Flatten all diff lines with their hunk IDs for sequential processing
  type DiffEntry = { type: DiffLine['type']; newLineNo?: number; oldLineNo?: number; hunkId: string }
  const diffEntries: DiffEntry[] = []
  for (const hunk of hunks) {
    for (const dl of hunk.lines) {
      diffEntries.push({
        type: dl.type,
        newLineNo: dl.newLineNo,
        oldLineNo: dl.oldLineNo,
        hunkId: hunk.id,
      })
    }
  }

  // Build a map: newLineNo → DiffEntry for context/added lines
  const newLineMap = new Map<number, DiffEntry>()
  for (const entry of diffEntries) {
    if (entry.type === 'added' || entry.type === 'context') {
      if (entry.newLineNo !== undefined) {
        newLineMap.set(entry.newLineNo, entry)
      }
    }
  }

  // For each line in the working tree (1-based), look up its status
  for (let lineNo = 1; lineNo <= totalLines; lineNo++) {
    const entry = newLineMap.get(lineNo)
    if (entry) {
      result.push({
        lineNumber: lineNo,
        type: entry.type === 'added' ? 'added' : 'normal',
        hunkId: entry.type === 'added' || entry.type === 'removed' ? entry.hunkId : undefined,
      })
    } else {
      result.push({
        lineNumber: lineNo,
        type: 'normal',
      })
    }
  }

  return result
}

// -- Display lines builder --------------------------------------------------

/**
 * Compute character-level diff between two strings using word-level diffing.
 * Returns segments of equal/added/removed parts for inline rendering.
 */
export function computeCharDiff(oldStr: string, newStr: string): CharDiffPart[] {
  const changes = diffWords(oldStr, newStr)
  const result: CharDiffPart[] = []

  for (const change of changes) {
    if (change.added) {
      result.push({ type: 'added', value: change.value })
    } else if (change.removed) {
      result.push({ type: 'removed', value: change.value })
    } else {
      result.push({ type: 'equal', value: change.value })
    }
  }

  return mergeConsecutiveParts(result)
}

function mergeConsecutiveParts(parts: CharDiffPart[]): CharDiffPart[] {
  if (parts.length === 0) return []
  const merged: CharDiffPart[] = [parts[0]!]
  for (let i = 1; i < parts.length; i++) {
    const last = merged[merged.length - 1]!
    const cur = parts[i]!
    if (last.type === cur.type) {
      last.value += cur.value
    } else {
      merged.push(cur)
    }
  }
  return merged
}

/**
 * Build an ordered list of display lines from the working tree content
 * and parsed diff hunks. Unlike classifyLines, this includes removed lines
 * (with no lineNumber) and produces character-level diffs for modifications
 * (consecutive removed+added pairs within a hunk).
 */
export function buildDisplayLines(lines: string[], hunks: DiffHunk[]): DisplayLine[] {
  const result: DisplayLine[] = []
  let newLineNo = 1 // current position in the new file

  for (const hunk of hunks) {
    // Emit normal lines before this hunk starts
    while (newLineNo < hunk.newStart) {
      result.push({
        type: 'normal',
        lineNumber: newLineNo,
        content: lines[newLineNo - 1] ?? '',
      })
      newLineNo++
    }

    // Process hunk lines
    let i = 0
    while (i < hunk.lines.length) {
      const dl = hunk.lines[i]!

      if (dl.type === 'context') {
        result.push({
          type: 'normal',
          lineNumber: dl.newLineNo,
          content: dl.content,
        })
        newLineNo = dl.newLineNo! + 1
        i++
      } else if (dl.type === 'removed') {
        // Collect consecutive removed lines
        const removedLines: DiffLine[] = []
        while (i < hunk.lines.length && hunk.lines[i]!.type === 'removed') {
          removedLines.push(hunk.lines[i]!)
          i++
        }

        // Collect consecutive added lines that follow (if any)
        const addedLines: DiffLine[] = []
        while (i < hunk.lines.length && hunk.lines[i]!.type === 'added') {
          addedLines.push(hunk.lines[i]!)
          i++
        }

        // Pair removed+added as modifications
        const pairCount = Math.min(removedLines.length, addedLines.length)

        // Emit pure removed lines (those without a matching added line)
        for (let r = 0; r < removedLines.length - pairCount; r++) {
          result.push({
            type: 'removed',
            oldLineNumber: removedLines[r]!.oldLineNo,
            content: removedLines[r]!.content,
            hunkId: hunk.id,
          })
        }

        // Emit modified lines (paired removed+added with char diff)
        for (let p = 0; p < pairCount; p++) {
          const removed = removedLines[removedLines.length - pairCount + p]!
          const added = addedLines[p]!
          result.push({
            type: 'modified',
            lineNumber: added.newLineNo,
            oldLineNumber: removed.oldLineNo,
            content: added.content,
            oldContent: removed.content,
            hunkId: hunk.id,
            charDiff: computeCharDiff(removed.content, added.content),
          })
          newLineNo = added.newLineNo! + 1
        }

        // Emit pure added lines (those without a matching removed line)
        for (let a = pairCount; a < addedLines.length; a++) {
          result.push({
            type: 'added',
            lineNumber: addedLines[a]!.newLineNo,
            content: addedLines[a]!.content,
            hunkId: hunk.id,
          })
          newLineNo = addedLines[a]!.newLineNo! + 1
        }
      } else if (dl.type === 'added') {
        // Pure added line (no preceding removed)
        result.push({
          type: 'added',
          lineNumber: dl.newLineNo,
          content: dl.content,
          hunkId: hunk.id,
        })
        newLineNo = dl.newLineNo! + 1
        i++
      } else {
        i++
      }
    }
  }

  // Emit remaining normal lines after last hunk
  while (newLineNo <= lines.length) {
    result.push({
      type: 'normal',
      lineNumber: newLineNo,
      content: lines[newLineNo - 1] ?? '',
    })
    newLineNo++
  }

  return result
}
