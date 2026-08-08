import { describe, it, expect } from 'vitest'
import { parseHunkRaw, buildSideBySidePairs, summarizeHunk, hunkLineRangeLabel, type DiffLine } from './diffParsing'

// Minimal DiffLine builders. Only `type` matters to buildSideBySidePairs;
// the text/oldNum/newNum values are arbitrary but kept valid for realism.
// The array index (not these values) determines leftIdx/rightIdx output.
const ctx = (text: string): DiffLine => ({ type: 'context', text, oldNum: 0, newNum: 0 })
const del = (text: string): DiffLine => ({ type: 'del', text, oldNum: 0, newNum: null })
const add = (text: string): DiffLine => ({ type: 'add', text, oldNum: null, newNum: 0 })
const header = (text = '@@ -1,1 +1,1 @@'): DiffLine => ({ type: 'header', text, oldNum: null, newNum: null })
const noNewline = (): DiffLine => ({ type: 'noNewline', text: 'No newline at end of file', oldNum: null, newNum: null })

describe('parseHunkRaw', () => {
  it('classifies add/del/context/header/noNewline line types and slices off the marker char', () => {
    const raw = [
      '@@ -10,3 +10,3 @@',
      ' context-line',
      '-removed-line',
      '+added-line',
      '\\ No newline at end of file',
    ].join('\n')

    const lines = parseHunkRaw(raw, 10, 10)

    expect(lines).toHaveLength(5)
    expect(lines[0]).toEqual({ type: 'header', text: '@@ -10,3 +10,3 @@', oldNum: null, newNum: null })
    expect(lines[1]).toEqual({ type: 'context', text: 'context-line', oldNum: 10, newNum: 10 })
    expect(lines[2]).toEqual({ type: 'del', text: 'removed-line', oldNum: 11, newNum: null })
    expect(lines[3]).toEqual({ type: 'add', text: 'added-line', oldNum: null, newNum: 11 })
    expect(lines[4]).toEqual({ type: 'noNewline', text: 'No newline at end of file', oldNum: null, newNum: null })
  })

  it('increments oldNum/newNum independently for context, and only on the owning side for del/add', () => {
    // context advances both counters; del advances only oldNum; add only newNum.
    const raw = [' keep-a', ' keep-b', '-drop-c', '+grow-d', ' keep-e'].join('\n')

    const lines = parseHunkRaw(raw, 1, 1)

    expect(lines.map((l) => l.oldNum)).toEqual([1, 2, 3, null, 4])
    expect(lines.map((l) => l.newNum)).toEqual([1, 2, null, 3, 4])
  })

  it('treats a bare empty line as context advancing both counters', () => {
    const lines = parseHunkRaw(' ctx\n', 1, 1)
    expect(lines.map((l) => l.type)).toEqual(['context', 'context'])
    expect(lines.map((l) => l.oldNum)).toEqual([1, 2])
    expect(lines.map((l) => l.newNum)).toEqual([1, 2])
  })
})

describe('buildSideBySidePairs', () => {
  it('(a) all-context hunk: every row has leftIdx === rightIdx', () => {
    const lines = [ctx('c0'), ctx('c1'), ctx('c2')]

    expect(buildSideBySidePairs(lines)).toEqual([
      { leftIdx: 0, rightIdx: 0 },
      { leftIdx: 1, rightIdx: 1 },
      { leftIdx: 2, rightIdx: 2 },
    ])
  })

  it('(b) pure-deletion: left side filled, rightIdx null on every row', () => {
    const lines = [del('d0'), del('d1'), del('d2')]

    expect(buildSideBySidePairs(lines)).toEqual([
      { leftIdx: 0, rightIdx: null },
      { leftIdx: 1, rightIdx: null },
      { leftIdx: 2, rightIdx: null },
    ])
  })

  it('(c) pure-addition: right side filled, leftIdx null on every row', () => {
    const lines = [add('a0'), add('a1')]

    expect(buildSideBySidePairs(lines)).toEqual([
      { leftIdx: null, rightIdx: 0 },
      { leftIdx: null, rightIdx: 1 },
    ])
  })

  it('(d) interleaved del then add: rows zip pairwise (del[0] with add[0], …)', () => {
    // Alternating del/add — buffers pool together and zip by position.
    const lines = [del('d0'), add('a0'), del('d1'), add('a1')]

    expect(buildSideBySidePairs(lines)).toEqual([
      { leftIdx: 0, rightIdx: 1 }, // del@0 (d0) <-> add@1 (a0)
      { leftIdx: 2, rightIdx: 3 }, // del@2 (d1) <-> add@3 (a1)
    ])
  })

  it('(d2) grouped del-then-add: standard git hunk shape also zips pairwise', () => {
    // The canonical --- +++/+++ hunk: all deletions, then all additions.
    const lines = [del('d0'), del('d1'), add('a0'), add('a1')]

    expect(buildSideBySidePairs(lines)).toEqual([
      { leftIdx: 0, rightIdx: 2 }, // del@0 <-> add@2
      { leftIdx: 1, rightIdx: 3 }, // del@1 <-> add@3
    ])
  })

  it('(e) uneven del/add counts (3 del, 1 add): remainder padded with null', () => {
    const lines = [del('d0'), del('d1'), del('d2'), add('a0')]

    expect(buildSideBySidePairs(lines)).toEqual([
      { leftIdx: 0, rightIdx: 3 }, // only add pairs with first del
      { leftIdx: 1, rightIdx: null }, // leftover dels pad the right
      { leftIdx: 2, rightIdx: null },
    ])
  })

  it('(e2) uneven counts the other way (1 del, 2 add): leftover adds pad the left', () => {
    const lines = [del('d0'), add('a0'), add('a1')]

    expect(buildSideBySidePairs(lines)).toEqual([
      { leftIdx: 0, rightIdx: 1 }, // only del pairs with first add
      { leftIdx: null, rightIdx: 2 }, // leftover add pads the left
    ])
  })

  it('(f) header + noNewline lines propagate to both columns (leftIdx === rightIdx === index)', () => {
    const lines = [header('@@ -1,3 +1,3 @@'), del('d0'), add('a0'), noNewline()]

    expect(buildSideBySidePairs(lines)).toEqual([
      { leftIdx: 0, rightIdx: 0 }, // header -> both columns
      { leftIdx: 1, rightIdx: 2 }, // del@1 <-> add@2
      { leftIdx: 3, rightIdx: 3 }, // noNewline -> both columns
    ])
  })

  it('flushes pending del/add buffers when a context line interrupts them', () => {
    // del/add block, then context forces a flush, then another del/add block.
    const lines = [del('d0'), add('a0'), ctx('mid'), del('d1'), add('a1')]

    expect(buildSideBySidePairs(lines)).toEqual([
      { leftIdx: 0, rightIdx: 1 }, // first block zipped
      { leftIdx: 2, rightIdx: 2 }, // context both columns
      { leftIdx: 3, rightIdx: 4 }, // second block zipped
    ])
  })
})

describe('summarizeHunk', () => {
  it('counts added/removed lines and tracks the first/last new-file line of additions', () => {
    // @@ -10,4 +10,5 @@: context @10, del @11, add @11, add @12, context @13
    const raw = [' keep', '-old', '+new1', '+new2', ' tail'].join('\n')

    const summary = summarizeHunk(raw, 10, 10)

    expect(summary.added).toBe(2)
    expect(summary.removed).toBe(1)
    // additions land at new-file lines 11 and 12 (context took 10)
    expect(summary.firstNewLine).toBe(11)
    expect(summary.lastNewLine).toBe(12)
  })

  it('returns null line range for a pure-deletion hunk (no additions)', () => {
    const raw = ['-gone1', '-gone2'].join('\n')

    const summary = summarizeHunk(raw, 5, 5)

    expect(summary.added).toBe(0)
    expect(summary.removed).toBe(2)
    expect(summary.firstNewLine).toBeNull()
    expect(summary.lastNewLine).toBeNull()
  })

  it('counts a single addition with first === last', () => {
    const raw = [' ctx', '+solo'].join('\n')

    const summary = summarizeHunk(raw, 1, 1)

    expect(summary.added).toBe(1)
    expect(summary.removed).toBe(0)
    expect(summary.firstNewLine).toBe(2)
    expect(summary.lastNewLine).toBe(2)
  })

  it('ignores context and header lines in the counts', () => {
    const raw = ['@@ -1,2 +1,2 @@', ' keep', '+added'].join('\n')

    const summary = summarizeHunk(raw, 1, 1)

    expect(summary.added).toBe(1)
    expect(summary.removed).toBe(0)
  })
})

describe('hunkLineRangeLabel', () => {
  it('renders LX-Y when first and last addition lines differ', () => {
    const summary = { added: 2, removed: 1, firstNewLine: 11, lastNewLine: 15 }
    expect(hunkLineRangeLabel(summary, 10)).toBe('L11-15')
  })

  it('renders a single LX when first === last', () => {
    const summary = { added: 1, removed: 0, firstNewLine: 42, lastNewLine: 42 }
    expect(hunkLineRangeLabel(summary, 40)).toBe('L42')
  })

  it('falls back to the provided anchor for a pure-deletion hunk', () => {
    const summary = { added: 0, removed: 3, firstNewLine: null, lastNewLine: null }
    expect(hunkLineRangeLabel(summary, 7)).toBe('L7')
  })
})
