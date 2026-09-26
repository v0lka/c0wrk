// Embedded LLM section of the Settings "LLM" tab.
//
// The whole lifecycle of the pinned local model (see
// specs/domains/embedded-llm.md § Settings block) behind one block, mounted as
// the FIRST block of LLMSettings' provider section — directly under the Default
// Model field and above "+ Add compatible provider":
//
//   not installed → ONE Install button. The RPC runs only the synchronous
//                   gates (single-run, hardware probe, the 16 GiB refusal), so
//                   a refusal arrives as a rejected promise and is shown inline
//                   — never as a toast (the backend raises toasts only for
//                   BACKGROUND failures).
//   installing    → an inline progress bar PER COMPONENT (runtime, cudart when
//                   this machine gets one, model, mmproj) — see
//                   embedded/EmbeddedLLMProgress.
//   installed     → the INFORMATIONAL install record (the packing actually
//                   resolved — PQ2_0 | PTQ1_0 — plus the effective backend,
//                   context, the MEASURED device topology and the effective
//                   plan with its planner notes), Load/Unload per the current
//                   state, and Remove behind a confirmation dialog (it rolls
//                   the whole install back), plus the auto-unload budget and
//                   the memory-plan tuning controls (three primary + a
//                   collapsed Advanced section; the commit flows live in
//                   useEmbeddedLLMAutoUnload / useEmbeddedLLMTuning).
//
// State comes from embeddedLLMStore, which owns the authoritative
// GetEmbeddedLLMStatus snapshot and is fed by the two `embedded_llm:*` global
// events; this component holds NO copy of it — only the two pieces of pure UI
// state (the confirmation dialog and the minutes draft).
//
// Zoom-safety: no viewport units, no `*-screen` utility, no pointer-anchored
// placement — the block flows in the settings column and the dialog is a Radix
// modal, both of which are scale-agnostic.

import { useCallback, useEffect, useState } from 'react'
import { AlertCircle, Download, Loader2, Play, PowerOff, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { logger } from '@/lib/logger'
import {
  installEmbeddedLLM,
  loadEmbeddedLLM,
  removeEmbeddedLLM,
  unloadEmbeddedLLM,
} from '@/api/embedded'
import {
  refreshEmbeddedLLMStatus,
  subscribeEmbeddedLLMEvents,
  useEmbeddedLLMBusy,
  useEmbeddedLLMError,
  useEmbeddedLLMInstalling,
  useEmbeddedLLMProgress,
  useEmbeddedLLMStatus,
  useEmbeddedLLMStatusLoading,
  useEmbeddedLLMStore,
  type EmbeddedLLMBusyAction,
} from '@/stores/embeddedLLMStore'
import { EmbeddedLLMProgress } from './embedded/EmbeddedLLMProgress'
import { EmbeddedLLMInstallRecord } from './embedded/EmbeddedLLMInstallRecord'
import { EmbeddedLLMAutoUnload } from './embedded/EmbeddedLLMAutoUnload'
import { EmbeddedLLMTuning } from './embedded/EmbeddedLLMTuning'
import { EmbeddedLLMAdvancedTuning } from './embedded/EmbeddedLLMAdvancedTuning'
import { EmbeddedLLMRemoveDialog } from './embedded/EmbeddedLLMRemoveDialog'
import { useEmbeddedLLMAutoUnload } from '@/hooks/useEmbeddedLLMAutoUnload'
import { useEmbeddedLLMTuning } from '@/hooks/useEmbeddedLLMTuning'

/** Wails rejects with a Go error string, so the message IS the report. */
function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/** Compact idle-remaining readout (`41m 40s`, `1h 05m`). */
function formatIdle(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  if (s >= 3600) {
    const h = Math.floor(s / 3600)
    const m = Math.floor((s % 3600) / 60)
    return `${h}h ${String(m).padStart(2, '0')}m`
  }
  return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`
}

export function EmbeddedLLMSettings() {
  const status = useEmbeddedLLMStatus()
  const installing = useEmbeddedLLMInstalling()
  const progress = useEmbeddedLLMProgress()
  const busy = useEmbeddedLLMBusy()
  const actionError = useEmbeddedLLMError()
  const statusLoading = useEmbeddedLLMStatusLoading()
  const beginInstall = useEmbeddedLLMStore((s) => s.beginInstall)
  const setBusy = useEmbeddedLLMStore((s) => s.setBusy)
  const setError = useEmbeddedLLMStore((s) => s.setError)

  // Pure UI state: the destructive-action confirmation. The minutes draft and
  // the tuning values/commits live in their extracted hooks
  // (useEmbeddedLLMAutoUnload / useEmbeddedLLMTuning) so this file keeps its
  // baseline length.
  const [confirmRemove, setConfirmRemove] = useState(false)
  const autoUnload = useEmbeddedLLMAutoUnload()
  const tuning = useEmbeddedLLMTuning()

  // Lifecycle: ONE shared event subscription (refcounted in the store, so the
  // status-bar indicator mounting alongside this block does not double-apply an
  // event) plus the initial authoritative read. Nothing here installs or loads
  // anything — both are explicit user clicks only.
  useEffect(() => {
    const unsubscribe = subscribeEmbeddedLLMEvents()
    void refreshEmbeddedLLMStatus()
    return unsubscribe
  }, [])

  // --- Derived view state (never stored) ---------------------------------
  const installed = status?.installed ?? false
  const loaded = status?.loaded ?? false
  const loading = status?.loading ?? false
  const unavailable = status !== null && !status.available
  const idleSeconds = status?.idle_remaining_seconds ?? 0
  const error = actionError ?? (status && status.error !== '' ? status.error : null)
  const busyNow = busy !== null

  // --- Actions -----------------------------------------------------------
  const runAction = useCallback(
    async (action: EmbeddedLLMBusyAction, rpc: () => Promise<void>) => {
      setBusy(action)
      setError(null)
      try {
        await rpc()
      } catch (err) {
        logger.warn(`[embedded-llm] ${action} failed`, err)
        setError(errorMessage(err))
      } finally {
        setBusy(null)
      }
      // Read back in BOTH cases: a refusal (an install in flight, a stop that
      // did not take) is also a state this block has to show honestly.
      await refreshEmbeddedLLMStatus()
    },
    [setBusy, setError],
  )

  const handleInstall = useCallback(async () => {
    setBusy('install')
    setError(null)
    try {
      // Resolving means the gates passed: the multi-gigabyte run is now in the
      // background on the app context, reported through the progress events.
      await installEmbeddedLLM()
      beginInstall()
    } catch (err) {
      logger.warn('[embedded-llm] install refused', err)
      setError(errorMessage(err))
    } finally {
      setBusy(null)
    }
    await refreshEmbeddedLLMStatus()
  }, [beginInstall, setBusy, setError])

  const handleRemove = useCallback(() => {
    setConfirmRemove(false)
    void runAction('remove', removeEmbeddedLLM)
  }, [runAction])

  return (
    <div className="flex flex-col gap-3" data-testid="embedded-llm-settings">
      <div className="flex flex-col gap-1">
        <h3 className="text-sm font-medium">Embedded LLM</h3>
        <p className="text-xs text-muted-foreground">
          Run the pinned Bonsai 2 27B model entirely on this machine: c0wrk downloads a pinned
          inference runtime and the weights, supervises llama-server on 127.0.0.1 and registers it
          as the provider "embedded". Needs 16 GiB of RAM and about 8 GiB of disk; the download is
          resumable and nothing is loaded until you ask for it.
        </p>
      </div>

      {error && (
        <p
          className="flex items-start gap-1.5 text-xs text-destructive"
          role="alert"
          data-testid="embedded-llm-error"
        >
          <AlertCircle className="mt-0.5 size-3.5 shrink-0" />
          <span>{error}</span>
        </p>
      )}

      {unavailable && (
        <p className="text-xs text-muted-foreground">
          The embedded-model subsystem is not available yet — the backend has not finished starting.
        </p>
      )}

      {installing ? (
        <EmbeddedLLMProgress progress={progress} />
      ) : installed && status ? (
        <>
          <EmbeddedLLMInstallRecord status={status} />

          <div className="flex flex-wrap items-center gap-2">
            {loaded ? (
              <Button
                size="sm"
                variant="outline"
                className="gap-2"
                onClick={() => void runAction('unload', unloadEmbeddedLLM)}
                disabled={busyNow}
                data-testid="embedded-llm-unload"
              >
                {busy === 'unload' ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : (
                  <PowerOff className="size-4" />
                )}
                {busy === 'unload' ? 'Unloading…' : 'Unload'}
              </Button>
            ) : (
              <Button
                size="sm"
                variant="outline"
                className="gap-2"
                onClick={() => void runAction('load', loadEmbeddedLLM)}
                disabled={busyNow || loading}
                data-testid="embedded-llm-load"
              >
                {busy === 'load' || loading ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : (
                  <Play className="size-4" />
                )}
                {busy === 'load' || loading ? 'Loading weights…' : 'Load'}
              </Button>
            )}

            <Button
              size="sm"
              variant="ghost"
              className="gap-2 text-destructive enabled:hover:bg-destructive/10 enabled:hover:text-destructive"
              onClick={() => setConfirmRemove(true)}
              disabled={busyNow}
              data-testid="embedded-llm-remove"
            >
              <Trash2 className="size-4" />
              Remove
            </Button>
          </div>

          <EmbeddedLLMAutoUnload {...autoUnload} />

          <EmbeddedLLMTuning {...tuning.primary} />

          <EmbeddedLLMAdvancedTuning {...tuning.advanced} />

          {loaded && autoUnload.enabled && idleSeconds > 0 && (
            <p className="text-xs text-muted-foreground" data-testid="embedded-llm-idle">
              Unloads after {formatIdle(idleSeconds)} of idle time.
            </p>
          )}
        </>
      ) : (
        <div className="flex flex-col gap-2">
          <Button
            size="sm"
            variant="outline"
            className="self-start gap-2"
            onClick={() => void handleInstall()}
            disabled={busyNow || unavailable}
            data-testid="embedded-llm-install"
          >
            {busy === 'install' ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <Download className="size-4" />
            )}
            {busy === 'install' ? 'Checking hardware…' : 'Install'}
          </Button>
          <p className="text-xs text-muted-foreground">
            {statusLoading
              ? 'Reading the installed state…'
              : 'Refused up front when this machine has less than 16 GiB of RAM.'}
          </p>
        </div>
      )}

      <EmbeddedLLMRemoveDialog
        open={confirmRemove}
        busy={busy === 'remove'}
        onCancel={() => setConfirmRemove(false)}
        onConfirm={handleRemove}
      />
    </div>
  )
}
