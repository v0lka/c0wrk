import { useState, useCallback, useRef } from 'react'
import { FlaskConical, FileText, BookMarked, FileCheck2, Plus, AlertCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useResearchStatusEvents } from '@/hooks/useResearchStatusEvents'
import { useResearchFileWatcher } from '@/hooks/useResearchFileWatcher'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { ResearchToggle } from './ResearchToggle'
import { ResearchMetricsRow } from './ResearchMetrics'
import { ResearchHypothesisList } from './ResearchHypothesisList'
import { projectDir, projectFilePaths } from './researchDagRender'

/**
 * RESEARCH panel — the signature live hypothesis tree + metrics view.
 *
 * Modeled on the Git panel: subscribes to research:changed /
 * workspace:tree_changed via the side-effect hook, then renders either a
 * compact "Enable RESEARCH" empty state (when off) or the full panel —
 * toolbar with the mode toggle, confirmation/depth/active-front metrics, the
 * hypothesis list (a readable indented vertical tree), and clickable
 * quick-link chips for the brief / prior-art / graph / report.
 *
 * The bottom links and hypothesis rows are interactive: clicking a quick link
 * opens the underlying `.research` artifact in the file viewer (ReadFile is
 * path-agnostic, so `.research/` files read fine), and "+ New hypothesis"
 * opens the hypothesis graph.md so the user can add an entry.
 */
export function ResearchPanel() {
  // Side-effect hook: keeps researchStore in sync with the backend.
  useResearchStatusEvents()

  // Side-effect hook: incrementally updates the hypothesis graph when files
  // inside the research directory change (hypothesis cards, brief, etc.).
  useResearchFileWatcher()

  const enabled = useResearchStore((s) => s.status?.enabled ?? false)
  const project = useResearchStore(selectActiveProject)
  const rootPath = useResearchStore((s) => s.status?.research_root ?? '')
  const root = useResearchStore((s) => s.status?.root)
  const error = useResearchStore((s) => s.error)
  const isLoading = useResearchStore((s) => s.isLoading)

  const [selectedNode, setSelectedNode] = useState<string | undefined>(undefined)
  const onSelectNode = useCallback((id: string) => {
    setSelectedNode((prev) => (prev === id ? undefined : id))
  }, [])

  // Ref to the scroll container for preserving scroll position across
  // incremental graph updates.
  const scrollContainerRef = useRef<HTMLDivElement>(null)

  // Open a research artifact file in the file viewer (path-agnostic ReadFile).
  const openArtifact = useCallback((filePath: string) => {
    const store = useFileViewerStore.getState()
    store.setCollapsed(false)
    store.openFile(filePath)
  }, [])

  // ── RESEARCH off → toggle empty state ────────────────────────────────
  if (!enabled) {
    return (
      <div className="flex flex-1 flex-col min-h-0">
        {error && <ErrorBanner message={error} />}
        <div className="flex flex-1 items-center justify-center min-h-0">
          <ResearchToggle variant="card" />
        </div>
      </div>
    )
  }

  const graph = project?.graph ?? { nodes: [], edges: [] }
  const metrics = project?.metrics
  const brief = project?.brief

  // Resolve artifact paths for the quick links (brief/prior-art/report/graph).
  const dir = project ? projectDir(root, project.id) : ''
  const paths = projectFilePaths(rootPath, dir)

  return (
    <div className="flex flex-col h-full min-h-0">
      {/* Toolbar: title + mode toggle (disable) */}
      <div className="flex items-center gap-2 px-2 py-1 min-h-[32px] shrink-0 border-b border-border bg-secondary/30">
        <FlaskConical className="size-3.5 shrink-0 text-success" />
        <span className="truncate text-xs font-medium">
          {brief?.title ?? 'Research'}
        </span>
        <div className="ml-auto shrink-0">
          <ResearchToggle variant="button" />
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      {/* Metrics row */}
      {metrics && <ResearchMetricsRow metrics={metrics} />}

      {/* Hypothesis list (readable vertical tree) */}
      <div
        ref={scrollContainerRef}
        className="flex-1 min-h-0 overflow-auto px-1"
      >
        {isLoading && !project ? (
          <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
            Loading…
          </div>
        ) : (
          <ResearchHypothesisList
            graph={graph}
            selectedId={selectedNode}
            onSelectNode={onSelectNode}
            scrollContainerRef={scrollContainerRef}
          />
        )}
      </div>

      {/* Quick links: brief / prior-art / graph / report / +hypothesis — all clickable */}
      <div className="flex shrink-0 flex-wrap items-center gap-1 border-t border-border bg-secondary/20 px-2 py-1.5">
        <QuickLink
          icon={FileText}
          label="Brief"
          value={brief?.title ?? '—'}
          onClick={() => openArtifact(paths.brief)}
        />
        <QuickLink
          icon={BookMarked}
          label="Prior art"
          value={String(project?.prior_art_count ?? 0)}
          onClick={() => openArtifact(paths.priorArt)}
        />
        <QuickLink
          icon={FileText}
          label="Graph"
          value=""
          onClick={() => openArtifact(paths.graph)}
        />
        <QuickLink
          icon={FileCheck2}
          label="Report"
          value={project?.has_report ? 'ready' : 'none'}
          tone={project?.has_report ? 'text-success' : 'text-muted-foreground'}
          onClick={project?.has_report ? () => openArtifact(paths.report) : undefined}
        />
        <QuickLink
          icon={Plus}
          label="New hypothesis"
          value=""
          primary
          onClick={() => openArtifact(paths.graph)}
        />
      </div>
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

interface QuickLinkProps {
  icon: typeof FileText
  label: string
  value: string
  tone?: string
  primary?: boolean
  muted?: boolean
  onClick?: () => void
}

function QuickLink({ icon: Icon, label, value, tone, primary, muted, onClick }: QuickLinkProps) {
  const interactive = !!onClick
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!interactive}
      title={interactive ? `Open ${label}` : label}
      className={cn(
        'inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] transition-colors',
        interactive && 'cursor-pointer hover:bg-muted',
        !interactive && 'cursor-default',
        primary
          ? 'font-medium text-success hover:bg-success/10'
          : muted
            ? 'text-muted-foreground/70'
            : 'text-muted-foreground',
      )}
    >
      <Icon className={cn('size-3', tone)} />
      <span className="uppercase tracking-wide">{label}</span>
      {value && (
        <span className="max-w-[80px] truncate font-medium text-foreground">
          {value}
        </span>
      )}
    </button>
  )
}
