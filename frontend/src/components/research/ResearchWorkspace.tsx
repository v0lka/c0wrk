import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  FlaskConical,
  FileText,
  BookMarked,
  FileCheck2,
  Loader2,
  AlertCircle,
  Save,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'
import { useResearchStatusEvents } from '@/hooks/useResearchStatusEvents'
import { useResearchFileWatcher } from '@/hooks/useResearchFileWatcher'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
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
  projectFilePaths,
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

interface HypothesisCardProps {
  node: HypothesisNode
  draft: HypothesisDraft
  saving: boolean
  dirty: boolean
  saveError: string | null
  onChange: (next: HypothesisDraft) => void
  onSave: () => void
}

function HypothesisCard({
  node,
  draft,
  saving,
  dirty,
  saveError,
  onChange,
  onSave,
}: HypothesisCardProps) {
  const parents = (node.parents ?? []).join(', ')

  return (
    <div className="flex flex-col gap-3" data-testid="hypothesis-card">
      <div>
        <div className="flex items-baseline gap-1.5">
          <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
            {node.id}
          </span>
          <span className="truncate text-xs font-semibold">{node.title}</span>
        </div>
        {parents && (
          <p className="mt-0.5 text-[11px] text-muted-foreground/70">
            parents: <span className="font-mono text-[10px]">{parents}</span>
          </p>
        )}
      </div>

      <label className="flex flex-col gap-1">
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

      <label className="flex flex-col gap-1">
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

      <label className="flex flex-col gap-1">
        <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
          Result
        </span>
        <textarea
          value={draft.result}
          onChange={(e) => onChange({ ...draft, result: e.target.value })}
          aria-label="Hypothesis result"
          rows={4}
          placeholder="Finding / outcome…"
          className="w-full resize-y rounded-md border border-input bg-background px-2 py-1 text-xs outline-none focus:border-primary"
        />
      </label>

      {saveError && (
        <p className="text-xs text-destructive" role="alert">
          {saveError}
        </p>
      )}

      <button
        type="button"
        onClick={onSave}
        disabled={saving || !dirty}
        className="inline-flex items-center justify-center gap-1.5 rounded-md bg-primary px-2 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-50"
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
 * a "hide completed" toggle), an inline editable hypothesis card (status /
 * result / timebox persisted through the t4 UpdateHypothesis RPC), and quick
 * links that open the brief / prior-art / report as sibling read-only tabs.
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

  // Open a research artifact (brief/prior-art/report) as a sibling read-only
  // tab in the file viewer. Declared before the early return so the hook order
  // stays stable across renders.
  const openReadView = useCallback((filePath: string) => {
    const store = useFileViewerStore.getState()
    store.setCollapsed(false)
    store.openFile(filePath)
  }, [])

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

  const dir = project ? projectDir(root, project.id) : ''
  const paths = projectFilePaths(rootPath, dir)

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* Header: title + hide-completed toggle */}
      <div className="flex items-center gap-2 shrink-0 border-b border-border bg-secondary/30 px-2 py-1">
        <FlaskConical className="size-3.5 shrink-0 text-success" />
        <span className="truncate text-xs font-medium">
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

      {/* Body: DAG + detail card */}
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

        <div className="w-72 shrink-0 overflow-auto border-l border-border bg-background p-3">
          {selectedNode && draft ? (
            <HypothesisCard
              node={selectedNode}
              draft={draft}
              saving={saving}
              dirty={dirty}
              saveError={saveError}
              onChange={setDraft}
              onSave={handleSave}
            />
          ) : (
            <p className="py-8 text-center text-xs text-muted-foreground">
              Select a hypothesis to edit
            </p>
          )}
        </div>
      </div>

      {/* Read views: open brief / prior-art / report as sibling tabs */}
      <div className="flex shrink-0 flex-wrap items-center gap-1 border-t border-border bg-secondary/20 px-2 py-1.5">
        <ReadViewLink
          icon={FileText}
          label="Brief"
          onClick={() => openReadView(paths.brief)}
        />
        <ReadViewLink
          icon={BookMarked}
          label="Prior art"
          onClick={() => openReadView(paths.priorArt)}
        />
        <ReadViewLink
          icon={FileCheck2}
          label="Report"
          disabled={!project?.has_report}
          onClick={
            project?.has_report ? () => openReadView(paths.report) : undefined
          }
        />
      </div>
    </div>
  )
}

interface ReadViewLinkProps {
  icon: typeof FileText
  label: string
  disabled?: boolean
  onClick?: () => void
}

function ReadViewLink({ icon: Icon, label, disabled, onClick }: ReadViewLinkProps) {
  const interactive = !!onClick && !disabled
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!interactive}
      title={interactive ? `Open ${label}` : label}
      className={cn(
        'inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] transition-colors',
        interactive && 'cursor-pointer hover:bg-muted',
        !interactive && 'cursor-default opacity-60',
        'text-muted-foreground',
      )}
    >
      <Icon className="size-3" />
      <span className="uppercase tracking-wide">{label}</span>
    </button>
  )
}
