import { useMemo } from 'react'
import { cn } from '@/lib/utils'
import { formatVersion } from '@/lib/formatters'
import type { ToolManagerToolInfo, ToolManagerProgressData } from '@/types/events'
import { Download, PackageOpen } from 'lucide-react'

// ── Helpers ─────────────────────────────────────────────────────────────────

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

function pct(done: number, total: number): number {
  if (total <= 0) return 0
  return Math.min(100, Math.round((done / total) * 100))
}

// ── Per-tool progress bar ───────────────────────────────────────────────────

const STAGE_LABELS: Record<string, string> = {
  download: 'Downloading',
  extract: 'Extracting',
  python_bootstrap: 'Bootstrapping Python',
}

function ToolProgressRow({ tool, progress }: { tool: ToolManagerToolInfo; progress?: ToolManagerProgressData }) {
  const hasProgress = progress !== undefined
  const done = hasProgress && progress.bytes_total > 0 && progress.bytes_done >= progress.bytes_total
  const active = hasProgress && progress.bytes_total > 0 && !done

  return (
    <div className="flex items-center gap-3 py-1.5">
      {/* Icon */}
      {!hasProgress
        ? <Download className="size-4 text-muted-foreground/50 shrink-0" />
        : done
          ? <PackageOpen className="size-4 text-success shrink-0" />
          : <Download className="size-4 text-primary shrink-0 animate-pulse" />
      }

      {/* Tool name + version */}
      <div className="flex flex-col min-w-0 flex-1">
        <div className="flex items-baseline gap-1.5">
          <span className="text-sm font-medium text-foreground truncate">
            {tool.name}
          </span>
          {tool.version && (
            <span className="text-xs text-muted-foreground shrink-0">
              {formatVersion(tool.version)}
            </span>
          )}
        </div>

        {/* Stage & bytes */}
        {progress && (
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <span>{STAGE_LABELS[progress.stage] ?? progress.stage}</span>
            {progress.bytes_total > 0 && (
              <span>
                {formatBytes(progress.bytes_done)} / {formatBytes(progress.bytes_total)}
              </span>
            )}
            {progress.bytes_total <= 0 && <span>...</span>}
          </div>
        )}
      </div>

      {/* Progress bar + percentage */}
      <div className="flex items-center gap-2 shrink-0 w-24">
        {active && (
          <>
            <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-primary transition-all duration-150"
                style={{ width: `${pct(progress.bytes_done, progress.bytes_total)}%` }}
              />
            </div>
            <span className="text-xs text-muted-foreground w-8 text-right tabular-nums">
              {pct(progress.bytes_done, progress.bytes_total)}%
            </span>
          </>
        )}
        {done && !active && (
          <span className="text-xs text-success ml-auto">Done</span>
        )}
        {!hasProgress && (
          <span className="text-xs text-muted-foreground ml-auto">Pending</span>
        )}
        {hasProgress && !done && !active && (
          <span className="text-xs text-muted-foreground ml-auto">Starting…</span>
        )}
      </div>
    </div>
  )
}

// ── Main component ──────────────────────────────────────────────────────────

export function ToolInstallSplash({ tools, progressMap }: {
  tools: readonly ToolManagerToolInfo[]
  progressMap: Map<string, ToolManagerProgressData>
}) {
  // Memoize the tools list in display order.
  const toolList = useMemo(() => tools, [tools])

  // All tools are either done (tracked in progressMap) or we're still waiting.
  // Every tool in the list that has been mentioned in a progress event is "active".
  const allDone = progressMap.size > 0 && toolList.every(t => {
    const p = progressMap.get(t.name)
    return p && p.bytes_done > 0 && p.bytes_done >= p.bytes_total
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-background">
      <div className="w-full max-w-md px-6 py-8">
        {/* Header */}
        <div className="flex flex-col items-center gap-3 mb-8">
          <div className="flex items-center justify-center size-14 rounded-2xl bg-primary/10">
            <Download className={cn('size-7', allDone ? 'text-success' : 'text-primary', !allDone && 'animate-pulse')} />
          </div>
          <h1 className="text-lg font-semibold text-foreground">
            {allDone ? 'Tools Ready' : 'Installing Tools'}
          </h1>
          <p className="text-sm text-muted-foreground text-center">
            {allDone
              ? 'All required tools are installed. Starting c0wrk…'
              : 'Downloading and installing required tools. This may take a few minutes on first run.'}
          </p>
        </div>

        {/* Tool list */}
        <div className="space-y-0.5">
          {toolList.map((tool) => (
            <ToolProgressRow
              key={tool.name}
              tool={tool}
              progress={progressMap.get(tool.name)}
            />
          ))}
        </div>

        {/* Overall progress bar */}
        {!allDone && toolList.length > 0 && (
          <div className="mt-6">
            <div className="h-2 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-primary transition-all duration-300"
                style={{
                  width: `${pct(
                    progressMap.size,
                    toolList.length,
                  )}%`,
                }}
              />
            </div>
            <p className="mt-2 text-xs text-muted-foreground text-center">
              {progressMap.size} of {toolList.length} tools ready
            </p>
          </div>
        )}
      </div>
    </div>
  )
}
