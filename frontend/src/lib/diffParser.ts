import { diffWords } from 'diff'

// -- Types -------------------------------------------------------------------

export interface DiffLine {
  type: 'context' | 'added' | 'removed'
  oldLineNo?: number
  newLineNo?: number
  content: string
}

export interface DiffHunk {
  id: string
  oldStart: number
  oldCount: number
  newStart: number
  newCount: number
  lines: DiffLine[]
}

export interface ParseResult { hunks: DiffHunk[] }

export interface LineInfo {
  lineNumber: number
  type: 'normal' | 'added' | 'removed'
  hunkId?: string
}

export interface CharDiffPart {
  type: 'equal' | 'added' | 'removed'
  value: string
}

export interface DisplayLine {
  type: 'normal' | 'added' | 'removed' | 'modified'
  lineNumber?: number
  oldLineNumber?: number
  content: string
  oldContent?: string
  hunkId?: string
  charDiff?: CharDiffPart[]
}

// -- Parser ------------------------------------------------------------------

const HUNK_HEADER_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/

export function parseUnifiedDiff(diffText: string): ParseResult {
  if (!diffText) return { hunks: [] }
  const lines = diffText.split('\n')
  const hunks: DiffHunk[] = []
  let currentHunk: DiffHunk | null = null
  let oldLine = 0, newLine = 0

  for (const line of lines) {
    const match = line.match(HUNK_HEADER_RE)
    if (match) {
      if (currentHunk) {
        currentHunk.oldCount = oldLine - currentHunk.oldStart
        currentHunk.newCount = newLine - currentHunk.newStart
      }
      const oStart = parseInt(match[1]!, 10)
      const nStart = parseInt(match[3]!, 10)
      oldLine = oStart; newLine = nStart
      currentHunk = { id: `hunk-${oStart}-${nStart}`, oldStart: oStart, oldCount: 0, newStart: nStart, newCount: 0, lines: [] }
      hunks.push(currentHunk)
      continue
    }
    if (!currentHunk) continue
    if (line.startsWith('---') || line.startsWith('+++') || line.startsWith('diff ') || line.startsWith('index ') || line.startsWith('Binary')) continue
    if (line.startsWith('+')) { currentHunk.lines.push({ type: 'added', newLineNo: newLine, content: line.slice(1) }); newLine++ }
    else if (line.startsWith('-')) { currentHunk.lines.push({ type: 'removed', oldLineNo: oldLine, content: line.slice(1) }); oldLine++ }
    else if (line.startsWith(' ')) { currentHunk.lines.push({ type: 'context', oldLineNo: oldLine, newLineNo: newLine, content: line.slice(1) }); oldLine++; newLine++ }
    else if (line.startsWith('\\')) continue
  }
  if (currentHunk) { currentHunk.oldCount = oldLine - currentHunk.oldStart; currentHunk.newCount = newLine - currentHunk.newStart }
  return { hunks }
}

// -- Line classifier ---------------------------------------------------------

export function classifyLines(totalLines: number, hunks: DiffHunk[]): LineInfo[] {
  const newLineMap = new Map<number, { type: DiffLine['type']; hunkId: string }>()
  for (const hunk of hunks) {
    for (const dl of hunk.lines) {
      if ((dl.type === 'added' || dl.type === 'context') && dl.newLineNo !== undefined) {
        newLineMap.set(dl.newLineNo, { type: dl.type, hunkId: hunk.id })
      }
    }
  }
  const result: LineInfo[] = []
  for (let lineNo = 1; lineNo <= totalLines; lineNo++) {
    const entry = newLineMap.get(lineNo)
    if (entry) {
      result.push({ lineNumber: lineNo, type: entry.type === 'added' ? 'added' : 'normal', hunkId: entry.type === 'added' ? entry.hunkId : undefined })
    } else {
      result.push({ lineNumber: lineNo, type: 'normal' })
    }
  }
  return result
}

// -- Char diff ---------------------------------------------------------------

export function computeCharDiff(oldStr: string, newStr: string): CharDiffPart[] {
  const changes = diffWords(oldStr, newStr)
  const parts: CharDiffPart[] = []
  for (const change of changes) {
    if (change.added) parts.push({ type: 'added', value: change.value })
    else if (change.removed) parts.push({ type: 'removed', value: change.value })
    else parts.push({ type: 'equal', value: change.value })
  }
  // Merge consecutive same-type
  if (parts.length === 0) return []
  const merged: CharDiffPart[] = [parts[0]!]
  for (let i = 1; i < parts.length; i++) {
    const last = merged[merged.length - 1]!
    const cur = parts[i]!
    if (last.type === cur.type) last.value += cur.value
    else merged.push(cur)
  }
  return merged
}

// -- Display lines builder ---------------------------------------------------

export function buildDisplayLines(lines: string[], hunks: DiffHunk[]): DisplayLine[] {
  const result: DisplayLine[] = []
  let newLineNo = 1

  for (const hunk of hunks) {
    while (newLineNo < hunk.newStart) {
      result.push({ type: 'normal', lineNumber: newLineNo, content: lines[newLineNo - 1] ?? '' })
      newLineNo++
    }
    let i = 0
    while (i < hunk.lines.length) {
      const dl = hunk.lines[i]!
      if (dl.type === 'context') {
        result.push({ type: 'normal', lineNumber: dl.newLineNo, content: dl.content })
        newLineNo = dl.newLineNo! + 1; i++
      } else if (dl.type === 'removed') {
        const removed: DiffLine[] = []
        while (i < hunk.lines.length && hunk.lines[i]!.type === 'removed') { removed.push(hunk.lines[i]!); i++ }
        const added: DiffLine[] = []
        while (i < hunk.lines.length && hunk.lines[i]!.type === 'added') { added.push(hunk.lines[i]!); i++ }
        const pairCount = Math.min(removed.length, added.length)
        // Pure removed (unmatched)
        for (let r = 0; r < removed.length - pairCount; r++) {
          result.push({ type: 'removed', oldLineNumber: removed[r]!.oldLineNo, content: removed[r]!.content, hunkId: hunk.id })
        }
        // Modified pairs
        for (let p = 0; p < pairCount; p++) {
          const rem = removed[removed.length - pairCount + p]!
          const add = added[p]!
          result.push({ type: 'modified', lineNumber: add.newLineNo, oldLineNumber: rem.oldLineNo, content: add.content, oldContent: rem.content, hunkId: hunk.id, charDiff: computeCharDiff(rem.content, add.content) })
          newLineNo = add.newLineNo! + 1
        }
        // Pure added (unmatched)
        for (let a = pairCount; a < added.length; a++) {
          result.push({ type: 'added', lineNumber: added[a]!.newLineNo, content: added[a]!.content, hunkId: hunk.id })
          newLineNo = added[a]!.newLineNo! + 1
        }
      } else if (dl.type === 'added') {
        result.push({ type: 'added', lineNumber: dl.newLineNo, content: dl.content, hunkId: hunk.id })
        newLineNo = dl.newLineNo! + 1; i++
      } else { i++ }
    }
  }
  while (newLineNo <= lines.length) {
    result.push({ type: 'normal', lineNumber: newLineNo, content: lines[newLineNo - 1] ?? '' })
    newLineNo++
  }
  return result
}
