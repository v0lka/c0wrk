import { useCallback, useLayoutEffect, useMemo, type MouseEvent as ReactMouseEvent } from 'react'
import { Maximize, ZoomIn, ZoomOut } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DEFAULT_ZOOM_STEP, usePanZoom } from '@/lib/usePanZoom'
import {
  edgePathH,
  statusColorVar,
  NODE_R,
  LABEL_MAX_CHARS,
  layoutSignature,
  type DagLayout,
} from './researchDagRender'

/** Truncate a title for compact SVG labelling. */
function truncate(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max - 1)}…` : s
}

// ── DAG SVG (lightweight; geometry comes from layoutDag) ───────────────

interface DagSvgProps {
  layout: DagLayout
  selectedId: string | null
  onSelect: (id: string) => void
}

function DagSvg({ layout, selectedId, onSelect }: DagSvgProps) {
  // The layout box hugs the painted content — ids hanging left of nodes,
  // truncated titles right of them — so the camera's fit() centers the
  // actual graph, and left-hanging ids stay inside the SVG viewport (an SVG
  // clips its own overflow, so content outside the box is unreachable by
  // panning). Guards keep width/height positive for degenerate layouts.
  const w = Math.max(layout.width, 1)
  const h = Math.max(layout.height, 1)

  return (
    <svg
      width={w}
      height={h}
      viewBox={`${layout.minX} 0 ${w} ${h}`}
      role="graphics-document"
      aria-label="Research hypothesis DAG"
      className="shrink-0"
    >
      {layout.edges.map((e) => (
        <path
          key={`${e.from}->${e.to}`}
          d={edgePathH(e.x1, e.y1, e.x2, e.y2)}
          fill="none"
          stroke="var(--color-border)"
          strokeWidth="1.5"
        />
      ))}

      {layout.nodes.map((node) => {
        const selected = node.id === selectedId
        return (
          <g
            key={node.id}
            role="button"
            tabIndex={0}
            data-node-id={node.id}
            aria-label={`${node.id} ${node.title}`}
            className="cursor-pointer"
            onClick={() => onSelect(node.id)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                onSelect(node.id)
              }
            }}
          >
            {/* Native tooltip: the full (untruncated) title. */}
            <title>{node.title}</title>
            {selected && (
              <circle
                cx={node.x}
                cy={node.y}
                r={NODE_R + 3}
                fill="none"
                stroke="var(--color-highlight)"
                strokeWidth="1.5"
              />
            )}
            <circle
              cx={node.x}
              cy={node.y}
              r={NODE_R}
              fill={statusColorVar(node.status)}
              stroke="var(--color-background)"
              strokeWidth="1"
            />
            <text
              x={node.x - NODE_R - 4}
              y={node.y - 6}
              fontSize="9"
              textAnchor="end"
              fill="var(--color-muted-foreground)"
            >
              {node.id}
            </text>
            <text
              x={node.x + NODE_R + 4}
              y={node.y + 3.5}
              fontSize="11"
              textAnchor="start"
              fill="var(--color-foreground)"
            >
              {/* Same budget the layout's column pitch and box math derive
                  from (LABEL_MAX_CHARS) — kept in lockstep via one constant. */}
              {truncate(node.title, LABEL_MAX_CHARS)}
            </text>
          </g>
        )
      })}
    </svg>
  )
}

// ── ResearchDagCanvas (pan/zoom camera around the DAG SVG) ─────────────

export interface ResearchDagCanvasProps {
  layout: DagLayout
  selectedId: string | null
  onSelect: (id: string) => void
}

/**
 * Camera viewport around the hypothesis DAG SVG: replaces the former native
 * `overflow-auto` scroll with drag-to-pan, cursor-anchored wheel zoom, and a
 * floating zoom toolbar (− / percentage / + / fit). All pan/zoom behavior
 * (anchored zoom, fit, drag, wheel, click-suppression counter) lives in the
 * reusable `usePanZoom` hook; on first paint — and whenever the painted
 * geometry actually changes — the DAG is scaled to fit the canvas width
 * (never upscaled).
 */
export function ResearchDagCanvas({ layout, selectedId, onSelect }: ResearchDagCanvasProps) {
  const {
    view,
    canvasRef,
    contentRef,
    fit,
    zoomFromCenter,
    didDragRef,
    isPanningRef,
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel,
  } = usePanZoom()

  // Fit on first paint and whenever the PAINTED GEOMETRY changes (graph
  // updates or the hide-completed toggle resize the SVG). The store replaces
  // `project.graph` with a fresh object on every applied update, so keying on
  // layout identity would re-fit even content-identical refreshes — snapping
  // the viewport back to fit mid-exploration. The cheap geometric signature
  // only changes when nodes/edges actually move. A refresh landing while the
  // user is mid-drag is skipped entirely; the next real geometry change
  // re-fits. `fit` is referentially stable, so a mere re-render (e.g.
  // selection change) never resets the camera.
  const geometrySig = useMemo(() => layoutSignature(layout), [layout])
  useLayoutEffect(() => {
    if (isPanningRef.current) return
    fit()
  }, [geometrySig, isPanningRef, fit])

  // Swallow the click that trails a pan gesture: once the drag threshold is
  // crossed the canvas holds pointer capture, and under capture the browser
  // retargets the trailing `click` to the canvas itself (per the Pointer
  // Events spec, compatibility mouse events follow the capture target).
  // Stopping it in the capture phase — before it can reach any node — keeps
  // panning from changing the selection; plain clicks (didDragRef false,
  // never captured) pass through to the node under the cursor.
  const onCanvasClickCapture = useCallback(
    (e: ReactMouseEvent<HTMLDivElement>) => {
      if (!didDragRef.current) return
      e.stopPropagation()
      e.preventDefault()
      didDragRef.current = false
    },
    [didDragRef],
  )

  const pct = `${Math.round(view.scale * 100)}%`

  return (
    <div className="relative h-full w-full">
      <div className="absolute right-2 top-2 z-10 flex items-center gap-0.5 rounded-md border border-border bg-background/85 p-0.5 shadow-sm backdrop-blur">
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => zoomFromCenter(1 / DEFAULT_ZOOM_STEP)}
          title="Zoom out"
          aria-label="Zoom out"
        >
          <ZoomOut />
        </Button>
        <span
          className="min-w-[4ch] select-none text-center text-xs tabular-nums text-muted-foreground"
          aria-live="polite"
        >
          {pct}
        </span>
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => zoomFromCenter(DEFAULT_ZOOM_STEP)}
          title="Zoom in"
          aria-label="Zoom in"
        >
          <ZoomIn />
        </Button>
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={fit}
          title="Fit to view"
          aria-label="Fit DAG to view"
        >
          <Maximize />
        </Button>
      </div>
      <div
        ref={canvasRef}
        className="relative h-full w-full cursor-grab touch-none select-none overflow-hidden active:cursor-grabbing"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerCancel}
        onClickCapture={onCanvasClickCapture}
      >
        <div
          ref={contentRef}
          className="absolute left-0 top-0 origin-top-left"
          style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}
        >
          <DagSvg layout={layout} selectedId={selectedId} onSelect={onSelect} />
        </div>
      </div>
    </div>
  )
}
