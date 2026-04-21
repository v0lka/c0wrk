import { describe, it, expect } from 'vitest'
import { parseUnifiedDiff, classifyLines, buildDisplayLines, computeCharDiff } from './diffParser'

describe('parseUnifiedDiff', () => {
  it('returns empty hunks for empty input', () => {
    const result = parseUnifiedDiff('')
    expect(result.hunks).toEqual([])
  })

  it('parses a single hunk with added lines', () => {
    const diff = [
      'diff --git a/test.txt b/test.txt',
      'index abc1234..def5678 100644',
      '--- a/test.txt',
      '+++ b/test.txt',
      '@@ -1,3 +1,4 @@',
      ' line1',
      '+added_line',
      ' line2',
      ' line3',
    ].join('\n')

    const result = parseUnifiedDiff(diff)
    expect(result.hunks).toHaveLength(1)

    const hunk = result.hunks[0]!
    expect(hunk.oldStart).toBe(1)
    expect(hunk.oldCount).toBe(3)
    expect(hunk.newStart).toBe(1)
    expect(hunk.newCount).toBe(4)
    expect(hunk.lines).toHaveLength(4)
    expect(hunk.lines[0]!.type).toBe('context')
    expect(hunk.lines[1]!.type).toBe('added')
    expect(hunk.lines[2]!.type).toBe('context')
    expect(hunk.lines[3]!.type).toBe('context')
  })

  it('parses a hunk with removed lines', () => {
    const diff = [
      '@@ -1,3 +1,2 @@',
      ' line1',
      '-removed_line',
      ' line2',
    ].join('\n')

    const result = parseUnifiedDiff(diff)
    expect(result.hunks).toHaveLength(1)

    const hunk = result.hunks[0]!
    expect(hunk.lines).toHaveLength(3)
    expect(hunk.lines[0]!.type).toBe('context')
    expect(hunk.lines[1]!.type).toBe('removed')
    expect(hunk.lines[1]!.content).toBe('removed_line')
    expect(hunk.lines[2]!.type).toBe('context')
  })

  it('parses multiple hunks', () => {
    const diff = [
      '@@ -1,3 +1,3 @@',
      ' line1',
      '-old',
      '+new',
      ' line2',
      '@@ -10,3 +10,3 @@',
      ' line10',
      '-old2',
      '+new2',
      ' line11',
    ].join('\n')

    const result = parseUnifiedDiff(diff)
    expect(result.hunks).toHaveLength(2)
    expect(result.hunks[0]!.lines).toHaveLength(4)
    expect(result.hunks[1]!.lines).toHaveLength(4)
  })

  it('assigns unique hunk IDs', () => {
    const diff = [
      '@@ -1,2 +1,2 @@',
      ' a',
      '-b',
      '+c',
      '@@ -5,2 +5,2 @@',
      ' d',
      '-e',
      '+f',
    ].join('\n')

    const result = parseUnifiedDiff(diff)
    expect(result.hunks[0]!.id).toBe('hunk-1-1')
    expect(result.hunks[1]!.id).toBe('hunk-5-5')
  })

  it('tracks line numbers correctly', () => {
    const diff = [
      '@@ -1,4 +1,5 @@',
      ' line1',
      '-line2',
      ' line3',
      '+inserted',
      '+another',
      ' line4',
    ].join('\n')

    const result = parseUnifiedDiff(diff)
    const hunk = result.hunks[0]!

    // line1: context, oldLine=1, newLine=1
    expect(hunk.lines[0]!.oldLineNo).toBe(1)
    expect(hunk.lines[0]!.newLineNo).toBe(1)

    // line2: removed, oldLine=2
    expect(hunk.lines[1]!.oldLineNo).toBe(2)
    expect(hunk.lines[1]!.newLineNo).toBeUndefined()

    // line3: context, oldLine=3, newLine=2
    expect(hunk.lines[2]!.oldLineNo).toBe(3)
    expect(hunk.lines[2]!.newLineNo).toBe(2)

    // inserted: added, newLine=3
    expect(hunk.lines[3]!.newLineNo).toBe(3)
    expect(hunk.lines[3]!.oldLineNo).toBeUndefined()

    // another: added, newLine=4
    expect(hunk.lines[4]!.newLineNo).toBe(4)

    // line4: context, oldLine=4, newLine=5
    expect(hunk.lines[5]!.oldLineNo).toBe(4)
    expect(hunk.lines[5]!.newLineNo).toBe(5)
  })

  it('skips "no newline at end of file" marker', () => {
    const diff = [
      '@@ -1 +1 @@',
      '-old',
      '\\ No newline at end of file',
      '+new',
    ].join('\n')

    const result = parseUnifiedDiff(diff)
    const hunk = result.hunks[0]!
    // Should have exactly 2 lines (old and new), not 3
    expect(hunk.lines).toHaveLength(2)
  })

  it('handles hunk header without count', () => {
    const diff = [
      '@@ -1 +1 @@',
      ' line1',
    ].join('\n')

    const result = parseUnifiedDiff(diff)
    expect(result.hunks).toHaveLength(1)
    expect(result.hunks[0]!.oldStart).toBe(1)
    expect(result.hunks[0]!.newStart).toBe(1)
  })
})

describe('classifyLines', () => {
  it('returns all normal lines when no hunks', () => {
    const result = classifyLines(5, [])
    expect(result).toHaveLength(5)
    for (const line of result) {
      expect(line.type).toBe('normal')
    }
  })

  it('classifies added lines correctly', () => {
    const diff = parseUnifiedDiff([
      '@@ -1,3 +1,4 @@',
      ' line1',
      '+added_line',
      ' line2',
      ' line3',
    ].join('\n'))

    const result = classifyLines(4, diff.hunks)
    expect(result).toHaveLength(4)

    // line1: normal
    expect(result[0]!.type).toBe('normal')
    // added_line: added
    expect(result[1]!.type).toBe('added')
    expect(result[1]!.hunkId).toBeDefined()
    // line2: normal
    expect(result[2]!.type).toBe('normal')
    // line3: normal
    expect(result[3]!.type).toBe('normal')
  })

  it('classifies removed lines as not present in working tree', () => {
    const diff = parseUnifiedDiff([
      '@@ -1,3 +1,2 @@',
      ' line1',
      '-removed',
      ' line2',
    ].join('\n'))

    // Working tree has 2 lines (line1, line2) — removed line is gone
    const result = classifyLines(2, diff.hunks)
    expect(result).toHaveLength(2)
    expect(result[0]!.type).toBe('normal')
    expect(result[1]!.type).toBe('normal')
  })

  it('assigns hunkId to added lines only', () => {
    const diff = parseUnifiedDiff([
      '@@ -1,2 +1,3 @@',
      ' ctx',
      '+added1',
      '+added2',
      ' ctx2',
    ].join('\n'))

    const result = classifyLines(4, diff.hunks)
    // ctx: no hunkId
    expect(result[0]!.hunkId).toBeUndefined()
    // added1: has hunkId
    expect(result[1]!.hunkId).toBeDefined()
    // added2: has hunkId
    expect(result[2]!.hunkId).toBeDefined()
    // ctx2: no hunkId
    expect(result[3]!.hunkId).toBeUndefined()
  })

  it('handles multiple hunks', () => {
    const diffText = [
      '@@ -1,2 +1,3 @@',
      ' line1',
      '+added_at_top',
      ' line2',
      '@@ -10,2 +11,3 @@',
      ' line10',
      '+added_at_bottom',
      ' line11',
    ].join('\n')

    const diff = parseUnifiedDiff(diffText)

    // Working tree: line1, added_at_top, line2, ... line10, added_at_bottom, line11
    // Total lines = 13 (3 from first hunk + middle + 3 from second hunk)
    // But we only have partial data, so test with a reasonable line count
    const result = classifyLines(15, diff.hunks)

    expect(result[0]!.lineNumber).toBe(1)
    expect(result[0]!.type).toBe('normal')

    expect(result[1]!.lineNumber).toBe(2)
    expect(result[1]!.type).toBe('added')

    expect(result[2]!.lineNumber).toBe(3)
    expect(result[2]!.type).toBe('normal')
  })

  it('produces correct line numbers', () => {
    const result = classifyLines(5, [])
    for (let i = 0; i < result.length; i++) {
      expect(result[i]!.lineNumber).toBe(i + 1)
    }
  })
})

describe('computeCharDiff', () => {
  it('returns single equal part for identical strings', () => {
    const result = computeCharDiff('hello', 'hello')
    expect(result).toEqual([{ type: 'equal', value: 'hello' }])
  })

  it('detects word-level additions', () => {
    const result = computeCharDiff('hello world', 'hello beautiful world')
    expect(result).toContainEqual({ type: 'added', value: 'beautiful ' })
  })

  it('detects word-level removals', () => {
    const result = computeCharDiff('hello beautiful world', 'hello world')
    expect(result).toContainEqual({ type: 'removed', value: 'beautiful ' })
  })

  it('handles completely different strings', () => {
    const result = computeCharDiff('old', 'new')
    const hasRemoved = result.some(p => p.type === 'removed')
    const hasAdded = result.some(p => p.type === 'added')
    expect(hasRemoved).toBe(true)
    expect(hasAdded).toBe(true)
  })

  it('handles empty strings', () => {
    const result = computeCharDiff('', '')
    expect(result).toEqual([])
  })

  it('handles addition to empty string', () => {
    const result = computeCharDiff('', 'new content')
    expect(result).toEqual([{ type: 'added', value: 'new content' }])
  })
})

describe('buildDisplayLines', () => {
  it('returns only normal lines when no hunks', () => {
    const lines = ['line1', 'line2', 'line3']
    const result = buildDisplayLines(lines, [])
    expect(result).toHaveLength(3)
    expect(result.every(dl => dl.type === 'normal')).toBe(true)
    expect(result[0]!.lineNumber).toBe(1)
    expect(result[1]!.lineNumber).toBe(2)
    expect(result[2]!.lineNumber).toBe(3)
  })

  it('includes removed lines in output', () => {
    const diff = parseUnifiedDiff([
      '@@ -1,3 +1,2 @@',
      ' line1',
      '-removed',
      ' line2',
    ].join('\n'))

    // Working tree: line1, line2 (removed line is gone)
    const lines = ['line1', 'line2']
    const result = buildDisplayLines(lines, diff.hunks)

    // Should have: normal(line1), removed(removed), normal(line2)
    expect(result).toHaveLength(3)
    expect(result[0]!.type).toBe('normal')
    expect(result[0]!.lineNumber).toBe(1)
    expect(result[1]!.type).toBe('removed')
    expect(result[1]!.lineNumber).toBeUndefined()
    expect(result[1]!.content).toBe('removed')
    expect(result[1]!.oldLineNumber).toBe(2)
    expect(result[2]!.type).toBe('normal')
    expect(result[2]!.lineNumber).toBe(2)
  })

  it('pairs removed+added as modified with charDiff', () => {
    const diff = parseUnifiedDiff([
      '@@ -1,3 +1,3 @@',
      ' line1',
      '-old_text',
      '+new_text',
      ' line2',
    ].join('\n'))

    const lines = ['line1', 'new_text', 'line2']
    const result = buildDisplayLines(lines, diff.hunks)

    // Should have: normal(line1), modified(new_text with charDiff), normal(line2)
    expect(result).toHaveLength(3)
    expect(result[0]!.type).toBe('normal')
    expect(result[1]!.type).toBe('modified')
    expect(result[1]!.lineNumber).toBe(2)
    expect(result[1]!.content).toBe('new_text')
    expect(result[1]!.oldContent).toBe('old_text')
    expect(result[1]!.charDiff).toBeDefined()
    expect(result[1]!.charDiff!.length).toBeGreaterThan(0)
    expect(result[2]!.type).toBe('normal')
  })

  it('handles more removed than added (pure removed + modified)', () => {
    const diff = parseUnifiedDiff([
      '@@ -1,4 +1,3 @@',
      ' line1',
      '-removed_only',
      '-old_text',
      '+new_text',
      ' line2',
    ].join('\n'))

    const lines = ['line1', 'new_text', 'line2']
    const result = buildDisplayLines(lines, diff.hunks)

    // Should have: normal(line1), removed(removed_only), modified(new_text), normal(line2)
    expect(result).toHaveLength(4)
    expect(result[0]!.type).toBe('normal')
    expect(result[1]!.type).toBe('removed')
    expect(result[1]!.content).toBe('removed_only')
    expect(result[2]!.type).toBe('modified')
    expect(result[2]!.content).toBe('new_text')
    expect(result[3]!.type).toBe('normal')
  })

  it('handles more added than removed (modified + pure added)', () => {
    const diff = parseUnifiedDiff([
      '@@ -1,3 +1,4 @@',
      ' line1',
      '-old_text',
      '+new_text',
      '+added_only',
      ' line2',
    ].join('\n'))

    const lines = ['line1', 'new_text', 'added_only', 'line2']
    const result = buildDisplayLines(lines, diff.hunks)

    // Should have: normal(line1), modified(new_text), added(added_only), normal(line2)
    expect(result).toHaveLength(4)
    expect(result[0]!.type).toBe('normal')
    expect(result[1]!.type).toBe('modified')
    expect(result[2]!.type).toBe('added')
    expect(result[2]!.content).toBe('added_only')
    expect(result[3]!.type).toBe('normal')
  })

  it('handles pure added lines (no preceding removed)', () => {
    const diff = parseUnifiedDiff([
      '@@ -1,2 +1,3 @@',
      ' line1',
      '+added_line',
      ' line2',
    ].join('\n'))

    const lines = ['line1', 'added_line', 'line2']
    const result = buildDisplayLines(lines, diff.hunks)

    expect(result).toHaveLength(3)
    expect(result[0]!.type).toBe('normal')
    expect(result[1]!.type).toBe('added')
    expect(result[1]!.lineNumber).toBe(2)
    expect(result[2]!.type).toBe('normal')
  })

  it('assigns hunkId to diff lines', () => {
    const diff = parseUnifiedDiff([
      '@@ -1,2 +1,2 @@',
      '-old',
      '+new',
    ].join('\n'))

    const lines = ['new']
    const result = buildDisplayLines(lines, diff.hunks)

    expect(result[0]!.hunkId).toBe('hunk-1-1')
  })

  it('handles multiple hunks', () => {
    const diff = parseUnifiedDiff([
      '@@ -1,2 +1,3 @@',
      ' line1',
      '+added_at_top',
      ' line2',
      '@@ -10,2 +11,3 @@',
      ' line10',
      '+added_at_bottom',
      ' line11',
    ].join('\n'))

    const lines = [
      'line1', 'added_at_top', 'line2',
      'line4', 'line5', 'line6', 'line7', 'line8', 'line9',
      'line10', 'added_at_bottom', 'line11',
    ]
    const result = buildDisplayLines(lines, diff.hunks)

    const addedTop = result.find(dl => dl.content === 'added_at_top')
    expect(addedTop!.type).toBe('added')

    const addedBottom = result.find(dl => dl.content === 'added_at_bottom')
    expect(addedBottom!.type).toBe('added')
  })
})
