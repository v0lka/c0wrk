// The quick-actions row: one button per PANEL-LEVEL action the active
// framework declares, each dispatching that action's own skill with its own
// declared prompt. This is the "start doing" surface — the panel never writes
// to the tree itself, and a framework that declares no gesture renders no row.
//
// Node-scoped actions (an action carrying a `concept`) belong to their nodes
// and the reserved findings gesture belongs to the findings badge, so neither
// appears here.

import {
  BookPlus,
  Compass,
  FilePlus,
  MessagesSquare,
  Play,
  RefreshCw,
  ScanSearch,
  Sparkles,
  type LucideIcon,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { actionToDispatch, inertActionReason, toolbarActions, type SpecsDispatch } from './specsActions'
import type { SDDAction } from '@/types/models'

/** Icon per well-known action kind; an unknown kind gets a generic icon. The
 *  LABEL always comes from the payload — this map is presentation only. */
const ACTION_ICONS: Record<string, LucideIcon> = {
  init: BookPlus,
  initialize: BookPlus,
  check: ScanSearch,
  consult: MessagesSquare,
  explore: Compass,
  create: FilePlus,
  update: RefreshCw,
  apply: Play,
}

const ACTION_BUTTON_CLASS =
  'inline-flex items-center gap-1 rounded-md border border-border bg-background px-2 py-1 text-xs text-foreground transition-colors hover:bg-muted'

/**
 * Every button's enablement is the PAYLOAD's answer: `available` is the
 * backend's evaluation of the action's declared `requires` against the parsed
 * tree, so the panel offers no workflow-invalid gesture and never re-derives a
 * requirement rule of its own. A disabled button explains why.
 *
 * Shift modifier: holding Shift while clicking dispatches into a brand-new
 * session instead of the active one (the research panel's contract).
 */
export function SpecsQuickActions({
  actions,
  onDispatch,
}: {
  actions: SDDAction[]
  /** Where the gesture goes: SpecsPanel's preview gate, which shows the
   *  framework-declared prompt and sends on confirm. */
  onDispatch: (pending: SpecsDispatch, newSession: boolean) => void
}) {
  const declared = toolbarActions(actions)
  if (declared.length === 0) return null

  return (
    <div
      data-testid="specs-quick-actions"
      className="flex shrink-0 flex-wrap items-center gap-1 px-1.5 pt-1.5"
    >
      {declared.map((action) => {
        // hasOwnProperty: a plain record lookup also reads INHERITED names —
        // a valid operator slug like `constructor` would resolve to
        // Object.prototype.constructor, and React would try to render that
        // function-as-component and crash the whole app. Unknown slugs take
        // the generic icon.
        const Icon: LucideIcon = Object.prototype.hasOwnProperty.call(
          ACTION_ICONS,
          action.kind,
        )
          ? (ACTION_ICONS[action.kind] ?? Sparkles)
          : Sparkles
        const pending = actionToDispatch(action)
        const enabled = action.available && pending !== null
        return (
          <button
            key={action.kind}
            type="button"
            data-testid="specs-quick-action"
            data-action={action.kind}
            data-skill={action.skill ?? ''}
            disabled={!enabled}
            onClick={(e) => {
              if (!enabled || pending === null) return
              onDispatch(pending, e.shiftKey)
            }}
            title={
              enabled
                ? `${action.label} (${action.skill ?? ''}) — Shift = new session`
                : inertActionReason(action)
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
