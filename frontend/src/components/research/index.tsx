import { useCallback } from 'react'
import { FlaskConical, FileText, BookMarked, FileCheck2, Plus, AlertCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useResearchStatusEvents } from '@/hooks/useResearchStatusEvents'
import { useResearchFileWatcher } from '@/hooks/useResearchFileWatcher'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
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
 * The hypothesis tree/DAG presentation moved to the Research workspace tab
 * (t5); the bottom links remain for opening the brief / prior-art / graph /
 * report artifacts.
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
          onClick={openResearchTab}
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
          onClick={openResearchTab}
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
