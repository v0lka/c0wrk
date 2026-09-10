// Download progress bar for the self-update flow.
//
// Isolated so the ~100ms update:progress ticks re-render only this component
// (which subscribes to the progress slice directly) — not the parent surface
// that carries the action buttons. `total === 0` renders an indeterminate
// pulse; otherwise a determinate bar with byte/percentage readouts.

import { useUpdateProgress } from '@/stores/updateStore'
import { formatBytes } from '@/lib/formatters'

function pct(done: number, total: number): number {
  if (total <= 0) return 0
  return Math.min(100, Math.round((done / total) * 100))
}

export function DownloadProgressBar() {
  const progress = useUpdateProgress()
  const done = progress?.done ?? 0
  const total = progress?.total ?? 0
  const known = total > 0

  return (
    <div className="space-y-1.5">
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
        {known ? (
          <div
            className="h-full rounded-full bg-primary transition-all duration-150"
            style={{ width: `${pct(done, total)}%` }}
          />
        ) : (
          <div className="h-full w-1/3 rounded-full bg-primary animate-pulse" />
        )}
      </div>
      <div className="flex items-center justify-between text-xs text-muted-foreground tabular-nums">
        <span>{known ? `${formatBytes(done)} / ${formatBytes(total)}` : 'Loading…'}</span>
        {known && <span>{pct(done, total)}%</span>}
      </div>
    </div>
  )
}
