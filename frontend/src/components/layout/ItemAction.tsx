// Shared row-action primitives used by both list surfaces in the sidebar:
//
// - ProjectSelector (project dropdown items)
// - SessionListItem (session rows, dropdown + flat variants)
//
// Both render the identical hover overlay: a gradient panel sliding over the
// right edge of the row with action buttons that appear on hover/focus. Keeping
// the markup here ensures the two lists stay visually and behaviorally in sync.

import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

// --- Single action button ---

interface ItemActionProps {
  label: string
  onClick: () => void
  disabled?: boolean
  /** Optional tooltip shown when the action is disabled (overrides `label`). */
  disabledReason?: string
  children: ReactNode
}

/**
 * A single row action button. The native `title` attribute is the ONLY
 * tooltip mechanism — no Radix Tooltip. For a disabled button (which never
 * receives pointer events, so its own `title` never shows) the reason is
 * carried by the `title` on the focusable wrapper span instead.
 *
 * Generic across list types — the calling site supplies the icon and intent
 * color (e.g. `text-info`, `text-destructive`).
 */
export function ItemAction({ label, onClick, disabled, disabledReason, children }: ItemActionProps) {
  const reason = disabled && disabledReason ? disabledReason : undefined
  return (
    <span
      tabIndex={disabled ? 0 : undefined}
      title={reason}
      // The span is the tab stop while disabled and its contents (a disabled
      // button) are excluded from accessible-name computation, so the action
      // name must be carried explicitly: "label: reason".
      aria-label={reason ? `${label}: ${reason}` : undefined}
      className={disabled ? 'cursor-not-allowed' : undefined}
    >
      <button
        type="button"
        title={reason ?? label}
        onClick={onClick}
        disabled={disabled}
        className={cn(
          'rounded p-0.5 transition-colors enabled:hover:bg-accent/20 enabled:active:bg-accent/30',
          disabled && 'pointer-events-none opacity-30',
        )}
      >
        {children}
      </button>
    </span>
  )
}

// --- Hover overlay container ---

interface ItemActionsProps {
  children: ReactNode
}

/**
 * Absolutely-positioned hover overlay for a row's action buttons. Renders a
 * gradient panel over the right edge of the item so the underlying relative
 * time stays readable; appears on hover/focus. Pointer events are contained so
 * clicking an action never selects the row.
 *
 * The parent item must be `relative` (e.g. a `DropdownMenuItem`).
 */
export function ItemActions({ children }: ItemActionsProps) {
  return (
    <span
      className="absolute inset-y-0 right-0 flex items-center gap-0.5 pl-10 pr-1 opacity-0 transition-opacity bg-gradient-to-l from-popover via-popover via-65% to-popover/0 group-hover/item:opacity-100 group-focus-within/item:opacity-100"
      onPointerDown={(e) => e.stopPropagation()}
      onPointerUp={(e) => e.stopPropagation()}
      onClick={(e) => e.stopPropagation()}
    >
      {children}
    </span>
  )
}
