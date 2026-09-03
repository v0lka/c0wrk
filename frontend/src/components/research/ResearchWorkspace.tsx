import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  FlaskConical,
  Loader2,
  AlertCircle,
  Save,
  ExternalLink,
} from 'lucide-react'
import { logger } from '@/lib/logger'
import { useResearchStatusEvents } from '@/hooks/useResearchStatusEvents'
import { useResearchFileWatcher } from '@/hooks/useResearchFileWatcher'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useResize } from '@/hooks/useResize'
import { ResizeHandle } from '@/components/ResizeHandle'
import { MiniCodeMirrorField } from '@/components/fileViewer/MiniCodeMirrorField'
import { updateHypothesis } from '@/api/research'
import { ResearchToggle } from './ResearchToggle'
import {
  draftFromNode,
  buildUpdateFields,
  type HypothesisDraft,
} from './researchWorkspaceUtils'
import {
  layoutDag,
  buildDisplayGraph,
  projectDir,
  hypothesisCardPath,
} from './researchDagRender'
import { ResearchDagCanvas } from './ResearchDagCanvas'
import type {
  HypothesisGraph,
  HypothesisNode,
  HypothesisStatus,
} from '@/types/models'

// ── Hypothesis detail card (editable status/result/timebox) ───────────

const STATUS_OPTIONS: HypothesisStatus[] = [
  'open',
  'in-progress',
  'confirmed',
  'refuted',
  'cancelled',
]

// Detail-sidebar width bounds (px). The default matches the former w-72
// (288px); the split is user-resizable via the drag handle / arrow keys.
const DEFAULT_SIDEBAR_WIDTH = 288
const SIDEBAR_MIN_WIDTH = 220
const SIDEBAR_MAX_WIDTH = 560

interface HypothesisCardProps {
  node: HypothesisNode
  draft: HypothesisDraft
  saving: boolean
  dirty: boolean
  saveError: string | null
  onChange: (next: HypothesisDraft) => void
  onSave: () => void
  /** Open a hypothesis's markdown card in the file viewer. */
  onOpenCard: (id: string) => void
}

function HypothesisCard({
  node,
  draft,
  saving,
  dirty,
  saveError,
  onChange,
  onSave,
  onOpenCard,
}: HypothesisCardProps) {
  const parentIds = node.parents ?? []

  return (
    <div
      className="flex h-full min-h-0 flex-col gap-3"
      data-testid="hypothesis-card"
    >
      <div className="shrink-0">
        {/* The card header (id + title) is itself a hypothesis mention: it
            opens this hypothesis's markdown card as a sibling viewer tab.
            The native tooltip carries the full, untruncated title. */}
        <button
          type="button"
          onClick={() => onOpenCard(node.id)}
          title={node.title}
          aria-label={`Open ${node.id} markdown card`}
          className="flex min-w-0 max-w-full items-baseline gap-1.5 rounded-sm text-left underline-offset-2 hover:underline"
        >
          <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
            {node.id}
          </span>
          <span className="truncate text-xs font-semibold">{node.title}</span>
          <ExternalLink className="size-3 shrink-0 self-center text-muted-foreground/60" />
        </button>
        {parentIds.length > 0 && (
          <p className="mt-0.5 flex flex-wrap items-baseline gap-1 text-[11px] text-muted-foreground/70">
            <span>parents:</span>
            {parentIds.map((p, i) => (
              <span key={p} className="flex items-baseline">
                {i > 0 && <span className="text-muted-foreground/50">,</span>}
                <button
                  type="button"
                  onClick={() => onOpenCard(p)}
                  title={`Open ${p} markdown card`}
                  className="rounded-sm font-mono text-[10px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
                >
                  {p}
                </button>
              </span>
            ))}
          </p>
        )}
      </div>

      <label className="flex shrink-0 flex-col gap-1">
        <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
          Status
        </span>
        <select
          value={draft.status}
          onChange={(e) => onChange({ ...draft, status: e.target.value })}
          aria-label="Hypothesis status"
          className="h-8 w-full rounded-md border border-input bg-background px-2 text-xs outline-none focus:border-primary"
        >
          {STATUS_OPTIONS.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </label>

      <label className="flex shrink-0 flex-col gap-1">
        <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
          Timebox
        </span>
        <input
          type="text"
          value={draft.timebox}
          onChange={(e) => onChange({ ...draft, timebox: e.target.value })}
          aria-label="Hypothesis timebox"
          placeholder="e.g. 2 weeks"
          className="h-8 w-full rounded-md border border-input bg-background px-2 text-xs outline-none focus:border-primary"
        />
      </label>

      {/* Result: fills every remaining vertical pixel of the sidebar. The
          markdown-aware CodeMirror field brings syntax highlighting and the
          project-wide custom scrollbar (cm-viewer-container). */}
      <label className="flex min-h-0 flex-1 flex-col gap-1" data-testid="hypothesis-result-field">
        <span className="shrink-0 text-[10px] uppercase tracking-wide text-muted-foreground">
          Result
        </span>
        <MiniCodeMirrorField
          value={draft.result}
          onChange={(result) => onChange({ ...draft, result })}
          placeholder="Finding / outcome…"
          lineWrapping
          className="min-h-0 max-h-none flex-1"
        />
      </label>

      {saveError && (
        <p className="shrink-0 text-xs text-destructive" role="alert">
          {saveError}
        </p>
      )}

      <button
        type="button"
        onClick={onSave}
        disabled={saving || !dirty}
        data-testid="hypothesis-save"
        className="inline-flex shrink-0 items-center justify-center gap-1.5 rounded-md bg-primary px-2 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-50"
      >
        {saving ? (
          <Loader2 className="size-3.5 animate-spin" />
        ) : (
          <Save className="size-3.5" />
        )}
        Save
      </button>
    </div>
  )
}

// ── Error banner ──────────────────────────────────────────────────────

function ErrorBanner({ message }: { message: string }) {
  return (
    <div className="flex items-center gap-1.5 shrink-0 px-3 py-1.5 text-xs text-destructive bg-destructive/10 border-b border-destructive/20">
      <AlertCircle className="size-3.5 shrink-0" />
      <span className="truncate">{message}</span>
    </div>
  )
}

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
  // Keep the store in sync (full status fetch + incremental graph updates).
  useResearchStatusEvents()
  useResearchFileWatcher()

  const enabled = useResearchStore((s) => s.status?.enabled ?? false)
  const project = useResearchStore(selectActiveProject)
  const root = useResearchStore((s) => s.status?.root)
  const rootPath = useResearchStore((s) => s.status?.research_root ?? '')
  const error = useResearchStore((s) => s.error)
  const isLoading = useResearchStore((s) => s.isLoading)

  const [hideTerminal, setHideTerminal] = useState(false)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [draft, setDraft] = useState<HypothesisDraft | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

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

  const selectedNode = useMemo(
    () => graph.nodes.find((n) => n.id === selectedId) ?? null,
    [graph, selectedId],
  )

  // Initialise the draft whenever the selection changes (reads the latest
  // graph via a ref so background file-change updates never clobber an
  // in-progress edit).
  const graphRef = useRef(graph)
  graphRef.current = graph
  useEffect(() => {
    setSaveError(null)
    if (!selectedId) {
      setDraft(null)
      return
    }
    const node = graphRef.current.nodes.find((n) => n.id === selectedId)
    setDraft(node ? draftFromNode(node) : null)
  }, [selectedId])

  const selectNode = useCallback((id: string) => {
    setSelectedId((prev) => (prev === id ? null : id))
  }, [])

  const dirty = useMemo(() => {
    if (!selectedNode || !draft) return false
    return Object.keys(buildUpdateFields(selectedNode, draft)).length > 0
  }, [selectedNode, draft])

  const handleSave = useCallback(async () => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId || !selectedNode || !draft) return
    const fields = buildUpdateFields(selectedNode, draft)
    if (Object.keys(fields).length === 0) return
    setSaving(true)
    setSaveError(null)
    try {
      const res = await updateHypothesis(projectId, selectedNode.id, fields)
      // Apply the refreshed graph so the DAG + metrics reflect the mutation.
      useResearchStore.getState().loadGraph(res)
      // Keep the selection; the store now carries the persisted values.
    } catch (err) {
      logger.error('Failed to update hypothesis:', err)
      setSaveError(
        err instanceof Error ? err.message : 'Failed to update hypothesis',
      )
    } finally {
      setSaving(false)
    }
  }, [selectedNode, draft])

  // Open a hypothesis's markdown card as a sibling read-only tab in the file
  // viewer. Declared before the early return so the hook order stays stable
  // across renders.
  const dir = project ? projectDir(root, project.id) : ''
  const openHypothesisCard = useCallback(
    (id: string) => {
      const store = useFileViewerStore.getState()
      store.setCollapsed(false)
      store.openFile(hypothesisCardPath(rootPath, dir, id))
    },
    [rootPath, dir],
  )

  // Sidebar ↔ DAG split: dragging (or arrow-keying) the border between the
  // canvas and the sidebar resizes them. Right-side panel → drag left grows
  // it (direction −1), mirroring the docked file viewer's handle.
  const [sidebarWidth, setSidebarWidth] = useState(DEFAULT_SIDEBAR_WIDTH)
  const sidebarResize = useResize({
    initialWidth: sidebarWidth,
    min: SIDEBAR_MIN_WIDTH,
    max: SIDEBAR_MAX_WIDTH,
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
              selectedId={selectedId}
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
            <HypothesisCard
              node={selectedNode}
              draft={draft}
              saving={saving}
              dirty={dirty}
              saveError={saveError}
              onChange={setDraft}
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
