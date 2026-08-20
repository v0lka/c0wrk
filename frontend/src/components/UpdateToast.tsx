// Update toast — surfaces the self-update lifecycle to the user.
//
// Renders nothing while the store phase is `idle`. Otherwise shows one of:
//   • available   — "c0wrk vX.Y.Z is available (you're on vA.B.C)" + [Update][Skip][Later]
//   • downloading — indeterminate/progress bar (bytes done / total)
//   • downloaded  — "Restart to install?" + [Restart][Later]
//   • error       — the failure message + a dismiss button
//
// Performance: the parent subscribes ONLY to `phase` (and the static `info`
// for the available surface), so it re-renders only on phase transitions. The
// ~100ms progress ticks re-render only the isolated <DownloadProgressBar/>
// sub-component, which subscribes to the progress slice directly — no
// re-render storm on the button-bearing parent.

import { useCallback } from 'react'
import { Download, RefreshCw, X, AlertCircle, Sparkles } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  useUpdatePhase,
  useUpdateInfo,
  useCurrentVersion,
  useUpdateError,
  useUpdateDownloading,
  useUpdateProgress,
} from '@/stores/updateStore'
import { downloadUpdate, applyUpdate, skipVersion } from '@/api/updater'
import { useUpdateStore } from '@/stores/updateStore'
import { cn } from '@/lib/utils'
import { formatVersion } from '@/lib/formatters'

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

// ── Progress bar (isolated to absorb ~100ms ticks) ──────────────────────────

function DownloadProgressBar() {
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

// ── Toast shell ─────────────────────────────────────────────────────────────

function ToastShell({
  icon,
  accent,
  children,
  onClose,
}: {
  icon: React.ReactNode
  accent: string
  children: React.ReactNode
  onClose?: () => void
}) {
  return (
    <div
      className={cn(
        'pointer-events-auto flex w-80 flex-col gap-3 rounded-lg border border-border bg-popover p-4 shadow-lg',
        'animate-in fade-in slide-in-from-bottom-2 duration-200',
      )}
      role="alert"
      data-accent={accent}
    >
      <div className="flex items-start gap-3">
        <div className="mt-0.5 shrink-0">{icon}</div>
        <div className="min-w-0 flex-1">{children}</div>
        {onClose && (
          <button
            onClick={onClose}
            className="shrink-0 rounded p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            aria-label="Close"
          >
            <X className="size-4" />
          </button>
        )}
      </div>
    </div>
  )
}

// ── Surfaces ────────────────────────────────────────────────────────────────

function AvailableSurface() {
  const info = useUpdateInfo()
  const currentVersion = useCurrentVersion()
  const isDownloading = useUpdateDownloading()
  const dismiss = useUpdateStore((s) => s.dismiss)
  const setDownloading = useUpdateStore((s) => s.setDownloading)

  const handleUpdate = useCallback(async () => {
    if (isDownloading) return
    setDownloading(true)
    try {
      await downloadUpdate()
    } catch {
      // update:error event drives the error surface; just clear the busy flag.
      setDownloading(false)
    }
  }, [isDownloading, setDownloading])

  const handleSkip = useCallback(async () => {
    if (!info) return
    const latest = info.latest_version
    try {
      await skipVersion(latest)
    } catch {
      // skipVersion already logs the failure; still dismiss below so the toast
      // doesn't reappear for this version within the session.
    } finally {
      // Whether or not the persist succeeded, hide the toast for this version
      // this session. SkipVersion invalidates the cached check on the backend.
      dismiss()
    }
  }, [info, dismiss])

  if (!info) return null

  return (
    <ToastShell
      icon={<Sparkles className="size-5 text-highlight" />}
      accent="info"
      onClose={dismiss}
    >
      <div className="space-y-3">
        <div>
          <p className="text-sm font-medium text-foreground">
            c0wrk {formatVersion(info.latest_version)} is available
          </p>
          <p className="text-xs text-muted-foreground">
            {currentVersion ? `you're on ${formatVersion(currentVersion)}` : 'a new update is available'}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="xs" onClick={handleUpdate} disabled={isDownloading}>
            <Download className="size-3" />
            Update
          </Button>
          <Button size="xs" variant="ghost" onClick={handleSkip}>
            Skip
          </Button>
          <Button size="xs" variant="ghost" onClick={dismiss}>
            Later
          </Button>
        </div>
      </div>
    </ToastShell>
  )
}

function DownloadingSurface() {
  return (
    <ToastShell icon={<Download className="size-5 text-info" />} accent="info">
      <div className="space-y-2">
        <p className="text-sm font-medium text-foreground">Downloading update…</p>
        <DownloadProgressBar />
      </div>
    </ToastShell>
  )
}

function DownloadedSurface() {
  const dismiss = useUpdateStore((s) => s.dismiss)
  const handleRestart = useCallback(() => {
    applyUpdate().catch(() => {
      // update:error event surfaces the failure; keep the toast so the user
      // can retry or dismiss.
    })
  }, [])

  return (
    <ToastShell
      icon={<RefreshCw className="size-5 text-success" />}
      accent="success"
      onClose={dismiss}
    >
      <div className="space-y-3">
        <div>
          <p className="text-sm font-medium text-foreground">Update ready</p>
          <p className="text-xs text-muted-foreground">Restart to install?</p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="xs" onClick={handleRestart}>
            <RefreshCw className="size-3" />
            Restart
          </Button>
          <Button size="xs" variant="ghost" onClick={dismiss}>
            Later
          </Button>
        </div>
      </div>
    </ToastShell>
  )
}

function ErrorSurface() {
  const errorMessage = useUpdateError()
  const dismiss = useUpdateStore((s) => s.dismiss)
  return (
    <ToastShell
      icon={<AlertCircle className="size-5 text-destructive" />}
      accent="destructive"
      onClose={dismiss}
    >
      <div className="space-y-1">
        <p className="text-sm font-medium text-foreground">Update failed</p>
        <p className="text-xs text-muted-foreground">{errorMessage ?? 'An error occurred'}</p>
      </div>
    </ToastShell>
  )
}

// ── Root ────────────────────────────────────────────────────────────────────

/** Fixed bottom-right update toast. Renders nothing when idle. Mount once at
 *  the app root. */
export function UpdateToast() {
  const phase = useUpdatePhase()
  if (phase === 'idle') return null

  return (
    <div className="pointer-events-none fixed bottom-4 right-4 z-50 flex flex-col items-end gap-2">
      {phase === 'available' && <AvailableSurface />}
      {phase === 'downloading' && <DownloadingSurface />}
      {phase === 'downloaded' && <DownloadedSurface />}
      {phase === 'error' && <ErrorSurface />}
    </div>
  )
}
