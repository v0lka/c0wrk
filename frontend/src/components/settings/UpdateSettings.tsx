// Updates section for the Settings "about" tab.
//
// Shows the running build version, the auto-check toggle (persisted to
// config.yaml updates.auto_check), and a
// "Check for updates" button that triggers an explicit check. The check result
// flows back through the global update:available / update:none / update:error
// events, which updateStore (and thus the UpdateToast) already consume — so
// this component only needs to surface the in-flight spinner and the most
// recent textual outcome of the explicit check. The toast is the primary
// notification surface.
//
// The operator-level master gate (config.yaml updates.enabled) is the sole
// enable/disable switch for the whole subsystem. It is reflected via the
// operator_enabled flag: when an administrator has disabled updates, the
// auto-check toggle is disabled and a notice is shown.

import { useCallback, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { RefreshCw, Check, AlertCircle, ShieldAlert } from 'lucide-react'
import { checkForUpdates, getUpdateSettings, setUpdateSettings } from '@/api/updater'
import type { UpdateSettings as UpdateSettingsData } from '@/api/updater'
import {
  useCurrentVersion,
  useUpdateChecking,
  useUpdateStore,
} from '@/stores/updateStore'
import { logger } from '@/lib/logger'
import { formatVersion } from '@/lib/formatters'

type CheckOutcome = 'idle' | 'up-to-date' | 'error'

export function UpdateSettings() {
  const currentVersion = useCurrentVersion()
  const isChecking = useUpdateChecking()
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

  // When an explicit check surfaces an available update, the toast takes over;
  // reset the local outcome so stale "up to date" text doesn't linger.
  const phase = useUpdateStore((s) => s.phase)
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
          <Button
            size="sm"
            variant="outline"
            onClick={handleCheck}
            disabled={isChecking || operatorDisabled}
          >
            <RefreshCw className={`size-4 ${isChecking ? 'animate-spin' : ''}`} />
            Check for updates
          </Button>
        </div>

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

        {outcome === 'up-to-date' && (
          <p className="flex items-center gap-1.5 text-xs text-success">
            <Check className="size-3.5" />
            You're running the latest version
          </p>
        )}
        {outcome === 'error' && (
          <p className="flex items-center gap-1.5 text-xs text-destructive">
            <AlertCircle className="size-3.5" />
            Failed to check for updates
          </p>
        )}
      </div>
    </div>
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
