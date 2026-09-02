import { useMemo } from 'react'
import {
  FlaskConical,
  Split,
  RefreshCw,
  FileText,
  type LucideIcon,
} from 'lucide-react'
import type { LogKind } from '@/types/models'
import { useResearchStore, selectActiveLog } from '@/stores/researchStore'
import { latestLogEntries, formatLogTime } from './researchLogUtils'

const KIND_ICONS: Record<LogKind, LucideIcon> = {
  experiment: FlaskConical,
  decision: Split,
  status_change: RefreshCw,
  note: FileText,
}

/**
 * Research log — the most recent `log.md` entries for the active project (t1).
 * Reads `selectActiveLog` (a stable store reference) so it re-renders when the
 * log is refreshed via `loadStatus`/`loadGraph` on research:file_changed.
 */
export function ResearchLog() {
  const log = useResearchStore(selectActiveLog)
  const entries = useMemo(() => latestLogEntries(log), [log])

  return (
    <div
      data-testid="research-log"
      className="flex shrink-0 flex-col gap-1 border-t border-border pt-2"
    >
      <span className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
        Research log
      </span>

      {entries.length === 0 ? (
        <p className="text-xs text-muted-foreground/70">No entries yet</p>
      ) : (
        <ul className="flex flex-col gap-1">
          {entries.map((entry) => {
            const Icon = KIND_ICONS[entry.kind] ?? FileText
            return (
              <li
                key={entry.id}
                data-testid="research-log-entry"
                className="flex items-start gap-1.5 rounded bg-background/60 px-2 py-1"
              >
                <Icon className="mt-0.5 size-3 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline gap-1.5">
                    {entry.hypothesis_id && (
                      <span className="shrink-0 font-mono text-[10px] text-info">
                        {entry.hypothesis_id}
                      </span>
                    )}
                    <span className="truncate text-xs text-foreground/90">
                      {entry.message}
                    </span>
                  </div>
                  <span className="font-mono text-[10px] text-muted-foreground/60">
                    {formatLogTime(entry.created_at)}
                  </span>
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
