// Embedded LLM section of the Settings "LLM" tab.
//
// The whole lifecycle of the pinned local model (see
// specs/domains/embedded-llm.md § Settings block) behind one block, mounted as
// the FIRST block of LLMSettings' provider section — directly under the Default
// Model field and above "+ Add compatible provider":
//
//   not installed → ONE Install button (embedded/EmbeddedLLMInstallAction). The
//                   RPC runs only the synchronous gates — single-run, the
//                   bounded hardware probe, and the COMBINED memory gate
//                   (`embeddedllm.CheckMemoryBudget` prices BOTH pools, the
//                   accelerator's and system RAM; there is no flat RAM floor) —
//                   so a refusal arrives as a rejected promise and is shown
//                   inline, never as a toast (the backend toasts only
//                   BACKGROUND failures).
//   installing    → an inline progress bar PER COMPONENT (runtime, cudart when
//                   this machine gets one, model, mmproj) with a Cancel button
//                   that asks the run to stop; its quiet end re-reads the
//                   snapshot through `embedded_llm:state` and returns the
//                   block to the Install button (partial bytes are kept, so a
//                   retry resumes instead of restarting).
//   installed     → the INFORMATIONAL install record (the packing resolved, the
//                   effective backend, context, the MEASURED topology and the
//                   effective plan with its notes), the Load/Unload/Remove row,
//                   the auto-unload budget and the tuning controls.
//
// This file is the composition root only: state comes from embeddedLLMStore
// (the authoritative GetEmbeddedLLMStatus snapshot, fed by the two
// `embedded_llm:*` global events), the mutating actions from
// useEmbeddedLLMLifecycle, and the commit flows — with their drafts — from
// useEmbeddedLLMAutoUnload / useEmbeddedLLMTuning. The only state held here is
// the destructive-action confirmation flag.
//
// Zoom-safety: no viewport units, no `*-screen` utility, no pointer-anchored
// placement — the block flows in the settings column and the dialog is a Radix
// modal, both of which are scale-agnostic.

import { useState } from 'react'
import { AlertCircle } from 'lucide-react'
import { formatIdleCountdown } from '@/lib/formatters'
import {
  useEmbeddedLLMBusy,
  useEmbeddedLLMError,
  useEmbeddedLLMInstalling,
  useEmbeddedLLMProgress,
  useEmbeddedLLMStatus,
  useEmbeddedLLMStatusLoading,
} from '@/stores/embeddedLLMStore'
import { EmbeddedLLMProgress } from './embedded/EmbeddedLLMProgress'
import { EmbeddedLLMInstallRecord } from './embedded/EmbeddedLLMInstallRecord'
import { EmbeddedLLMActions, EmbeddedLLMInstallAction } from './embedded/EmbeddedLLMActions'
import { EmbeddedLLMAutoUnload } from './embedded/EmbeddedLLMAutoUnload'
import { EmbeddedLLMTuning } from './embedded/EmbeddedLLMTuning'
import { EmbeddedLLMAdvancedTuning } from './embedded/EmbeddedLLMAdvancedTuning'
import { EmbeddedLLMRemoveDialog } from './embedded/EmbeddedLLMRemoveDialog'
import { useEmbeddedLLMAutoUnload } from '@/hooks/useEmbeddedLLMAutoUnload'
import { useEmbeddedLLMLifecycle } from '@/hooks/useEmbeddedLLMLifecycle'
import { useEmbeddedLLMTuning } from '@/hooks/useEmbeddedLLMTuning'

export function EmbeddedLLMSettings() {
  const status = useEmbeddedLLMStatus()
  const installing = useEmbeddedLLMInstalling()
  const progress = useEmbeddedLLMProgress()
  const busy = useEmbeddedLLMBusy()
  const actionError = useEmbeddedLLMError()
  const statusLoading = useEmbeddedLLMStatusLoading()
  const lifecycle = useEmbeddedLLMLifecycle()

  // Pure UI state: the destructive-action confirmation.
  const [confirmRemove, setConfirmRemove] = useState(false)
  const autoUnload = useEmbeddedLLMAutoUnload()
  const tuning = useEmbeddedLLMTuning()

  // --- Derived view state (never stored) ---------------------------------
  const installed = status?.installed ?? false
  const loaded = status?.loaded ?? false
  const loading = status?.loading ?? false
  const unavailable = status !== null && !status.available
  const idleSeconds = status?.idle_remaining_seconds ?? 0
  // Error precedence: the action the operator just took (its rejected promise
  // is the report), then the last FATAL install failure (the backend retries
  // resumable download failures silently, so this line only ever carries the
  // fatal, operator-friendly text), then the supervisor's own error state.
  const error = actionError
    ?? (status && status.install_error !== '' ? status.install_error : null)
    ?? (status && status.error !== '' ? status.error : null)

  return (
    <div className="flex flex-col gap-3" data-testid="embedded-llm-settings">
      <div className="flex flex-col gap-1">
        <h3 className="text-sm font-medium">Embedded LLM</h3>
        <p className="text-xs text-muted-foreground">
          Run the pinned Bonsai 2 27B model entirely on this machine: c0wrk downloads a pinned
          inference runtime and the weights, supervises llama-server on 127.0.0.1 and registers it as
          the provider "embedded". Needs about 8 GiB of disk, plus enough memory — accelerator or
          system RAM — for the launch shape the planner resolves; the download is resumable and
          nothing is loaded until you ask for it.
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
        <EmbeddedLLMProgress progress={progress} busy={busy} onCancel={lifecycle.cancelInstall} />
      ) : installed && status ? (
        <>
          <EmbeddedLLMInstallRecord status={status} />

          <EmbeddedLLMActions
            loaded={loaded}
            loading={loading}
            busy={busy}
            onLoad={lifecycle.load}
            onUnload={lifecycle.unload}
            onRemove={() => setConfirmRemove(true)}
          />

          <EmbeddedLLMAutoUnload {...autoUnload} />

          <EmbeddedLLMTuning {...tuning.primary} />

          <EmbeddedLLMAdvancedTuning {...tuning.advanced} />

          {loaded && autoUnload.enabled && idleSeconds > 0 && (
            <p className="text-xs text-muted-foreground" data-testid="embedded-llm-idle">
              Unloads after {formatIdleCountdown(idleSeconds)} of idle time.
            </p>
          )}
        </>
      ) : (
        <EmbeddedLLMInstallAction
          busy={busy}
          unavailable={unavailable}
          statusLoading={statusLoading}
          onInstall={lifecycle.install}
        />
      )}

      <EmbeddedLLMRemoveDialog
        open={confirmRemove}
        busy={busy === 'remove'}
        onCancel={() => setConfirmRemove(false)}
        onConfirm={() => {
          setConfirmRemove(false)
          lifecycle.remove()
        }}
      />
    </div>
  )
}
