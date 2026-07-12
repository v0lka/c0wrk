import { useMemo } from 'react'
import { type GraphNode, type RowYLayout } from '@/lib/gitGraphLayout'
import { LANE_SPACING, MERGE_R, NODE_R, edgePath, laneVar, xFor } from './gitGraphRender'

interface GitGraphGutterProps {
  /** Lane-laid-out commit nodes (newest-first), 1:1 with the commit rows. */
  nodes: GraphNode[]
  /** Variable-height vertical layout (node y + total SVG height). */
  rowY: RowYLayout
}

/**
 * SVG lane gutter for the unified history+graph view. Draws branch/merge
 * edges and commit nodes positioned by `rowY` so they stay aligned with the
 * HTML commit rows beside them — including when a row expands inline and
 * pushes later nodes down.
 */
export function GitGraphGutter({ nodes, rowY }: GitGraphGutterProps) {
  const { svgWidth, shaToNode } = useMemo(() => {
    const maxLane = nodes.reduce((m, n) => Math.max(m, n.lane), 0)
    const map = new Map<string, GraphNode>()
    for (const n of nodes) map.set(n.sha, n)
    return { svgWidth: xFor(maxLane) + LANE_SPACING, shaToNode: map }
  }, [nodes])

  return (
    <svg width={svgWidth} height={rowY.totalHeight} className="shrink-0" aria-hidden="true">
      {nodes.map((node) =>
        node.parents.map((edge) => {
          const parent = shaToNode.get(edge.sha)
          // Edges to parents outside the visible window anchor at the bottom.
          const targetRow = parent ? parent.row : nodes.length
          return (
            <path
              key={`${node.sha}-${edge.sha}`}
              d={edgePath(
                xFor(node.lane),
                rowY.yFor(node.row),
                xFor(edge.lane),
                rowY.yFor(targetRow),
              )}
              fill="none"
              strokeWidth={1.5}
              style={{ stroke: `var(${laneVar(edge.lane)})` }}
            />
          )
        }),
      )}
      {nodes.map((node) => (
        <circle
          key={node.sha}
          cx={xFor(node.lane)}
          cy={rowY.yFor(node.row)}
          r={node.isMerge ? MERGE_R : NODE_R}
          style={{
            fill: `var(${laneVar(node.lane)})`,
            stroke: node.isMerge ? 'var(--color-background)' : 'none',
            strokeWidth: node.isMerge ? 1.5 : 0,
          }}
        />
      ))}
    </svg>
  )
}
