// Unit tests for gitGraphLayout — pure commit-graph lane layout (Phase 6)

import { describe, it, expect } from 'vitest'
import { computeGraphLayout, computeRowYLayout, shortSha, type RowYLayout } from './gitGraphLayout'
import type { GitHistoryCommit } from '@/types/models'

/** Helper: build a commit with minimal boilerplate. */
function commit(sha: string, parents: string[], message = sha, refs: string[] = []): GitHistoryCommit {
  return { sha, parents, author: '', email: '', date: '', message, refs }
}

describe('shortSha', () => {
  it('returns first 7 chars of a full SHA', () => {
    expect(shortSha('abcdef1234567890')).toBe('abcdef1')
  })

  it('returns the whole string when shorter than 7 chars', () => {
    expect(shortSha('abc')).toBe('abc')
  })

  it('returns exactly 7 chars for a 7-char input', () => {
    expect(shortSha('abcdefg')).toBe('abcdefg')
  })

  it('returns empty string for empty input', () => {
    expect(shortSha('')).toBe('')
  })
})

describe('computeGraphLayout', () => {
  it('returns empty array for empty input', () => {
    expect(computeGraphLayout([])).toEqual([])
  })

  it('places a single root commit at lane 0, row 0 with no parent edges', () => {
    const nodes = computeGraphLayout([commit('a', [])])
    expect(nodes).toHaveLength(1)
    expect(nodes[0]).toMatchObject({ sha: 'a', lane: 0, row: 0, isMerge: false })
    expect(nodes[0]!.parents).toEqual([])
  })

  it('places a linear chain all in lane 0 with edges pointing to lane 0', () => {
    // newest-first: c -> b -> a (each has the previous as its single parent)
    const nodes = computeGraphLayout([
      commit('c', ['b']),
      commit('b', ['a']),
      commit('a', []),
    ])
    expect(nodes.map((n) => n.lane)).toEqual([0, 0, 0])
    expect(nodes.map((n) => n.row)).toEqual([0, 1, 2])
    expect(nodes[0]!.parents).toEqual([{ sha: 'b', lane: 0 }])
    expect(nodes[1]!.parents).toEqual([{ sha: 'a', lane: 0 }])
    expect(nodes[2]!.parents).toEqual([])
    expect(nodes.every((n) => !n.isMerge)).toBe(true)
  })

  it('forks a branch into a new lane', () => {
    // a is root; b and c both descend from a (newest-first: c, b, a)
    const nodes = computeGraphLayout([
      commit('c', ['a']),
      commit('b', ['a']),
      commit('a', []),
    ])
    // c reuses lane 0; b forks into lane 1; a returns to lane 0.
    expect(nodes[0]!.lane).toBe(0) // c
    expect(nodes[1]!.lane).toBe(1) // b
    expect(nodes[2]!.lane).toBe(0) // a
    // Both children point back to a; a occupies lane 0 for both edges.
    expect(nodes[0]!.parents).toEqual([{ sha: 'a', lane: 0 }])
    expect(nodes[1]!.parents).toEqual([{ sha: 'a', lane: 0 }])
  })

  it('marks a merge commit and routes both parents to their lanes', () => {
    // m merges b and c; both b and c descend from a (newest-first: m, b, c, a)
    const nodes = computeGraphLayout([
      commit('m', ['b', 'c']),
      commit('b', ['a']),
      commit('c', ['a']),
      commit('a', []),
    ])
    expect(nodes[0]!.isMerge).toBe(true)
    expect(nodes.slice(1).every((n) => !n.isMerge)).toBe(true)
    // m's parents route to b (lane 0) and c (lane 1).
    expect(nodes[0]!.parents).toEqual([
      { sha: 'b', lane: 0 },
      { sha: 'c', lane: 1 },
    ])
    // a ends up in lane 0; both b and c edges converge there.
    expect(nodes[3]!.lane).toBe(0)
    expect(nodes[1]!.parents).toEqual([{ sha: 'a', lane: 0 }])
    expect(nodes[2]!.parents).toEqual([{ sha: 'a', lane: 0 }])
  })

  it('reuses an existing lane for a shared parent', () => {
    // Two commits x and y share parent p (newest-first: y, x, p).
    // x is placed first (lane 0, parent p → lane 0). y should find p already
    // travelling lane 0 and reuse it for its parent edge.
    const nodes = computeGraphLayout([
      commit('y', ['p']),
      commit('x', ['p']),
      commit('p', []),
    ])
    // Both x and y point their parent edge to lane 0 (where p travels).
    expect(nodes[0]!.parents).toEqual([{ sha: 'p', lane: 0 }])
    expect(nodes[1]!.parents).toEqual([{ sha: 'p', lane: 0 }])
    expect(nodes[2]!.sha).toBe('p')
  })

  it('preserves message and refs on each node', () => {
    const nodes = computeGraphLayout([
      commit('a', [], 'feat: init', ['HEAD -> main', 'tag: v1.0']),
    ])
    expect(nodes[0]!.message).toBe('feat: init')
    expect(nodes[0]!.refs).toEqual(['HEAD -> main', 'tag: v1.0'])
  })

  it('assigns sequential row indices in input order', () => {
    const nodes = computeGraphLayout([
      commit('a', []),
      commit('b', ['a']),
      commit('c', ['b']),
      commit('d', ['c']),
    ])
    expect(nodes.map((n) => n.row)).toEqual([0, 1, 2, 3])
  })
})

describe('computeRowYLayout', () => {
  it('reproduces the fixed-height formula when all rows equal rowSpacing (backward compat)', () => {
    const rowSpacing = 28
    const rowHeights = [28, 28, 28, 28, 28]
    const { yFor, totalHeight } = computeRowYLayout(rowHeights, rowSpacing)
    for (let r = 0; r < rowHeights.length; r++) {
      expect(yFor(r)).toBe(r * rowSpacing + rowSpacing / 2)
    }
    expect(totalHeight).toBe(rowHeights.length * rowSpacing + rowSpacing / 2)
  })

  it('matches the GitGraph.tsx formula exactly for fixed heights (r*28+14)', () => {
    const { yFor, totalHeight } = computeRowYLayout([28, 28, 28, 28, 28], 28)
    expect(yFor(0)).toBe(14)
    expect(yFor(1)).toBe(42)
    expect(yFor(2)).toBe(70)
    expect(yFor(3)).toBe(98)
    expect(yFor(4)).toBe(126)
    expect(totalHeight).toBe(5 * 28 + 14) // 154
  })

  it('pushes all later rows down when one row is expanded', () => {
    // Row 1 expands from 28 to 80 — a 52px delta that offsets every later row.
    // The node sits at the TOP of each row (center of the commit-line area),
    // so yFor(r) = sum(prev heights) + rowSpacing/2.
    const { yFor, totalHeight } = computeRowYLayout([28, 80, 28, 28], 28)
    expect(yFor(0)).toBe(14) // 0 + 28/2
    expect(yFor(1)).toBe(42) // 28 + 28/2 (node at top of the expanded row)
    expect(yFor(2)).toBe(122) // 28 + 80 + 28/2
    expect(yFor(3)).toBe(150) // 28 + 80 + 28 + 28/2
    expect(totalHeight).toBe(28 + 80 + 28 + 28 + 14) // 178
  })

  it('handles empty rowHeights with a half-spacing total height', () => {
    const { yFor, totalHeight } = computeRowYLayout([], 28)
    expect(totalHeight).toBe(28 / 2) // 14 — just the bottom pad, no rows
    // No rows exist; yFor(0) returns the bottom content edge (0 for empty).
    expect(yFor(0)).toBe(0)
  })

  it('lays out correctly when every row is expanded', () => {
    // Node at top of each row: yFor(r) = sum(prev) + rowSpacing/2.
    const { yFor, totalHeight } = computeRowYLayout([60, 60, 60], 28)
    expect(yFor(0)).toBe(14) // 0 + 28/2
    expect(yFor(1)).toBe(74) // 60 + 28/2
    expect(yFor(2)).toBe(134) // 120 + 28/2
    expect(totalHeight).toBe(60 + 60 + 60 + 28 / 2) // 180 + 14 = 194
  })

  it('does not mutate the input rowHeights array', () => {
    const rowHeights = [28, 80, 28, 28]
    const snapshot = [...rowHeights]
    computeRowYLayout(rowHeights, 28)
    expect(rowHeights).toEqual(snapshot)
  })

  it('returns a yFor closure independent of later caller mutations', () => {
    const rowHeights = [28, 80, 28, 28]
    const { yFor } = computeRowYLayout(rowHeights, 28)
    const before = yFor(2)
    rowHeights[1] = 999 // mutate after construction
    expect(yFor(2)).toBe(before) // internal prefix-sum snapshot is unaffected
  })

  it('handles out-of-range rows defensively without NaN', () => {
    const { yFor } = computeRowYLayout([28, 80], 28)
    expect(yFor(-1)).toBe(0) // above the graph → top edge
    expect(yFor(5)).toBe(28 + 80) // beyond last row → bottom content edge
    expect(Number.isNaN(yFor(5))).toBe(false)
  })

  it('uses nodeOffset to position nodes within each row', () => {
    // With nodeOffset=11, the node sits 11px from the top of each row
    // (aligning with the first line of a two-line row), not at the center.
    const { yFor, totalHeight } = computeRowYLayout([32, 32, 32], 32, 11)
    expect(yFor(0)).toBe(11) // 0 + 11
    expect(yFor(1)).toBe(43) // 32 + 11
    expect(yFor(2)).toBe(75) // 64 + 11
    // totalHeight is unaffected by nodeOffset (still rowSpacing/2 bottom pad).
    expect(totalHeight).toBe(32 * 3 + 32 / 2)
  })

  it('defaults nodeOffset to rowSpacing/2 when omitted', () => {
    const { yFor } = computeRowYLayout([32, 32], 32)
    expect(yFor(0)).toBe(16) // 0 + 32/2
    expect(yFor(1)).toBe(48) // 32 + 32/2
  })

  it('satisfies the RowYLayout interface shape', () => {
    const layout: RowYLayout = computeRowYLayout([28, 28], 28)
    expect(typeof layout.yFor).toBe('function')
    expect(typeof layout.totalHeight).toBe('number')
  })
})
