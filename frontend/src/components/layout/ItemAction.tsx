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
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'

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
 * A single row action button with a left-aligned tooltip.
 *
 * Generic across list types — the calling site supplies the icon and intent
 * color (e.g. `text-info`, `text-destructive`).
 */
export function ItemAction({ label, onClick, disabled, disabledReason, children }: ItemActionProps) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={disabled ? 0 : undefined} className={disabled ? 'cursor-not-allowed' : undefined}>
          <button
            type="button"
            onClick={onClick}
            disabled={disabled}
            className={cn(
              'rounded p-0.5',
              disabled && 'pointer-events-none opacity-30',
            )}
          >
            {children}
          </button>
        </span>
      </TooltipTrigger>
      <TooltipContent side="left">{disabled && disabledReason ? disabledReason : label}</TooltipContent>
    </Tooltip>
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
