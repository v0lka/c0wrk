import { useMemo } from 'react'
import { cn } from '@/lib/utils'
import { formatVersion } from '@/lib/formatters'
import type { ToolManagerProgressData, ToolManagerStage, ToolManagerToolInfo } from '@/types/events'
import { CheckCircle2, Download, PackageOpen, XCircle } from 'lucide-react'

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

// ── Stage vocabulary (backend ToolProgress.Stage / .Detail) ────────────────

const STAGE_LABELS: Record<ToolManagerStage, string> = {
  download: 'Downloading',
  extract: 'Extracting',
  verify: 'Verifying',
  python_bootstrap: 'Bootstrapping Python',
  done: 'Done',
  failed: 'Failed',
}

const DETAIL_LABELS: Record<string, string> = {
  interpreter: 'Fetching interpreter',
  venv: 'Creating venv',
  packages: 'Installing packages',
}

type RowState = 'pending' | 'active' | 'done' | 'failed'

function rowState(p?: ToolManagerProgressData): RowState {
  if (!p) return 'pending'
  if (p.stage === 'done') return 'done'
  if (p.stage === 'failed') return 'failed'
  return 'active'
}

// ── Per-tool progress bar ───────────────────────────────────────────────────

function ToolProgressRow({ tool, progress }: { tool: ToolManagerToolInfo; progress?: ToolManagerProgressData }) {
  const state = rowState(progress)
  const known = !!progress && progress.bytes_total > 0
  const stageLabel = progress && state === 'active' ? STAGE_LABELS[progress.stage] : undefined
  const detailLabel = progress?.detail ? DETAIL_LABELS[progress.detail] ?? progress.detail : undefined

  return (
    <div className="flex items-center gap-3 py-1.5" data-testid={`tool-row-${tool.name}`}>
      {/* Status icon: a download arrow while working, the install verdict
          once the tool reached a terminal state. */}
      {state === 'pending' && <Download className="size-4 text-muted-foreground/50 shrink-0" />}
      {state === 'active' && <Download className="size-4 text-primary shrink-0 animate-pulse" />}
      {state === 'done' && <PackageOpen className="size-4 text-success shrink-0" />}
      {state === 'failed' && <XCircle className="size-4 text-destructive shrink-0" />}

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

        {/* Stage / sub-step / bytes; the verdict lives on the right. */}
        {stageLabel && (
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <span>{stageLabel}</span>
            {detailLabel && <span className="truncate">· {detailLabel}</span>}
            {known && (
              <span className="tabular-nums">
                {formatBytes(progress.bytes_done)} / {formatBytes(progress.bytes_total)}
              </span>
            )}
            {!known && !detailLabel && <span>…</span>}
          </div>
        )}
        {state === 'failed' && progress?.error && (
          <span className="text-xs text-destructive truncate" title={progress.error}>
            {progress.error}
          </span>
        )}
      </div>

      {/* Progress bar + verdict */}
      <div className="flex items-center gap-2 shrink-0 w-24">
        {state === 'active' && known && (
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
        {state === 'active' && !known && (
          // No byte count exists for this stage — pulse instead of a lying
          // percentage (same posture as EmbeddedProgressRow).
          <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
            <div className="h-full w-1/3 animate-pulse rounded-full bg-primary" />
          </div>
        )}
        {state === 'done' && (
          <span className="text-xs text-success ml-auto flex items-center gap-1">
            <CheckCircle2 className="size-3.5" />
            Done
          </span>
        )}
        {state === 'failed' && <span className="text-xs text-destructive ml-auto">Failed</span>}
        {state === 'pending' && <span className="text-xs text-muted-foreground ml-auto">Pending</span>}
      </div>
    </div>
  )
}

// ── Main component ──────────────────────────────────────────────────────────

export function ToolInstallSplash({ tools, progressMap, toolsTotal = 0 }: {
  tools: readonly ToolManagerToolInfo[]
  progressMap: Map<string, ToolManagerProgressData>
  /** Splash denominator stamped by the backend onto every progress event —
   *  the authoritative total even when tool_manager:start was lost and the
   *  visible list is still the incrementally derived one. */
  toolsTotal?: number
}) {
  // Memoize the tools list in display order.
  const toolList = useMemo(() => tools, [tools])

  // "Ready" means the tool's install reported its terminal done — a row that
  // merely started is not ready. The denominator prefers the stamped total.
  const total = Math.max(toolList.length, toolsTotal)
  const doneCount = useMemo(() => {
    let n = 0
    for (const t of toolList) {
      if (progressMap.get(t.name)?.stage === 'done') n++
    }
    return n
  }, [toolList, progressMap])
  const allDone = progressMap.size > 0 && total > 0 && doneCount >= total

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
        {!allDone && total > 0 && (
          <div className="mt-6">
            <div className="h-2 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-primary transition-all duration-300"
                style={{ width: `${pct(doneCount, total)}%` }}
              />
            </div>
            <p className="mt-2 text-xs text-muted-foreground text-center">
              {doneCount} of {total} tools ready
            </p>
          </div>
        )}
      </div>
    </div>
  )
}
