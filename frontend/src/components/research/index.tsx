import { useCallback, type ReactNode } from 'react'
import { FlaskConical, FolderOpen, ChevronDown, AlertCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore, selectIsNoProject } from '@/stores/projectStore'
import { useUIStore, selectResearchSegment, type ResearchSegment } from '@/stores/uiStore'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { ResearchMetricsRow } from './ResearchMetrics'
import { ResearchNextStep } from './ResearchNextStep'
import { ResearchHypothesisPicker } from './ResearchHypothesisPicker'
import { ResearchQuickActions } from './ResearchQuickActions'
import { ResearchLog } from './ResearchLog'
import { ResearchProjectPicker } from './ResearchProjectPicker'
import { PriorArtRow } from './PriorArtRow'
import { DanglingPaperLinks } from './InformingPapers'
import { projectDir, projectFilePaths } from './researchDagRender'
import { PapersView } from '@/components/papers/PapersView'

/** The Research panel's two segments: the research control Dashboard vs the
 *  literature (Papers) library. The paper library lives independently of the
 *  hypothesis tracking, so the segment is always reachable. */
const RESEARCH_SEGMENTS: ReadonlyArray<{ value: ResearchSegment; label: string }> = [
  { value: 'dashboard', label: 'Dashboard' },
  { value: 'papers', label: 'Papers' },
]

/** Stable ids wiring each tab to its panel (ARIA tabs pattern). */
function segmentTabId(value: ResearchSegment): string {
  return `research-segment-tab-${value}`
}
function segmentPanelId(value: ResearchSegment): string {
  return `research-segment-panel-${value}`
}

/** Segmented control [Dashboard | Papers], persisted per project in uiStore.
 *  Implements the ARIA tabs pattern: `role="tab"` carries `aria-controls` to the
 *  `role="tabpanel"` rendered by {@link SegmentPanel}, and the tablist provides
 *  roving tabindex + Left/Right/Home/End navigation. */
function ResearchSegmentControl({
  active,
  onSelect,
}: {
  active: ResearchSegment
  onSelect: (segment: ResearchSegment) => void
}) {
  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const values = RESEARCH_SEGMENTS.map((segment) => segment.value)
    const currentIndex = values.indexOf(active)
    let nextIndex: number
    switch (e.key) {
      case 'ArrowRight':
        nextIndex = (currentIndex + 1) % values.length
        break
      case 'ArrowLeft':
        nextIndex = (currentIndex - 1 + values.length) % values.length
        break
      case 'Home':
        nextIndex = 0
        break
      case 'End':
        nextIndex = values.length - 1
        break
      default:
        return
    }
    e.preventDefault()
    const next = values[nextIndex]
    if (next === undefined) return
    onSelect(next)
    // Move focus with the roving tabindex so the newly selected tab receives it.
    e.currentTarget
      .querySelector<HTMLElement>(`[data-testid="research-segment-${next}"]`)
      ?.focus()
  }

  return (
    <div
      role="tablist"
      aria-label="Research view"
      aria-orientation="horizontal"
      data-testid="research-segment"
      onKeyDown={onKeyDown}
      className="flex shrink-0 items-center gap-0.5 border-b border-border bg-secondary/20 px-1.5 py-1"
    >
      {RESEARCH_SEGMENTS.map((segment) => {
        const selected = active === segment.value
        return (
          <button
            key={segment.value}
            type="button"
            role="tab"
            id={segmentTabId(segment.value)}
            aria-selected={selected}
            aria-controls={segmentPanelId(segment.value)}
            tabIndex={selected ? 0 : -1}
            data-testid={`research-segment-${segment.value}`}
            onClick={() => onSelect(segment.value)}
            className={cn(
              'flex-1 rounded px-2 py-0.5 text-[11px] transition-colors',
              selected
                ? 'bg-background text-foreground shadow-sm'
                : 'text-muted-foreground hover:bg-muted/50',
            )}
          >
            {segment.label}
          </button>
        )
      })}
    </div>
  )
}

/** The `role="tabpanel"` for one segment, associated with its tab through
 *  `aria-labelledby`/`aria-controls` (the ARIA tabs pattern). */
function SegmentPanel({
  segment,
  className,
  children,
}: {
  segment: ResearchSegment
  className?: string
  children: ReactNode
}) {
  return (
    <div
      role="tabpanel"
      id={segmentPanelId(segment)}
      aria-labelledby={segmentTabId(segment)}
      className={className}
    >
      {children}
    </div>
  )
}

/**
 * RESEARCH panel — a control dashboard, not a passive mirror.
 *
 * Modeled on the Git panel: a pure view over researchStore (the data sync —
 * research:changed / workspace:tree_changed full status and
 * research:file_changed incremental graph + log — is mounted once at the App
 * root via ResearchEventBridge), rendering a compact control
 * surface — a header with the research-project picker (active R-NNN switch,
 * pins, delete) and the research-init plus button, the status/metrics row,
 * the recommended next step with one-click execution, the current-hypothesis
 * picker (selection, status flip, Create hypothesis), the quick-actions row
 * that dispatches research-* skills, and the research log (t1).
 *
 * A segmented control switches between this Dashboard and the Papers view (the
 * literature library), persisted per project in uiStore.
 *
 * The hypothesis tree/DAG presentation lives in the Research workspace tab
 * (t5); the bottom bar is a single View Artifacts dropdown that opens the
 * brief / prior-art / graph / report artifacts that actually exist.
 */
export function ResearchPanel() {
  // Data sync (research:changed / workspace:tree_changed full status +
  // research:file_changed incremental graph) lives in the App-root
  // ResearchEventBridge — this panel is a pure view over researchStore.
  const project = useResearchStore(selectActiveProject)
  const rootPath = useResearchStore((s) => s.status?.research_root ?? '')
  const root = useResearchStore((s) => s.status?.root)
  const error = useResearchStore((s) => s.error)
  const isLoading = useResearchStore((s) => s.isLoading)

  // The segment is remembered per project; '' / null id falls back to the
  // Dashboard.
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const isNoProject = useProjectStore(selectIsNoProject)
  const segment = useUIStore((s) => selectResearchSegment(s, activeProjectId))
  const setResearchSegment = useUIStore((s) => s.setResearchSegment)
  const selectSegment = useCallback(
    (next: ResearchSegment) => {
      if (activeProjectId !== null) setResearchSegment(activeProjectId, next)
    },
    [activeProjectId, setResearchSegment],
  )

  // Open a research artifact file in the file viewer (path-agnostic ReadFile).
  const openArtifact = useCallback((filePath: string) => {
    const store = useFileViewerStore.getState()
    store.setCollapsed(false)
    store.openFile(filePath)
  }, [])

  // Open the Research workspace tab (the interactive hypothesis DAG) in the
  // file viewer instead of the raw graph.md.
  const openResearchTab = useCallback(() => {
    const store = useFileViewerStore.getState()
    store.setCollapsed(false)
    store.openResearch()
  }, [])

  const segmentControl = (
    <ResearchSegmentControl active={segment} onSelect={selectSegment} />
  )

  // ── No Project (CHAT mode) → neutral render ───────────────────────────
  // RESEARCH is always available for real projects, so the panel is otherwise
  // unconditional. In No-Project mode the workspace panel already hides the
  // Research tab, and the store is reset (no GetResearchStatus call is made);
  // this guard is the defensive neutral render for any other mount.
  if (isNoProject) {
    return null
  }

  const metrics = project?.metrics

  // Resolve artifact paths for the quick links (brief/prior-art/report/graph).
  const dir = project ? projectDir(root, project.id) : ''
  const paths = projectFilePaths(rootPath, dir)

  // Artifacts that actually exist: brief (a parsed project implies brief.md),
  // prior art (a non-placeholder catalog has entries), the graph (at least one
  // hypothesis), and the report. Items carry nothing but the artifact name.
  const artifactItems: { label: string; onClick: () => void }[] = []
  if (project) {
    artifactItems.push({ label: 'Brief', onClick: () => openArtifact(paths.brief) })
  }
  if ((project?.prior_art_count ?? 0) > 0) {
    artifactItems.push({ label: 'Prior art', onClick: () => openArtifact(paths.priorArt) })
  }
  if ((metrics?.total ?? 0) > 0) {
    artifactItems.push({ label: 'Graph', onClick: openResearchTab })
  }
  if (project?.has_report) {
    artifactItems.push({ label: 'Report', onClick: () => openArtifact(paths.report) })
  }

  return (
    <div className="flex flex-col h-full min-h-0">
      {/* Toolbar: flask + research-project picker (active R-NNN switch,
          pins, delete) + research-init plus button */}
      <div className="flex items-center gap-2 px-2 py-1 min-h-[32px] shrink-0 border-b border-border bg-secondary/30">
        <FlaskConical className="size-3.5 shrink-0 text-success" />
        <ResearchProjectPicker />
      </div>

      {segmentControl}

      {error && <ErrorBanner message={error} />}

      {segment === 'papers' ? (
        <SegmentPanel segment="papers" className="flex min-h-0 flex-1 flex-col">
          <PapersView />
        </SegmentPanel>
      ) : (
        <SegmentPanel segment="dashboard" className="flex min-h-0 flex-1 flex-col">
          {/* Control dashboard body */}
          <div className="flex-1 min-h-0 overflow-auto custom-scrollbar px-1.5 py-1.5 flex flex-col gap-2">
            {isLoading && !project ? (
              <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
                Loading…
              </div>
            ) : (
              <>
                {metrics && <ResearchMetricsRow metrics={metrics} />}
                <ResearchNextStep />
                <ResearchHypothesisPicker />
                <ResearchQuickActions />
                {project && (project.prior_art_count ?? 0) > 0 && (
                  <PriorArtRow path={paths.priorArt} count={project.prior_art_count} />
                )}
                {/* Reverse-link integrity: paper cards naming an H-NNN that
                    resolves to no hypothesis — the dangling bucket of the
                    paperStore × graph projection (renders nothing when clean). */}
                <DanglingPaperLinks />
                <ResearchLog />
              </>
            )}
          </div>

          {/* View Artifacts: one dropdown listing the artifacts that exist */}
          <div className="flex shrink-0 items-center gap-1 border-t border-border bg-secondary/20 px-2 py-1.5">
            <DropdownMenu>
              <DropdownMenuTrigger
                data-testid="research-view-artifacts"
                disabled={artifactItems.length === 0}
                title={
                  artifactItems.length === 0
                    ? 'No research artifacts yet'
                    : 'Open a research artifact'
                }
                className={cn(
                  'inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] transition-colors',
                  'text-muted-foreground',
                  artifactItems.length > 0
                    ? 'cursor-pointer hover:bg-muted'
                    : 'opacity-60',
                )}
              >
                <FolderOpen className="size-3" />
                <span className="uppercase tracking-wide">View artifacts</span>
                <ChevronDown className="size-3" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start">
                {artifactItems.map((item) => (
                  <DropdownMenuItem key={item.label} onSelect={item.onClick}>
                    {item.label}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </SegmentPanel>
      )}
    </div>
  )
}

function ErrorBanner({ message }: { message: string }) {
  return (
    <div className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-destructive bg-destructive/10 border-b border-destructive/20">
      <AlertCircle className="size-3.5 shrink-0" />
      <span className="truncate">{message}</span>
    </div>
  )
}
