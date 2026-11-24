import { useMemo } from 'react'
import { computeDAGLayout, ROW_HEIGHT_PX } from '@/lib/dagLayout'
import type { PlanItem } from '@/types/models'
import { LANE_WIDTH, buildEdgePath } from './dagEdgePath'

const BASE_ROW_HEIGHT = ROW_HEIGHT_PX
const PADDING = 6
const NODE_RADIUS = 3
const STROKE_COLOR = 'var(--color-muted-foreground)'
const STROKE_WIDTH = 1

interface DAGGraphProps {
  items: PlanItem[]
}

export function DAGGraph({ items }: DAGGraphProps) {
  const layout = useMemo(() => computeDAGLayout(items), [items])
  const rowOffsets = useMemo(() => {
    const offsets: number[] = []
    for (let i = 0; i < items.length; i++) {
      offsets.push(i * BASE_ROW_HEIGHT + BASE_ROW_HEIGHT / 2)
    }
    return offsets
  }, [items.length])

  if (layout.maxLane === -1) return null

  const width = (layout.maxLane + 1) * LANE_WIDTH + PADDING * 2
  const height = items.length * BASE_ROW_HEIGHT
  const laneX = (lane: number) => lane * LANE_WIDTH + PADDING + LANE_WIDTH / 2
  const rowY = (row: number) => rowOffsets[row] ?? 0

  return (
    <svg width={width} height={height} className="flex-shrink-0">
      {layout.connectors.map((c, i) => {
        const connKey = `conn-${c.type}-${c.fromLane}-${c.fromRow}-${c.toLane}-${c.toRow}-${i}`
        return (
          <path
            key={connKey}
            d={buildEdgePath(c, laneX, rowY)}
            stroke={STROKE_COLOR}
            strokeWidth={STROKE_WIDTH}
            strokeLinecap="round"
            strokeLinejoin="round"
            fill="none"
          />
        )
      })}
      {layout.nodes.map((node, i) => (
        <circle key={`node-${node.lane}-${i}`} cx={laneX(node.lane)} cy={rowY(i)} r={NODE_RADIUS} fill={STROKE_COLOR} stroke="none" />
      ))}
    </svg>
  )
}
