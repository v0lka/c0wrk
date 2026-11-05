import { useCallback, useState } from 'react'
import { AlertTriangle, FolderInput, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { migrateResearchRoots } from '@/api/research'
import { useResearchStore } from '@/stores/researchStore'

interface ResearchLegacyMigrationProps {
  /** Detected pre-rename `<workspace>/.research` roots (absolute paths),
   *  from the status payload's `legacy_research_roots`. */
  legacyRoots: string[]
  /** The project the status belongs to — the migration RPC's target. */
  projectId: string
}

/**
 * Blocking migration notice for pre-rename research roots.
 *
 * Older builds kept the research root at `<workspace>/.research`; the
 * canonical location is now `<workspace>/research`. While a legacy root
 * still exists on disk, the Research panel renders THIS notice instead of
 * its Dashboard/Papers content — the data behind them lives in the
 * unmigrated directory, so the panel has nothing truthful to show.
 *
 * The single button runs the backend `MigrateResearchRoots` RPC (an atomic
 * per-root rename; the backend refuses a non-empty canonical destination
 * and symlinked/file legacy roots — those errors render inline here). On
 * success the returned fresh status is loaded into researchStore directly:
 * its now-empty `legacy_research_roots` unmounts this notice and the panel
 * content returns without waiting for the `research:changed` event.
 */
export function ResearchLegacyMigration({
  legacyRoots,
  projectId,
}: ResearchLegacyMigrationProps) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const runMigration = useCallback(async () => {
    setBusy(true)
    setError(null)
    try {
      const status = await migrateResearchRoots(projectId)
      useResearchStore.getState().loadStatus(status, projectId)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }, [projectId])

  return (
    <div
      data-testid="research-legacy-migration"
      className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 overflow-auto custom-scrollbar px-4 py-6"
    >
      <div className="flex items-center gap-2 text-warning">
        <AlertTriangle className="size-4 shrink-0" />
        <span className="text-sm font-medium">Research directory needs migrating</span>
      </div>
      <p className="max-w-md text-center text-xs text-muted-foreground">
        An older c0wrk build kept research data in{' '}
        <code className="font-mono text-xs">.research</code>. Rename it to the canonical{' '}
        <code className="font-mono text-xs">research</code> directory to continue — the
        move is atomic and keeps every file.
      </p>
      <ul className="flex max-w-md flex-col gap-1" data-testid="research-legacy-migration-roots">
        {legacyRoots.map((root) => (
          <li key={root} className="truncate rounded bg-secondary/40 px-1.5 py-0.5 text-center font-mono text-xs">
            {root}
          </li>
        ))}
      </ul>
      <Button
        data-testid="research-legacy-migration-migrate"
        size="sm"
        title="Rename every legacy .research root to the canonical research directory"
        disabled={busy}
        onClick={() => void runMigration()}
      >
        {busy ? <Loader2 className="size-3.5 animate-spin" /> : <FolderInput className="size-3.5" />}
        {busy ? 'Migrating…' : 'Migrate research directory'}
      </Button>
      {error && (
        <div
          data-testid="research-legacy-migration-error"
          role="alert"
          className="max-w-md rounded border border-destructive/20 bg-destructive/10 px-2 py-1.5 text-xs text-destructive"
        >
          {error}
        </div>
      )}
    </div>
  )
}
