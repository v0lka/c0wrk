import { useMemo } from 'react'
import { FlaskConical } from 'lucide-react'
import {
  useResearchStore,
  selectActiveProject,
  RESEARCH_SIDEBAR_MIN_WIDTH,
  RESEARCH_SIDEBAR_MAX_WIDTH,
} from '@/stores/researchStore'
import { useResize } from '@/hooks/useResize'
import { ResizeHandle } from '@/components/ResizeHandle'
import { ResearchToggle } from './ResearchToggle'
import { HypothesisCard } from './HypothesisCard'
import { ErrorBanner } from './ResearchBanner'
import { useHypothesisEditor } from './useHypothesisEditor'
import { layoutDag, buildDisplayGraph } from './researchDagRender'
import { ResearchDagCanvas } from './ResearchDagCanvas'
import type { HypothesisGraph } from '@/types/models'

// Detail-sidebar width bounds live in the research store (RESEARCH_SIDEBAR_*)
// alongside the persisted width: the split must survive workspace remounts.

// ── ResearchWorkspace (the file-viewer tab content) ────────────────────

/**
 * Research workspace rendered by the file viewer for the synthetic
 * `c0wrk:research` pseudo-path. Shows the incomplete-path hypothesis DAG (with
 * a "hide completed" toggle) beside a resizable detail sidebar: every
 * hypothesis mention (the card header and parent ids) opens the corresponding
 * markdown card as a sibling read-only tab, and the editable card (status /
 * result / timebox persisted through the t4 UpdateHypothesis RPC) fills the
 * sidebar height with a markdown-highlighted result editor.
 */
export function ResearchWorkspace() {
  // Data sync (full status + incremental graph updates) lives in the App-root
  // ResearchEventBridge — this component is a pure view over researchStore.
  const enabled = useResearchStore((s) => s.status?.enabled ?? false)
  const project = useResearchStore(selectActiveProject)
  const error = useResearchStore((s) => s.error)
  const isLoading = useResearchStore((s) => s.isLoading)

  // Workspace view state (selection, draft, filter, sidebar width) lives in
  // the research store, not local state: the floating file viewer
  // auto-collapses on outside focus (and sibling-tab switches unmount the
  // workspace too), so local state would silently drop the selected vertex,
  // the open card, and any unsaved draft on every remount.
  const selectedId = useResearchStore((s) => s.selectedHypothesisId)
  const selectedProjectId = useResearchStore((s) => s.selectedHypothesisProjectId)
  const draft = useResearchStore((s) => s.hypothesisDraft)
  const setHypothesisDraft = useResearchStore((s) => s.setHypothesisDraft)
  const hideTerminal = useResearchStore((s) => s.hideTerminal)
  const setHideTerminal = useResearchStore((s) => s.setHideTerminal)
  const sidebarWidth = useResearchStore((s) => s.sidebarWidth)
  const setSidebarWidth = useResearchStore((s) => s.setSidebarWidth)

  // Full graph drives selection + the editable card; the display graph is a
  // filtered projection for layout/rendering only. Memoised so an absent
  // project yields a stable empty graph instead of a fresh object each render.
  const projectGraph = project?.graph
  const graph = useMemo<HypothesisGraph>(
    () => projectGraph ?? { nodes: [], edges: [] },
    [projectGraph],
  )

  const displayGraph = useMemo(
    () => buildDisplayGraph(graph, { hideTerminal }),
    [graph, hideTerminal],
  )
  const layout = useMemo(() => layoutDag(displayGraph), [displayGraph])

  // The selection is keyed to the research project it was made in: node ids
  // (H-001…) are generic and collide across R-NNN projects, so a selection
  // left over from an earlier active project must not rebind to the current
  // project's same-id node — an unsaved draft plus Save would then overwrite
  // ANOTHER project's card. Resolve (and highlight) it only while the key
  // matches the active project.
  const selectionIsCurrent = project !== null && selectedProjectId === project.id
  const selectedNode = useMemo(
    () =>
      selectionIsCurrent
        ? graph.nodes.find((n) => n.id === selectedId) ?? null
        : null,
    [graph, selectedId, selectionIsCurrent],
  )

  // Card editing (selection clicks, dirty, save round-trip, open-card link).
  // Called before the early return below so the hook order stays stable.
  const { saving, saveError, selectNode, dirty, handleSave, openHypothesisCard } =
    useHypothesisEditor(graph, selectedNode, draft)

  // Sidebar ↔ DAG split: dragging (or arrow-keying) the border between the
  // canvas and the sidebar resizes them. Right-side panel → drag left grows
  // it (direction −1), mirroring the docked file viewer's handle.
  const sidebarResize = useResize({
    initialWidth: sidebarWidth,
    min: RESEARCH_SIDEBAR_MIN_WIDTH,
    max: RESEARCH_SIDEBAR_MAX_WIDTH,
    direction: -1,
    onChange: setSidebarWidth,
  })

  // ── RESEARCH off → enable empty state ──────────────────────────────
  if (!enabled) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        {error && <ErrorBanner message={error} />}
        <div className="flex flex-1 items-center justify-center min-h-0">
          <ResearchToggle variant="card" />
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* Header: title + hide-completed toggle */}
      <div className="flex items-center gap-2 shrink-0 border-b border-border bg-secondary/30 px-2 py-1">
        <FlaskConical className="size-3.5 shrink-0 text-success" />
        <span
          className="truncate text-xs font-medium"
          title={project?.brief.title ?? 'Research'}
        >
          {project?.brief.title ?? 'Research'}
        </span>
        <label className="ml-auto flex shrink-0 cursor-pointer items-center gap-1.5 text-[11px] text-muted-foreground">
          <input
            type="checkbox"
            checked={hideTerminal}
            onChange={(e) => setHideTerminal(e.target.checked)}
            aria-label="Hide completed hypotheses"
            className="size-3.5"
          />
          Hide completed
        </label>
      </div>

      {error && <ErrorBanner message={error} />}

      {/* Body: DAG + resizable detail sidebar */}
      <div className="flex flex-1 min-h-0">
        <div className="relative flex-1 min-w-0">
          {isLoading && graph.nodes.length === 0 ? (
            <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
              Loading…
            </div>
          ) : displayGraph.nodes.length === 0 ? (
            <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
              No active hypotheses
            </div>
          ) : (
            <ResearchDagCanvas
              layout={layout}
              selectedId={selectionIsCurrent ? selectedId : null}
              onSelect={selectNode}
            />
          )}
        </div>

        <ResizeHandle
          onMouseDown={sidebarResize.handleMouseDown}
          onKeyDown={sidebarResize.handleKeyDown}
        />

        <div
          data-testid="hypothesis-sidebar"
          style={{ width: sidebarWidth }}
          className="flex shrink-0 flex-col overflow-auto border-l border-border bg-background p-3"
        >
          {selectedNode && draft ? (
            // key on the hypothesis id: switching cards REMOUNTS the card
            // subtree, so nothing captured at mount time (the CodeMirror
            // view — its doc, cursor, history — or any mount-time closure)
            // can leak from one hypothesis's editor into the next one's.
            // Without it React reconciles both cards as the same instance
            // and reuses the previous card's editor.
            <HypothesisCard
              key={selectedNode.id}
              node={selectedNode}
              draft={draft}
              saving={saving}
              dirty={dirty}
              saveError={saveError}
              onChange={setHypothesisDraft}
              onSave={handleSave}
              onOpenCard={openHypothesisCard}
            />
          ) : (
            <p className="py-8 text-center text-xs text-muted-foreground">
              Select a hypothesis to edit
            </p>
          )}
        </div>
      </div>
    </div>
  )
}
