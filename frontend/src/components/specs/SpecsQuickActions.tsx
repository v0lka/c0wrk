// Quick-actions row (ADR-105 D4): one button per lifecycle gesture, each
// dispatching its matching `spec-*` skill (with a constant prompt from
// specsActions.ts) through the shared dispatch hook. This is the "start
// doing" surface — the panel never writes to specs/ itself.

import { BookPlus, Compass, MessagesSquare, ScanSearch, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useSpecsDispatch } from './useSpecsDispatch'
import { NO_SPECS_REASON, QUICK_ACTIONS } from './specsActions'

/** Icon per quick-action key (stable; the key is part of the constant list). */
const ACTION_ICONS: Record<string, LucideIcon> = {
  initialize: BookPlus,
  'check-drift': ScanSearch,
  consult: MessagesSquare,
  explore: Compass,
}

const ACTION_BUTTON_CLASS =
  'inline-flex items-center gap-1 rounded-md border border-border bg-background px-2 py-1 text-xs text-foreground transition-colors hover:bg-muted'

/**
 * Every button's enablement is derived from the tree's presence so no
 * workflow-invalid gesture is offered: Check drift, Consult and Explore
 * operate on an existing specs/ tree and render disabled (with the reason
 * tooltip) until one exists; Initialize specs is always available — it is
 * the same gesture as the empty state's CTA and may repair a partial tree.
 *
 * Shift modifier: holding Shift while clicking dispatches into a brand-new
 * session instead of the active one (the research panel's contract).
 */
export function SpecsQuickActions({ present }: { present: boolean }) {
  const dispatch = useSpecsDispatch()

  return (
    <div
      data-testid="specs-quick-actions"
      className="flex shrink-0 flex-wrap items-center gap-1 px-1.5 pt-1.5"
    >
      {QUICK_ACTIONS.map((action) => {
        const Icon = ACTION_ICONS[action.key] ?? BookPlus
        const enabled = !action.requiresSpecs || present
        return (
          <button
            key={action.key}
            type="button"
            data-testid="specs-quick-action"
            data-skill={action.skill}
            disabled={!enabled}
            onClick={(e) => {
              if (!enabled) return
              dispatch(action, e.shiftKey)
            }}
            title={
              enabled
                ? `${action.label} (${action.skill}) — Shift = new session`
                : NO_SPECS_REASON
            }
            className={cn(ACTION_BUTTON_CLASS, 'disabled:opacity-50')}
          >
            <Icon className="size-3.5 text-muted-foreground" />
            {action.label}
          </button>
        )
      })}
    </div>
  )
}
