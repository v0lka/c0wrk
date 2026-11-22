// Unit tests for the DAG edge path builder. Golden strings encode the
// renderer geometry: laneX(l) = l*14 + 6 + 7, rowY(r) = r*24 + 12, corner
// radius 14/2 = 7, broken gap 6 (±3 around the segment midpoint). Changing
// the module constants legitimately changes them — that update is the
// review point.

import { describe, it, expect } from 'vitest'

import { buildEdgePath } from './dagEdgePath'
import type { DAGConnector } from '@/lib/dagLayout'

const laneX = (lane: number) => lane * 14 + 6 + 14 / 2
const rowY = (row: number) => row * 24 + 12

describe('buildEdgePath', () => {
  it('vertical: straight line down the lane', () => {
    const c: DAGConnector = { fromLane: 1, toLane: 1, fromRow: 0, toRow: 3, type: 'vertical' }
    expect(buildEdgePath(c, laneX, rowY)).toBe('M 27 12 L 27 84')
  })

  it('broken vertical: two stubs with a 6px gap at the midpoint', () => {
    const c: DAGConnector = { fromLane: 1, toLane: 1, fromRow: 0, toRow: 3, type: 'vertical', broken: true }
    const d = buildEdgePath(c, laneX, rowY)
    // y1=12, y2=84, midpoint 48, stubs end at 45 and resume at 51 (gap 6)
    expect(d).toBe('M 27 12 L 27 45 M 27 51 L 27 84')
    // structural: exactly two subpaths; the gap sits between them; the outer
    // endpoints are the segment endpoints
    const subpaths = d.split('M').filter(s => s.trim() !== '')
    expect(subpaths).toHaveLength(2)
    const endOf = (s: string) => Number(s.trim().split(' ').pop())
    expect(endOf(subpaths[0]!)).toBe(45)
    expect(Number(subpaths[1]!.trim().split(' ')[1])).toBe(51)
    expect(endOf(subpaths[1]!)).toBe(84)
    expect(d).toContain(`M 27 ${rowY(0)}`)
    expect(d.endsWith(`L 27 ${rowY(3)}`)).toBe(true)
  })

  it('fork right, exitDy 0: legacy shape (run straight out of the node)', () => {
    const c: DAGConnector = { fromLane: 0, toLane: 1, fromRow: 0, toRow: 2, type: 'fork' }
    const d = buildEdgePath(c, laneX, rowY)
    const legacy = `M ${laneX(0)} ${rowY(0)} L ${laneX(1) - 7} ${rowY(0)} Q ${laneX(1)} ${rowY(0)} ${laneX(1)} ${rowY(0) + 7} L ${laneX(1)} ${rowY(2)}`
    expect(d).toBe(legacy)
    expect(d).toBe('M 13 12 L 20 12 Q 27 12 27 19 L 27 60')
  })

  it('fork right, exitDy 3: drop stub onto the lowered run', () => {
    const c: DAGConnector = { fromLane: 0, toLane: 1, fromRow: 0, toRow: 2, type: 'fork', exitDy: 3 }
    expect(buildEdgePath(c, laneX, rowY)).toBe('M 13 12 L 13 15 L 20 15 Q 27 15 27 22 L 27 60')
  })

  it('fork right, exitDy 9 (max fan stack): run 9px below the parent row', () => {
    const c: DAGConnector = { fromLane: 0, toLane: 1, fromRow: 0, toRow: 2, type: 'fork', exitDy: 9 }
    expect(buildEdgePath(c, laneX, rowY)).toBe('M 13 12 L 13 21 L 20 21 Q 27 21 27 28 L 27 60')
  })

  it('fork: missing exitDy behaves as 0', () => {
    const c: DAGConnector = { fromLane: 0, toLane: 1, fromRow: 0, toRow: 2, type: 'fork' }
    expect(buildEdgePath(c, laneX, rowY)).toBe(buildEdgePath({ ...c, exitDy: 0 }, laneX, rowY))
  })

  it('fork left (toLane < fromLane): corner mirrors, run exits leftward', () => {
    const c: DAGConnector = { fromLane: 1, toLane: 0, fromRow: 0, toRow: 2, type: 'fork' }
    expect(buildEdgePath(c, laneX, rowY)).toBe('M 27 12 L 20 12 Q 13 12 13 19 L 13 60')
  })

  it('fork left, exitDy 6: drop stub + mirrored corner on the lowered run', () => {
    const c: DAGConnector = { fromLane: 1, toLane: 0, fromRow: 0, toRow: 2, type: 'fork', exitDy: 6 }
    expect(buildEdgePath(c, laneX, rowY)).toBe('M 27 12 L 27 18 L 20 18 Q 13 18 13 25 L 13 60')
  })

  it('merge left, entryDy 0: legacy shape (run lands on the child row)', () => {
    const c: DAGConnector = { fromLane: 1, toLane: 0, fromRow: 0, toRow: 2, type: 'merge' }
    const d = buildEdgePath(c, laneX, rowY)
    const legacy = `M ${laneX(1)} ${rowY(0)} L ${laneX(1)} ${rowY(2) - 7} Q ${laneX(1)} ${rowY(2)} ${laneX(1) - 7} ${rowY(2)} L ${laneX(0)} ${rowY(2)}`
    expect(d).toBe(legacy)
    expect(d).toBe('M 27 12 L 27 53 Q 27 60 20 60 L 13 60')
  })

  it('merge left, entryDy 3: run below the child row, tail rises back to the node', () => {
    const c: DAGConnector = { fromLane: 1, toLane: 0, fromRow: 0, toRow: 2, type: 'merge', entryDy: 3 }
    expect(buildEdgePath(c, laneX, rowY)).toBe('M 27 12 L 27 56 Q 27 63 20 63 L 13 63 L 13 60')
  })

  it('merge right, entryDy 0: corner curves onto the run toward the child', () => {
    const c: DAGConnector = { fromLane: 0, toLane: 1, fromRow: 0, toRow: 2, type: 'merge' }
    expect(buildEdgePath(c, laneX, rowY)).toBe('M 13 12 L 13 53 Q 13 60 20 60 L 27 60')
  })

  it('merge right, entryDy 6: run 6px below the child row', () => {
    const c: DAGConnector = { fromLane: 0, toLane: 1, fromRow: 0, toRow: 2, type: 'merge', entryDy: 6 }
    expect(buildEdgePath(c, laneX, rowY)).toBe('M 13 12 L 13 59 Q 13 66 20 66 L 27 66 L 27 60')
  })

  it('merge: missing entryDy behaves as 0', () => {
    const c: DAGConnector = { fromLane: 1, toLane: 0, fromRow: 0, toRow: 2, type: 'merge' }
    expect(buildEdgePath(c, laneX, rowY)).toBe(buildEdgePath({ ...c, entryDy: 0 }, laneX, rowY))
  })
})
