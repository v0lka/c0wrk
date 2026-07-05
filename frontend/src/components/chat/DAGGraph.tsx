import { useMemo } from 'react'
import { computeDAGLayout } from '@/lib/dagLayout'
import type { PlanItem } from '@/types/models'

const LANE_WIDTH = 6
const BASE_ROW_HEIGHT = 24
const PADDING = 4
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
        const x1 = laneX(c.fromLane), y1 = rowY(c.fromRow)
        const x2 = laneX(c.toLane), y2 = rowY(c.toRow)
        const connKey = `conn-${c.type}-${c.fromLane}-${c.fromRow}-${c.toLane}-${c.toRow}-${i}`

        if (c.type === 'vertical') {
          return <line key={connKey} x1={x1} y1={y1} x2={x2} y2={y2} stroke={STROKE_COLOR} strokeWidth={STROKE_WIDTH} strokeLinecap="round" fill="none" />
        }
        if (c.type === 'fork' || c.type === 'merge') {
          const r = LANE_WIDTH / 2
          const dx = Math.sign(x2 - x1)
          const d = c.type === 'fork'
            ? `M ${x1} ${y1} L ${x2 - dx * r} ${y1} Q ${x2} ${y1} ${x2} ${y1 + r} L ${x2} ${y2}`
            : `M ${x1} ${y1} L ${x1} ${y2 - r} Q ${x1} ${y2} ${x1 + dx * r} ${y2} L ${x2} ${y2}`
          return <path key={connKey} d={d} stroke={STROKE_COLOR} strokeWidth={STROKE_WIDTH} strokeLinecap="round" strokeLinejoin="round" fill="none" />
        }
        return null
      })}
      {layout.nodes.map((node, i) => (
        <circle key={`node-${node.lane}-${i}`} cx={laneX(node.lane)} cy={rowY(i)} r={2} fill={STROKE_COLOR} stroke="none" />
      ))}
    </svg>
  )
}
