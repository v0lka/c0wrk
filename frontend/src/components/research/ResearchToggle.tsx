import { useCallback } from 'react'
import { FlaskConical, Loader2, Power, PowerOff } from 'lucide-react'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'
import { enableResearch, disableResearch } from '@/api/research'
import { useProjectStore } from '@/stores/projectStore'
import { useResearchStore } from '@/stores/researchStore'

interface ResearchToggleProps {
  /** Render as a compact toolbar button (default) or a full empty-state card. */
  variant?: 'button' | 'card'
  className?: string
}

/**
 * Mode-toggle control for RESEARCH. Calls EnableResearch / DisableResearch,
 * then reflects the result in both researchStore (parsed status) and
 * projectStore (the persisted `research_root` / `is_research` flag) so the
 * panel reveals/hides immediately. Disabled while a toggle is in flight.
 */
export function ResearchToggle({ variant = 'button', className }: ResearchToggleProps) {
  const isToggling = useResearchStore((s) => s.isToggling)
  const enabled = useResearchStore((s) => s.status?.enabled ?? false)

  const handleToggle = useCallback(async () => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId || useResearchStore.getState().isToggling) return

    useResearchStore.getState().setToggling(true)
    try {
      if (!enabled) {
        const status = await enableResearch(projectId)
        useResearchStore.getState().loadStatus(status, projectId)
        // Mirror the persisted toggle into projectStore so any other consumer
        // (and the panel's reveal/hide) sees the new state without a reload.
        useProjectStore.getState().updateProject(projectId, {
          research_root: status.research_root,
          is_research: true,
        })
      } else {
        await disableResearch(projectId)
        useResearchStore.getState().loadStatus(
          { enabled: false, project_id: projectId, research_root: '' },
          projectId,
        )
        useProjectStore.getState().updateProject(projectId, {
          research_root: '',
          is_research: false,
        })
      }
    } catch (err) {
      logger.error('Failed to toggle research mode:', err)
      useResearchStore.getState().setError(
        err instanceof Error ? err.message : 'Failed to toggle research mode',
      )
    } finally {
      useResearchStore.getState().setToggling(false)
    }
  }, [enabled])

  if (variant === 'card') {
    return (
      <div
        className={cn(
          'flex flex-col items-center justify-center gap-3 px-6 py-10 text-center select-none',
          className,
        )}
      >
        <FlaskConical className="size-9 opacity-40" />
        <div className="space-y-1">
          <p className="text-sm font-medium">RESEARCH mode is off</p>
          <p className="text-xs text-muted-foreground max-w-[220px]">
            Enable structured hypothesis tracking with a live DAG, confirmation
            metrics, and a methodology skill-pack.
          </p>
        </div>
        <button
          type="button"
          onClick={handleToggle}
          disabled={isToggling}
          className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-50"
        >
          {isToggling && <Loader2 className="size-3.5 animate-spin" />}
          <FlaskConical className="size-3.5" />
          Enable RESEARCH
        </button>
      </div>
    )
  }

  // Compact toolbar control: an icon-only power button. While RESEARCH is on
  // (the only place this variant renders — the enabled panel header) it reads
  // as a destructive-toned "power off" affordance with no text label; the
  // muted "power on" look is a defensive fallback for the off state.
  return (
    <button
      type="button"
      onClick={handleToggle}
      disabled={isToggling}
      title={enabled ? 'Disable RESEARCH mode' : 'Enable RESEARCH mode'}
      aria-label={enabled ? 'Disable RESEARCH mode' : 'Enable RESEARCH mode'}
      aria-pressed={enabled}
      className={cn(
        'inline-flex shrink-0 items-center justify-center rounded-md p-1 transition-colors disabled:opacity-50',
        enabled
          ? 'text-destructive hover:bg-destructive/10'
          : 'text-muted-foreground hover:bg-muted',
        className,
      )}
    >
      {isToggling ? (
        <Loader2 className="size-3.5 shrink-0 animate-spin" />
      ) : enabled ? (
        <PowerOff className="size-3.5 shrink-0" />
      ) : (
        <Power className="size-3.5 shrink-0" />
      )}
    </button>
  )
}
