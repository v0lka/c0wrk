// Updates section for the Settings "about" tab.
//
// Shows the running build version, the auto-check toggle (persisted to
// config.yaml updates.auto_check), and a
// "Check for updates" button that triggers an explicit check. The check result
// flows back through the global update:available / update:none / update:error
// events, which updateStore already consumes.
//
// While an update is in flight (phase !== idle), the corner toast is
// suppressed (see UpdateToast) and this component renders the full lifecycle
// inline, in place of the "Check for updates" button: available → downloading
// (progress) → downloaded (restart confirm) → error. The explicit-check outcome
// (up-to-date / failed check) remains a separate, idle-only surface so it never
// collides with the store's download/apply error phase.
//
// The operator-level master gate (config.yaml updates.enabled) is the sole
// enable/disable switch for the whole subsystem. It is reflected via the
// operator_enabled flag: when an administrator has disabled updates, the
// auto-check toggle is disabled and a notice is shown.

import { useCallback, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Download, RefreshCw, Check, AlertCircle, ShieldAlert, Sparkles } from 'lucide-react'
import { checkForUpdates, getUpdateSettings, setUpdateSettings } from '@/api/updater'
import type { UpdateSettings as UpdateSettingsData } from '@/api/updater'
import { DownloadProgressBar } from '@/components/update/DownloadProgressBar'
import { useUpdateActions } from '@/hooks/useUpdateActions'
import {
  useCurrentVersion,
  useUpdateChecking,
  useUpdateStore,
  useUpdatePhase,
  useUpdateInfo,
  useUpdateError,
} from '@/stores/updateStore'
import { logger } from '@/lib/logger'
import { formatVersion } from '@/lib/formatters'

type CheckOutcome = 'idle' | 'up-to-date' | 'error'

export function UpdateSettings() {
  const currentVersion = useCurrentVersion()
  const isChecking = useUpdateChecking()
  const phase = useUpdatePhase()
  const setChecking = useUpdateStore((s) => s.setChecking)
  const setCurrentVersion = useUpdateStore((s) => s.setCurrentVersion)
  const [outcome, setOutcome] = useState<CheckOutcome>('idle')
  const [settings, setSettings] = useState<UpdateSettingsData | null>(null)

  // Load the persisted preferences (auto-check, operator gate) so the toggles
  // reflect the authoritative backend state.
  useEffect(() => {
    let cancelled = false
    getUpdateSettings()
      .then((s) => {
        if (!cancelled) setSettings(s)
      })
      .catch((err) => logger.warn('Could not load update settings:', err))
    return () => {
      cancelled = true
    }
  }, [])

  // When an explicit check surfaces an update flow, reset the local outcome so
  // stale "up to date" text doesn't linger beside the inline update surface.
  useEffect(() => {
    if (phase === 'available' || phase === 'downloading' || phase === 'downloaded') {
      setOutcome('idle')
    }
  }, [phase])

  const handleCheck = useCallback(async () => {
    setChecking(true)
    setOutcome('idle')
    try {
      const info = await checkForUpdates()
      setCurrentVersion(info.current_version)
      if (!info.available) {
        setOutcome('up-to-date')
      }
    } catch {
      setOutcome('error')
    } finally {
      setChecking(false)
    }
  }, [setChecking, setCurrentVersion])

  const handleToggle = useCallback(
    async (field: 'auto_check', value: boolean) => {
      if (!settings) return
      // Optimistically update the local state for snappy feedback.
      const next = { ...settings, [field]: value }
      setSettings(next)
      try {
        const resolved = await setUpdateSettings(next.auto_check)
        setSettings(resolved)
      } catch (err) {
        // Revert on failure.
        setSettings(settings)
        logger.error('Failed to save update setting:', err)
      }
    },
    [settings],
  )

  const operatorDisabled = settings ? !settings.operator_enabled : false
  // Auto-check is meaningful only when not administrator-disabled.
  const autoCheckDisabled = !settings || operatorDisabled
  const idle = phase === 'idle'

  return (
    <div className="border-t border-border pt-4">
      <h3 className="mb-3 text-sm font-medium text-foreground">Updates</h3>
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm text-foreground">Current version</p>
            <p className="text-xs text-muted-foreground">
              {currentVersion ? formatVersion(currentVersion) : '—'}
            </p>
          </div>
          {idle && (
            <Button
              size="sm"
              variant="outline"
              onClick={handleCheck}
              disabled={isChecking || operatorDisabled}
            >
              <RefreshCw className={`size-4 ${isChecking ? 'animate-spin' : ''}`} />
              Check for updates
            </Button>
          )}
        </div>

        {/* Inline update lifecycle — replaces the corner toast while the About
            tab is open. Renders in place of the "Check for updates" button. */}
        {!idle && <InlineUpdateFlow />}

        {operatorDisabled && (
          <p className="flex items-center gap-1.5 text-xs text-warning">
            <ShieldAlert className="size-3.5" />
            Updates are disabled by the administrator (config.yaml)
          </p>
        )}

        {/* Auto-check toggle */}
        <ToggleRow
          label="Check automatically"
          checked={settings ? settings.auto_check : true}
          disabled={autoCheckDisabled}
          onChange={(v) => handleToggle('auto_check', v)}
        />

        {/* Explicit-check outcome — idle-only so it never collides with the
            store's download/apply error phase. */}
        {idle && outcome === 'up-to-date' && (
          <p className="flex items-center gap-1.5 text-xs text-success">
            <Check className="size-3.5" />
            You're running the latest version
          </p>
        )}
        {idle && outcome === 'error' && (
          <p className="flex items-center gap-1.5 text-xs text-destructive">
            <AlertCircle className="size-3.5" />
            Failed to check for updates
          </p>
        )}
      </div>
    </div>
  )
}

// ── Inline update lifecycle ─────────────────────────────────────────────────

function InlineUpdateFlow() {
  const phase = useUpdatePhase()
  switch (phase) {
    case 'available':
      return <InlineAvailableSurface />
    case 'downloading':
      return <InlineDownloadingSurface />
    case 'downloaded':
      return <InlineDownloadedSurface />
    case 'error':
      return <InlineErrorSurface />
    default:
      return null
  }
}

function InlineSurface({
  icon,
  accent,
  children,
}: {
  icon: React.ReactNode
  accent: string
  children: React.ReactNode
}) {
  return (
    <div
      className="rounded-md border border-border bg-muted/30 p-3"
      data-accent={accent}
    >
      <div className="flex items-start gap-2.5">
        <div className="mt-0.5 shrink-0">{icon}</div>
        <div className="min-w-0 flex-1 space-y-2">{children}</div>
      </div>
    </div>
  )
}

function InlineAvailableSurface() {
  const info = useUpdateInfo()
  const currentVersion = useCurrentVersion()
  const { handleUpdate, handleSkip, handleDismiss, isDownloading } = useUpdateActions()

  if (!info) return null

  return (
    <InlineSurface icon={<Sparkles className="size-5 text-highlight" />} accent="info">
      <div>
        <p className="text-sm font-medium text-foreground">
          c0wrk {formatVersion(info.latest_version)} is available
        </p>
        <p className="text-xs text-muted-foreground">
          {currentVersion ? `you're on ${formatVersion(currentVersion)}` : 'a new update is available'}
        </p>
      </div>
      <div className="flex items-center gap-2">
        <Button size="sm" onClick={handleUpdate} disabled={isDownloading}>
          <Download className="size-4" />
          Update
        </Button>
        <Button size="sm" variant="ghost" onClick={handleSkip}>
          Skip
        </Button>
        <Button size="sm" variant="ghost" onClick={handleDismiss}>
          Later
        </Button>
      </div>
    </InlineSurface>
  )
}

function InlineDownloadingSurface() {
  return (
    <InlineSurface icon={<Download className="size-5 text-info" />} accent="info">
      <p className="text-sm font-medium text-foreground">Downloading update…</p>
      <DownloadProgressBar />
    </InlineSurface>
  )
}

function InlineDownloadedSurface() {
  const { handleRestart, handleDismiss } = useUpdateActions()

  return (
    <InlineSurface icon={<RefreshCw className="size-5 text-success" />} accent="success">
      <div>
        <p className="text-sm font-medium text-foreground">Update ready</p>
        <p className="text-xs text-muted-foreground">Restart to install?</p>
      </div>
      <div className="flex items-center gap-2">
        <Button size="sm" onClick={handleRestart}>
          <RefreshCw className="size-4" />
          Restart
        </Button>
        <Button size="sm" variant="ghost" onClick={handleDismiss}>
          Later
        </Button>
      </div>
    </InlineSurface>
  )
}

function InlineErrorSurface() {
  const errorMessage = useUpdateError()
  const { handleDismiss } = useUpdateActions()

  return (
    <InlineSurface icon={<AlertCircle className="size-5 text-destructive" />} accent="destructive">
      <p className="text-sm font-medium text-foreground">Update failed</p>
      <p className="text-xs text-muted-foreground">{errorMessage ?? 'An error occurred'}</p>
      <div className="flex items-center gap-2">
        <Button size="sm" variant="ghost" onClick={handleDismiss}>
          Dismiss
        </Button>
      </div>
    </InlineSurface>
  )
}

/** A labelled on/off toggle using the same sr-only-peer pattern as ProxySettings. */
function ToggleRow({
  label,
  checked,
  disabled,
  onChange,
}: {
  label: string
  checked: boolean
  disabled: boolean
  onChange: (value: boolean) => void
}) {
  return (
    <div className="flex items-center gap-3">
      <label className={`relative inline-flex items-center ${disabled ? 'cursor-not-allowed opacity-50' : 'cursor-pointer'}`}>
        <input
          type="checkbox"
          checked={checked}
          disabled={disabled}
          onChange={(e) => onChange(e.target.checked)}
          className="sr-only peer"
        />
        <div className="w-9 h-5 bg-muted rounded-full peer peer-checked:bg-primary peer-disabled:opacity-50 transition-colors after:content-[''] after:absolute after:top-0.5 after:start-[2px] after:bg-background after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-full" />
      </label>
      <span className="text-sm text-foreground">{label}</span>
    </div>
  )
}
