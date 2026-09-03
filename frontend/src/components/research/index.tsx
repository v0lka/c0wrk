import { useCallback } from 'react'
import { FlaskConical, FolderOpen, ChevronDown, AlertCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useResearchStatusEvents } from '@/hooks/useResearchStatusEvents'
import { useResearchFileWatcher } from '@/hooks/useResearchFileWatcher'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { ResearchToggle } from './ResearchToggle'
import { ResearchMetricsRow } from './ResearchMetrics'
import { ResearchNextStep } from './ResearchNextStep'
import { ResearchQuickActions } from './ResearchQuickActions'
import { ResearchQuickMutate } from './ResearchQuickMutate'
import { ResearchLog } from './ResearchLog'
import { projectDir, projectFilePaths } from './researchDagRender'

/**
 * RESEARCH panel — a control dashboard, not a passive mirror.
 *
 * Modeled on the Git panel: subscribes to research:changed /
 * workspace:tree_changed (full status) and research:file_changed (incremental
 * graph + log) via the side-effect hooks, then renders a compact control
 * surface — a status/metrics header, the recommended next step with one-click
 * execution, a quick-actions row that dispatches research-* skills, quick
 * status mutations on the active front (t4), and the research log (t1).
 *
 * The hypothesis tree/DAG presentation lives in the Research workspace tab
 * (t5); the bottom bar is a single View Artifacts dropdown that opens the
 * brief / prior-art / graph / report artifacts that actually exist.
 */
export function ResearchPanel() {
  // Side-effect hook: keeps researchStore (status + next step) in sync.
  useResearchStatusEvents()

  // Side-effect hook: incrementally updates the graph, log, and next step when
  // files inside the research directory change.
  useResearchFileWatcher()

  const enabled = useResearchStore((s) => s.status?.enabled ?? false)
  const project = useResearchStore(selectActiveProject)
  const rootPath = useResearchStore((s) => s.status?.research_root ?? '')
  const root = useResearchStore((s) => s.status?.root)
  const error = useResearchStore((s) => s.error)
  const isLoading = useResearchStore((s) => s.isLoading)

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

  const metrics = project?.metrics
  const brief = project?.brief

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
      {/* Toolbar: title + mode toggle (disable) */}
      <div className="flex items-center gap-2 px-2 py-1 min-h-[32px] shrink-0 border-b border-border bg-secondary/30">
        <FlaskConical className="size-3.5 shrink-0 text-success" />
        <span
          className="truncate text-xs font-medium"
          title={brief?.title ?? 'Research'}
        >
          {brief?.title ?? 'Research'}
        </span>
        <div className="ml-auto shrink-0">
          <ResearchToggle variant="button" />
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      {/* Control dashboard body */}
      <div className="flex-1 min-h-0 overflow-auto px-1.5 py-1.5 flex flex-col gap-2">
        {isLoading && !project ? (
          <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
            Loading…
          </div>
        ) : (
          <>
            {metrics && <ResearchMetricsRow metrics={metrics} />}
            <ResearchNextStep />
            <ResearchQuickActions />
            <ResearchQuickMutate />
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
                : 'cursor-default opacity-60',
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
