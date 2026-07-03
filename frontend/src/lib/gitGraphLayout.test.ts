// Unit tests for gitGraphLayout — pure commit-graph lane layout (Phase 6)

import { describe, it, expect } from 'vitest'
import { computeGraphLayout, shortSha } from './gitGraphLayout'
import type { GraphCommit } from '@/types/models'

/** Helper: build a commit with minimal boilerplate. */
function commit(sha: string, parents: string[], message = sha, refs: string[] = []): GraphCommit {
  return { sha, parents, message, refs }
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
