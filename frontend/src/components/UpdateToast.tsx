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
//
// Suppressed while the Settings modal is open on the About tab: the inline
// About flow renders the same phases in place of the "Check for updates"
// button, so the corner toast would be redundant (and it sits under the modal
// overlay anyway).

import { Download, RefreshCw, X, AlertCircle, Sparkles } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DownloadProgressBar } from '@/components/update/DownloadProgressBar'
import { useUpdateActions } from '@/hooks/useUpdateActions'
import {
  useUpdatePhase,
  useUpdateInfo,
  useCurrentVersion,
  useUpdateError,
} from '@/stores/updateStore'
import { useSettingsStore } from '@/stores/settingsStore'
import { cn } from '@/lib/utils'
import { formatVersion } from '@/lib/formatters'

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
  const { handleUpdate, handleSkip, handleDismiss, isDownloading } = useUpdateActions()

  if (!info) return null

  return (
    <ToastShell
      icon={<Sparkles className="size-5 text-highlight" />}
      accent="info"
      onClose={handleDismiss}
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
          <Button size="xs" variant="ghost" onClick={handleDismiss}>
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
  const { handleRestart, handleDismiss } = useUpdateActions()

  return (
    <ToastShell
      icon={<RefreshCw className="size-5 text-success" />}
      accent="success"
      onClose={handleDismiss}
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
          <Button size="xs" variant="ghost" onClick={handleDismiss}>
            Later
          </Button>
        </div>
      </div>
    </ToastShell>
  )
}

function ErrorSurface() {
  const errorMessage = useUpdateError()
  const { handleDismiss } = useUpdateActions()
  return (
    <ToastShell
      icon={<AlertCircle className="size-5 text-destructive" />}
      accent="destructive"
      onClose={handleDismiss}
    >
      <div className="space-y-1">
        <p className="text-sm font-medium text-foreground">Update failed</p>
        <p className="text-xs text-muted-foreground">{errorMessage ?? 'An error occurred'}</p>
      </div>
    </ToastShell>
  )
}

// ── Root ────────────────────────────────────────────────────────────────────

/** Fixed bottom-right update toast. Renders nothing when idle, and nothing
 *  while the Settings modal is open on the About tab (the inline flow takes
 *  over). Mount once at the app root. */
export function UpdateToast() {
  const phase = useUpdatePhase()
  const aboutOpen = useSettingsStore((s) => s.open && s.activeTab === 'about')

  if (phase === 'idle' || aboutOpen) return null

  return (
    <div className="pointer-events-none fixed bottom-4 right-4 z-50 flex flex-col items-end gap-2">
      {phase === 'available' && <AvailableSurface />}
      {phase === 'downloading' && <DownloadingSurface />}
      {phase === 'downloaded' && <DownloadedSurface />}
      {phase === 'error' && <ErrorSurface />}
    </div>
  )
}
