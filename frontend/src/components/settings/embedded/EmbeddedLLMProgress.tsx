// Per-component install progress of the embedded local model.
//
// One row PER ARTIFACT, modelled on ToolInstallSplash's ToolProgressRow: icon,
// name, stage, the component's OWN bytes, a bar and a percent. The transport
// never aggregates the components and neither does this view — a runtime
// archive, the paired Windows cudart archive, the weights and the vision
// projector each keep their own bar (see specs/contracts/event-catalog.md,
// `embedded_llm:install_progress`).
//
// A component that never reported gets NO row: `cudart` is fetched only on
// Windows CUDA, so inventing a pending row for it would promise an artifact
// this install will never download.
//
// The header's Cancel button is fully controlled (`busy` + `onCancel` from the
// parent): it only DELIVERS the stop request — the run's asynchronous, quiet
// end arrives through `embedded_llm:state`.
//
// The component/stage WORDS (and the render order) are shared with the
// status-bar indicator through `lib/embeddedLLMLabels`, so the two surfaces
// describing the same payload cannot drift apart.

import { useMemo } from 'react'
import { Download, Loader2, PackageOpen, Square } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { formatBytes } from '@/lib/formatters'
import {
  EMBEDDED_COMPONENT_ORDER,
  embeddedComponentLabel,
  embeddedStageLabel,
} from '@/lib/embeddedLLMLabels'
import type { EmbeddedLLMBusyAction, EmbeddedLLMProgressByComponent } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMComponent, EmbeddedLLMInstallProgressData } from '@/types/events'

function pct(done: number, total: number): number {
  if (total <= 0) return 0
  return Math.min(100, Math.round((done / total) * 100))
}

/** One artifact's row. A stage that carries no byte count (verifying,
 *  extracting, signing) shows none and renders an indeterminate pulse, because
 *  a percentage there would be a lie. */
export function EmbeddedProgressRow({
  component,
  progress,
}: {
  component: EmbeddedLLMComponent
  progress: EmbeddedLLMInstallProgressData
}) {
  const known = progress.bytes_total > 0
  const done = progress.stage === 'done' || (known && progress.bytes_done >= progress.bytes_total)
  const active = !done && known
  const percent = pct(progress.bytes_done, progress.bytes_total)

  return (
    <div className="flex items-center gap-3 py-1" data-testid={`embedded-progress-${component}`}>
      {done ? (
        <PackageOpen className="size-4 shrink-0 text-success" />
      ) : (
        <Download className="size-4 shrink-0 animate-pulse text-primary" />
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-sm text-foreground">{embeddedComponentLabel(component)}</span>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span>{embeddedStageLabel(progress.stage)}</span>
          {known && (
            <span className="tabular-nums">
              {formatBytes(progress.bytes_done)} / {formatBytes(progress.bytes_total)}
            </span>
          )}
          {!known && !done && <span>…</span>}
        </div>
      </div>

      <div className="flex w-28 shrink-0 items-center gap-2">
        {active && (
          <>
            <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-primary transition-all duration-150"
                style={{ width: `${percent}%` }}
              />
            </div>
            <span className="w-9 text-right text-xs tabular-nums text-muted-foreground">
              {percent}%
            </span>
          </>
        )}
        {done && <span className="ml-auto text-xs text-success">Done</span>}
        {!done && !active && (
          <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
            <div className="h-full w-1/3 animate-pulse rounded-full bg-primary" />
          </div>
        )}
      </div>
    </div>
  )
}

export interface EmbeddedLLMProgressProps {
  /** The per-component progress map. */
  progress: EmbeddedLLMProgressByComponent
  /** The store's busy window, passed through (not a plain boolean) so the
   *  Cancel button can spinner ONLY its own action. During the download itself
   *  the window is idle — the install RPC resolved long ago — so Cancel stays
   *  clickable exactly when there is something to stop. */
  busy: EmbeddedLLMBusyAction | null
  /** Asks the in-flight run to stop (the parent's lifecycle.cancelInstall). */
  onCancel: () => void
}

/** The whole installing surface: the header with its Cancel action, one row per
 *  reporting component and the "closing this does not interrupt it" note (the
 *  run lives on the app context, not on this dialog). */
export function EmbeddedLLMProgress({ progress, busy, onCancel }: EmbeddedLLMProgressProps) {
  // Derived here (not in a selector): the map reference is stable, so this
  // rebuilds only when a component actually reported.
  const rows = useMemo(
    () =>
      EMBEDDED_COMPONENT_ORDER.flatMap((component) => {
        const entry = progress[component]
        return entry ? [{ component, entry }] : []
      }),
    [progress],
  )

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border bg-muted/30 p-3">
      <div className="flex items-center justify-between gap-2">
        <p className="flex items-center gap-2 text-sm text-foreground">
          <Loader2 className="size-4 shrink-0 animate-spin text-primary" />
          Installing the embedded model…
        </p>
        {/* The stop request is asynchronous and idempotent, so the button stays
            disabled for the whole busy window and the run's quiet end arrives
            later through `embedded_llm:state`, which re-reads the snapshot and
            drops `installing`. */}
        <Button
          size="xs"
          variant="outline"
          className="gap-1.5"
          onClick={onCancel}
          disabled={busy !== null}
          data-testid="embedded-llm-cancel-install"
        >
          {busy === 'cancel' ? (
            <Loader2 className="size-3 animate-spin" />
          ) : (
            <Square className="size-3" />
          )}
          {busy === 'cancel' ? 'Cancelling…' : 'Cancel'}
        </Button>
      </div>
      <div className="flex flex-col" data-testid="embedded-llm-progress">
        {rows.map(({ component, entry }) => (
          <EmbeddedProgressRow key={component} component={component} progress={entry} />
        ))}
        {rows.length === 0 && (
          <p className="py-1 text-xs text-muted-foreground">Preparing the download…</p>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        Verified artifacts are reused on a retry. Closing this dialog does not interrupt the
        install.
      </p>
    </div>
  )
}
