import { describe, it, expect } from 'vitest'
import { computeDAGLayout, type DAGItem } from './dagLayout'

describe('computeDAGLayout', () => {
  it('returns empty layout for empty input', () => {
    const result = computeDAGLayout([])
    expect(result).toEqual({ nodes: [], connectors: [], maxLane: -1 })
  })

  it('places a single node at lane 0', () => {
    const result = computeDAGLayout([{ id: 'A', dependsOn: [] }])
    expect(result.nodes).toEqual([{ id: 'A', lane: 0 }])
    expect(result.connectors).toEqual([])
    expect(result.maxLane).toBe(0)
  })

  it('places a linear chain in lane 0 with vertical connectors', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['B'] },
    ]
    const result = computeDAGLayout(items)
    expect(result.nodes).toEqual([
      { id: 'A', lane: 0 },
      { id: 'B', lane: 0 },
      { id: 'C', lane: 0 },
    ])
    expect(result.maxLane).toBe(0)
    // Should have vertical connectors A->B and B->C
    const verticals = result.connectors.filter(c => c.type === 'vertical')
    expect(verticals.length).toBeGreaterThanOrEqual(2)
    expect(verticals).toContainEqual({ fromLane: 0, toLane: 0, fromRow: 0, toRow: 1, type: 'vertical' })
    expect(verticals).toContainEqual({ fromLane: 0, toLane: 0, fromRow: 1, toRow: 2, type: 'vertical' })
  })

  it('forks into separate lanes', () => {
    // A -> B and A -> C
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
    ]
    const result = computeDAGLayout(items)
    // B should inherit A's lane 0, C should get lane 1
    expect(result.nodes[0]).toEqual({ id: 'A', lane: 0 })
    expect(result.nodes[1]).toEqual({ id: 'B', lane: 0 })
    expect(result.nodes[2]).toEqual({ id: 'C', lane: 1 })
    expect(result.maxLane).toBe(1)
    // Should have a fork connector from A to C
    const forks = result.connectors.filter(c => c.type === 'fork')
    expect(forks.length).toBeGreaterThanOrEqual(1)
  })

  it('merges from multiple parents into leftmost lane', () => {
    // A and B (independent roots) -> C depends on both
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: [] },
      { id: 'C', dependsOn: ['A', 'B'] },
    ]
    const result = computeDAGLayout(items)
    expect(result.nodes[0]).toEqual({ id: 'A', lane: 0 })
    expect(result.nodes[1]).toEqual({ id: 'B', lane: 1 })
    // C merges — should inherit leftmost parent lane (0)
    expect(result.nodes[2]).toEqual({ id: 'C', lane: 0 })
    // Should have a merge connector
    const merges = result.connectors.filter(c => c.type === 'merge')
    expect(merges.length).toBeGreaterThanOrEqual(1)
  })

  it('handles diamond pattern (fork then merge)', () => {
    // A -> B, A -> C, B+C -> D
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'D', dependsOn: ['B', 'C'] },
    ]
    const result = computeDAGLayout(items)
    expect(result.nodes).toHaveLength(4)
    // A at lane 0, B inherits 0, C gets 1, D merges to leftmost (0)
    expect(result.nodes[0]).toEqual({ id: 'A', lane: 0 })
    expect(result.nodes[1]).toEqual({ id: 'B', lane: 0 })
    expect(result.nodes[2]).toEqual({ id: 'C', lane: 1 })
    expect(result.nodes[3]).toEqual({ id: 'D', lane: 0 })
  })

  it('reuses freed lanes', () => {
    // A -> B (B has no children), A -> C -> D
    // After B is processed with no children, its lane should be freed for reuse
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'D', dependsOn: ['C'] },
    ]
    const result = computeDAGLayout(items)
    expect(result.nodes).toHaveLength(4)
    // A at 0, B inherits 0 (first child), C gets 1
    // D inherits C's lane 1
    expect(result.nodes[0]).toEqual({ id: 'A', lane: 0 })
    expect(result.nodes[1]).toEqual({ id: 'B', lane: 0 })
    expect(result.nodes[2]).toEqual({ id: 'C', lane: 1 })
    expect(result.nodes[3]).toEqual({ id: 'D', lane: 1 })
  })

  it('handles two independent roots', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: [] },
    ]
    const result = computeDAGLayout(items)
    expect(result.nodes).toEqual([
      { id: 'A', lane: 0 },
      { id: 'B', lane: 1 },
    ])
    expect(result.maxLane).toBe(1)
  })
})
