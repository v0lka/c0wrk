// The action buttons of the Embedded LLM settings block, extracted from
// EmbeddedLLMSettings so that file stays at its baseline:
//
//   EmbeddedLLMActions        — the Load/Unload + Remove row of an INSTALLED
//                               model (Remove opens the parent's confirmation
//                               dialog; nothing destructive runs here).
//   EmbeddedLLMInstallAction  — the single Install action of a machine that has
//                               no install yet, with the refusal-policy line
//                               under it.
//
// Both are fully controlled leaves: the busy window, the RPCs, the error line
// and the dialog state all live in the parent. `busy` is passed through rather
// than a plain boolean so each button can label and spinner ONLY its own
// action, and `statusLoading` is the store's FIRST-read flag — it is never
// re-raised by a later re-read, so the helper text below cannot blink when an
// `embedded_llm:state` event invalidates the snapshot.

import { Download, Loader2, Play, PowerOff, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { EmbeddedLLMBusyAction } from '@/stores/embeddedLLMStore'

export interface EmbeddedLLMActionsProps {
  /** The model is resident, so the row offers Unload instead of Load. */
  loaded: boolean
  /** A weight load is in flight on the backend — including one this UI did not
   *  start (a request-path cold load), which is why it is not derived from
   *  `busy`. */
  loading: boolean
  busy: EmbeddedLLMBusyAction | null
  onLoad: () => void
  onUnload: () => void
  /** Opens the confirmation dialog; the parent runs the RPC. */
  onRemove: () => void
}

export function EmbeddedLLMActions({
  loaded,
  loading,
  busy,
  onLoad,
  onUnload,
  onRemove,
}: EmbeddedLLMActionsProps) {
  // Every action button stays disabled for the whole busy window, which the
  // parent holds open until the post-action read-back has landed.
  const busyNow = busy !== null
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid="embedded-llm-actions">
      {loaded ? (
        <Button
          size="sm"
          variant="outline"
          className="gap-2"
          onClick={onUnload}
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
          onClick={onLoad}
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
        onClick={onRemove}
        disabled={busyNow}
        data-testid="embedded-llm-remove"
      >
        <Trash2 className="size-4" />
        Remove
      </Button>
    </div>
  )
}

export interface EmbeddedLLMInstallActionProps {
  busy: EmbeddedLLMBusyAction | null
  /** The subsystem could not be constructed yet (the backend is still
   *  starting), so an install cannot be gated or run. */
  unavailable: boolean
  /** The first status read is in flight. */
  statusLoading: boolean
  onInstall: () => void
}

export function EmbeddedLLMInstallAction({
  busy,
  unavailable,
  statusLoading,
  onInstall,
}: EmbeddedLLMInstallActionProps) {
  return (
    <div className="flex flex-col gap-2">
      <Button
        size="sm"
        variant="outline"
        className="self-start gap-2"
        onClick={onInstall}
        disabled={busy !== null || unavailable}
        data-testid="embedded-llm-install"
      >
        {busy === 'install' ? (
          <Loader2 className="size-4 animate-spin" />
        ) : (
          <Download className="size-4" />
        )}
        {busy === 'install' ? 'Checking hardware…' : 'Install'}
      </Button>
      <p className="text-xs text-muted-foreground" data-testid="embedded-llm-install-hint">
        {statusLoading
          ? 'Reading the installed state…'
          : 'Refused up front when neither this machine’s accelerator memory nor its system RAM can hold the smallest launch shape.'}
      </p>
    </div>
  )
}
