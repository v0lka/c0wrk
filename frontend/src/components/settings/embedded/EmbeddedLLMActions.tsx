// The action buttons of the Embedded LLM settings block, extracted from
// EmbeddedLLMSettings so that file stays at its baseline:
//
//   EmbeddedLLMActions        — the Load/Unload + Remove row of an INSTALLED
//                               model (Remove opens the parent's confirmation
//                               dialog with the chosen scope; nothing
//                               destructive runs here).
//   EmbeddedLLMInstallAction  — the single Install action of a machine that has
//                               no install yet, with the refusal-policy line
//                               under it.
//   EmbeddedLLMCleanupAction  — the Remove button of a NOT-installed machine
//                               with leftover bytes on disk (a scoped removal's
//                               cache), with the residue named.
//
// All are fully controlled leaves: the busy window, the RPCs, the error line
// and the dialog state all live in the parent. `busy` is passed through rather
// than a plain boolean so each button can label and spinner ONLY its own
// action.
//
// The Remove control is a SPLIT BUTTON: the main click means "remove
// everything" (the historical behaviour), and the attached dropdown offers the
// three scoped removals — runtime / weights / vision projector. A scope whose
// bytes are not on disk renders disabled. Every scope still clears the install
// record and the config (a partial removal leaves a cache, not an install),
// which the dialog copy states.
//
// Zoom-safety: no viewport units and no pointer-anchored placement — the
// dropdown is a Radix portal, compensated globally by lib/floatingUiZoom.

import { ChevronDown, Download, Loader2, Play, PowerOff, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import type { EmbeddedLLMBusyAction } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMRemoveScope } from '@/api/embedded'

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
  /** Opens the confirmation dialog for the chosen scope; the parent runs the
   *  RPC. */
  onRemove: (scope: EmbeddedLLMRemoveScope) => void
  /** Which artifact groups are on disk right now (an installed model has all
   *  three). A scope with nothing to delete renders disabled. */
  leftovers: { runtime: boolean; weights: boolean; projection: boolean }
}

export function EmbeddedLLMActions({
  loaded,
  loading,
  busy,
  onLoad,
  onUnload,
  onRemove,
  leftovers,
}: EmbeddedLLMActionsProps) {
  // Every action button stays disabled for the whole busy window, which the
  // parent holds open until the post-action read-back has landed.
  const busyNow = busy !== null
  const anyLeft = leftovers.runtime || leftovers.weights || leftovers.projection
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

      {anyLeft && (
        <div className="inline-flex" data-testid="embedded-llm-remove-group">
          <Button
            size="sm"
            variant="ghost"
            className="gap-2 rounded-r-none border border-border/50 text-destructive enabled:hover:bg-destructive/10 enabled:hover:text-destructive"
            onClick={() => onRemove('all')}
            disabled={busyNow}
            data-testid="embedded-llm-remove"
          >
            <Trash2 className="size-4" />
            Remove
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                size="sm"
                variant="ghost"
                className="rounded-l-none border border-l-0 border-border/50 px-1 text-destructive enabled:hover:bg-destructive/10 enabled:hover:text-destructive"
                disabled={busyNow}
                aria-label="Remove options"
                data-testid="embedded-llm-remove-menu-trigger"
              >
                <ChevronDown className="size-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                disabled={busyNow || !leftovers.runtime}
                onClick={() => onRemove('runtime')}
                data-testid="embedded-llm-remove-runtime"
              >
                Remove runtime only
                {!leftovers.runtime && ' (none on disk)'}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={busyNow || !leftovers.weights}
                onClick={() => onRemove('weights')}
                data-testid="embedded-llm-remove-weights"
              >
                Remove weights only
                {!leftovers.weights && ' (none on disk)'}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={busyNow || !leftovers.projection}
                onClick={() => onRemove('projection')}
                data-testid="embedded-llm-remove-projection"
              >
                Remove vision projector only
                {!leftovers.projection && ' (none on disk)'}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      )}
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

export interface EmbeddedLLMCleanupActionProps {
  busy: EmbeddedLLMBusyAction | null
  /** Which artifact groups survived a scoped removal. */
  leftovers: { runtime: boolean; weights: boolean; projection: boolean }
  /** Opens the confirmation dialog for the chosen scope; the parent runs the
   *  RPC. */
  onRemove: (scope: EmbeddedLLMRemoveScope) => void
}

/** The "data is still on disk" row of a NOT-installed machine: one line naming
 *  the residue plus the same split-button Remove the installed row carries.
 *  Rendered only when at least one group is present — the parent decides. */
export function EmbeddedLLMCleanupAction({
  busy,
  leftovers,
  onRemove,
}: EmbeddedLLMCleanupActionProps) {
  const parts: string[] = []
  if (leftovers.runtime) parts.push('runtime')
  if (leftovers.weights) parts.push('weights')
  if (leftovers.projection) parts.push('vision projector')
  const anyLeft = parts.length > 0
  return (
    <div className="flex flex-col gap-2" data-testid="embedded-llm-leftovers">
      {anyLeft && (
        <p className="text-xs text-muted-foreground">
          Data still on disk: {parts.join(', ')}. An install re-verifies it instead of
          re-downloading; removing clears it for good.
        </p>
      )}
      <EmbeddedLLMCleanupButton busy={busy} leftovers={leftovers} onRemove={onRemove} />
    </div>
  )
}

function EmbeddedLLMCleanupButton({
  busy,
  leftovers,
  onRemove,
}: {
  busy: EmbeddedLLMBusyAction | null
  leftovers: { runtime: boolean; weights: boolean; projection: boolean }
  onRemove: (scope: EmbeddedLLMRemoveScope) => void
}) {
  const busyNow = busy !== null
  return (
    <div className="inline-flex self-start">
      <Button
        size="sm"
        variant="ghost"
        className="gap-2 rounded-r-none border border-border/50 text-destructive enabled:hover:bg-destructive/10 enabled:hover:text-destructive"
        onClick={() => onRemove('all')}
        disabled={busyNow}
        data-testid="embedded-llm-leftover-remove"
      >
        <Trash2 className="size-4" />
        Remove
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            size="sm"
            variant="ghost"
            className="rounded-l-none border border-l-0 border-border/50 px-1 text-destructive enabled:hover:bg-destructive/10 enabled:hover:text-destructive"
            disabled={busyNow}
            aria-label="Remove options"
            data-testid="embedded-llm-leftover-menu-trigger"
          >
            <ChevronDown className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            disabled={busyNow || !leftovers.runtime}
            onClick={() => onRemove('runtime')}
            data-testid="embedded-llm-leftover-remove-runtime"
          >
            Remove runtime only
            {!leftovers.runtime && ' (none on disk)'}
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={busyNow || !leftovers.weights}
            onClick={() => onRemove('weights')}
            data-testid="embedded-llm-leftover-remove-weights"
          >
            Remove weights only
            {!leftovers.weights && ' (none on disk)'}
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={busyNow || !leftovers.projection}
            onClick={() => onRemove('projection')}
            data-testid="embedded-llm-leftover-remove-projection"
          >
            Remove vision projector only
            {!leftovers.projection && ' (none on disk)'}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}
