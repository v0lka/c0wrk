// Embedded LLM section of the Settings "LLM" tab.
//
// The whole lifecycle of the pinned local model (see
// specs/domains/embedded-llm.md § Settings block) behind one block, mounted as
// the FIRST block of LLMSettings' provider section — directly under the Default
// Model field and above "+ Add compatible provider". The block is a
// collapsed-by-default collapsible shell styled after the provider accordions
// (the ProviderAccordion header anatomy); an install in flight expands it, so
// the progress surface and its Cancel button are never hidden behind the
// header:
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
//                   snapshot through `embedded_llm:state` and returns the block
//                   to the Install button (partial bytes are kept, so a
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
// the destructive-action confirmation flag and the shell's expanded flag.
//
// Zoom-safety: no viewport units, no `*-screen` utility, no pointer-anchored
// placement — the block flows in the settings column and the dialog is a Radix
// modal, both of which are scale-agnostic.

import { useEffect, useState } from 'react'
import { AlertCircle, ChevronDown, ChevronRight, Cpu } from 'lucide-react'
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
import { EmbeddedLLMDeviceMemory } from './embedded/EmbeddedLLMDeviceMemory'
import { EmbeddedLLMNotice } from './embedded/EmbeddedLLMNotice'
import { EmbeddedLLMActions, EmbeddedLLMInstallAction, EmbeddedLLMCleanupAction } from './embedded/EmbeddedLLMActions'
import { EmbeddedLLMAutoUnload } from './embedded/EmbeddedLLMAutoUnload'
import { EmbeddedLLMTuning } from './embedded/EmbeddedLLMTuning'
import { EmbeddedLLMAdvancedTuning } from './embedded/EmbeddedLLMAdvancedTuning'
import { EmbeddedLLMRemoveDialog } from './embedded/EmbeddedLLMRemoveDialog'
import type { EmbeddedLLMRemoveScope } from '@/api/embedded'
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

  // Pure UI state: the destructive-action confirmation and the scope the
  // dialog is about to confirm.
  const [confirmRemove, setConfirmRemove] = useState(false)
  const [removeScope, setRemoveScope] = useState<EmbeddedLLMRemoveScope>('all')
  const autoUnload = useEmbeddedLLMAutoUnload()
  const tuning = useEmbeddedLLMTuning()

  // The collapsible shell (the provider accordions' ProviderAccordion
  // pattern): collapsed by default — the block is an advanced, gated surface.
  // An install in flight always reveals itself: the effect also fires on
  // mount-while-installing (the status snapshot answers after the first
  // render), so reopening the dialog mid-download exposes the progress
  // surface and its Cancel button without a click.
  const [expanded, setExpanded] = useState(false)
  useEffect(() => {
    if (installing) setExpanded(true)
  }, [installing])

  // --- Derived view state (never stored) ---------------------------------
  const installed = status?.installed ?? false
  const loaded = status?.loaded ?? false
  const loading = status?.loading ?? false
  const unavailable = status !== null && !status.available
  const idleSeconds = status?.idle_remaining_seconds ?? 0
  // The artifact groups on disk right now. An installed model has all three
  // (the manifest describes them); a not-installed machine reports whatever a
  // scoped removal left behind. The projection scope is the exception: under
  // the mtplx engine there is no separately removable vision projector — the
  // backend refuses the scope with ErrNoProjectionOnMLX, but only AFTER it has
  // stopped the model and cleared the install record — so the menu entry
  // self-disables instead of leading the operator into that path (ADR-092 D7),
  // and the refusal text explains why. The LEADING SPACE matters: the string
  // is interpolated into `Remove vision projector only{…}` in
  // EmbeddedLLMActions, and JSX strips the whitespace between a text child and
  // an expression — without it the menu entry renders
  // "…onlynot removable under the MTPLX engine".
  const projectionRefusal =
    status?.engine === 'mtplx' ? ' not removable under the MTPLX engine' : undefined
  const leftovers = {
    runtime: installed || (status?.leftover_runtime ?? false),
    weights: installed || (status?.leftover_weights ?? false),
    projection:
      status?.engine !== 'mtplx' && (installed || (status?.leftover_projection ?? false)),
    projectionRefusal,
  }
  const anyLeftover = leftovers.runtime || leftovers.weights || leftovers.projection
  // Error precedence: the action the operator just took (its rejected promise
  // is the report), then the last FATAL install failure (the backend retries
  // resumable download failures silently, so this line only ever carries the
  // fatal, operator-friendly text), then the supervisor's own error state.
  const error = actionError
    ?? (status && status.install_error !== '' ? status.install_error : null)
    ?? (status && status.error !== '' ? status.error : null)

  /** Opens the confirmation dialog for the chosen scope. */
  const requestRemove = (scope: EmbeddedLLMRemoveScope) => {
    setRemoveScope(scope)
    setConfirmRemove(true)
  }

  return (
    <div className="flex flex-col rounded-lg border" data-testid="embedded-llm-settings">
      {/* Collapsible header — the provider accordions' header anatomy: a
          full-width button with the chevron + label on the left. */}
      <button
        type="button"
        title="Embedded LLM"
        aria-expanded={expanded}
        data-testid="embedded-llm-toggle"
        className="flex w-full items-center justify-between px-4 py-3 text-sm font-medium hover:bg-muted/50"
        onClick={() => setExpanded((v) => !v)}
      >
        <span className="flex items-center gap-2">
          {expanded ? (
            <ChevronDown className="h-4 w-4" />
          ) : (
            <ChevronRight className="h-4 w-4" />
          )}
          <Cpu className="h-4 w-4 text-muted-foreground" />
          Embedded LLM
        </span>
      </button>

      {expanded && (
        <div className="flex flex-col gap-3 border-t px-4 py-4">
          <p className="text-xs text-muted-foreground">
            Run the pinned Bonsai 2 27B model entirely on this machine: c0wrk downloads a pinned
            inference runtime and the weights, supervises llama-server on 127.0.0.1 and registers it as
            the provider "embedded". Needs about 8 GiB of disk, plus enough memory — accelerator or
            system RAM — for the launch shape the planner resolves; the download is resumable and
            nothing is loaded until you ask for it.
          </p>

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
              <EmbeddedLLMNotice />

              <EmbeddedLLMInstallRecord status={status} />

              <EmbeddedLLMDeviceMemory installed={installed} />

              <EmbeddedLLMActions
                loaded={loaded}
                loading={loading}
                busy={busy}
                onLoad={lifecycle.load}
                onUnload={lifecycle.unload}
                onRemove={requestRemove}
                leftovers={leftovers}
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
            <>
              <EmbeddedLLMInstallAction
                busy={busy}
                unavailable={unavailable}
                statusLoading={statusLoading}
                onInstall={lifecycle.install}
              />
              {anyLeftover && (
                <EmbeddedLLMCleanupAction busy={busy} leftovers={leftovers} onRemove={requestRemove} />
              )}
            </>
          )}
        </div>
      )}

      <EmbeddedLLMRemoveDialog
        open={confirmRemove}
        busy={busy === 'remove'}
        scope={removeScope}
        onCancel={() => setConfirmRemove(false)}
        onConfirm={() => {
          setConfirmRemove(false)
          lifecycle.remove(removeScope)
        }}
      />
    </div>
  )
}
