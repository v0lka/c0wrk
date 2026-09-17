// LiteratureGraph — renders a paper's literature neighbourhood (seed +
// predecessors + citing + contradictions) with the SHARED research DAG
// components. There is deliberately no bespoke canvas here: the graph model is
// projected into a hypothesis-shaped `HypothesisGraph` (see
// `@/lib/literatureGraph`) so geometry comes from `layoutDag` and the pan/zoom
// camera + node tooltips come from `ResearchDagCanvas` unchanged.
//
// Pure view: it owns no data loading and no selection — the parent passes the
// model and the selected id, mirroring how ResearchWorkspace drives
// ResearchDagCanvas.

import { useMemo } from 'react'
import { ResearchDagCanvas } from '@/components/research/ResearchDagCanvas'
import { layoutDag, statusColorVar } from '@/components/research/researchDagRender'
import {
  LITERATURE_KIND_ORDER,
  literatureKindLabel,
  literatureKindStatus,
  type LiteratureGraphModel,
} from '@/lib/literatureGraph'

export interface LiteratureGraphProps {
  model: LiteratureGraphModel
  selectedId: string | null
  onSelect: (id: string) => void
}

/** A small colour legend for the four work kinds, bottom-left over the canvas. */
function Legend() {
  return (
    <div
      data-testid="literature-legend"
      className="pointer-events-none absolute bottom-2 left-2 z-10 flex flex-col gap-0.5 rounded-md border border-border bg-background/85 px-1.5 py-1 shadow-sm backdrop-blur"
    >
      {LITERATURE_KIND_ORDER.map((kind) => (
        <span key={kind} className="flex items-center gap-1 text-[10px] text-muted-foreground">
          <span
            className="inline-block size-2 shrink-0 rounded-full"
            style={{ background: statusColorVar(literatureKindStatus(kind)) }}
          />
          {literatureKindLabel(kind)}
        </span>
      ))}
    </div>
  )
}

export function LiteratureGraph({ model, selectedId, onSelect }: LiteratureGraphProps) {
  // Geometry derives from the projected graph; the model is memoised by the
  // parent, so this only recomputes when the graph actually changes.
  const layout = useMemo(() => layoutDag(model.graph), [model])

  return (
    <div data-testid="literature-graph" className="relative h-full min-h-0">
      <ResearchDagCanvas
        layout={layout}
        nodes={model.graph.nodes}
        selectedId={selectedId}
        onSelect={onSelect}
      />
      <Legend />
    </div>
  )
}
