// The neutral !present state (ADR-106 D2): a missing or non-directory
// specs/ root is data, not an error. The CTA dispatches the Initialize
// gesture — the same constant the quick-actions row renders — into the
// chat.

import { BookPlus } from 'lucide-react'
import { INITIALIZE_ACTION } from './specsActions'
import { useSpecsDispatch } from './useSpecsDispatch'

/** The empty state: what is missing (the expected root path), one sentence
 *  of what the spec system is, and the Initialize CTA (Shift = new
 *  session, like every quick action). */
export function SpecsEmptyState({ root }: { root: string }) {
  const dispatch = useSpecsDispatch()

  return (
    <div
      data-testid="specs-empty-state"
      className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-4 py-8 text-center"
    >
      <div className="flex size-10 items-center justify-center rounded-full bg-secondary">
        <BookPlus className="size-5 text-muted-foreground" />
      </div>
      <div className="text-xs text-foreground">No specs/ directory yet</div>
      <div className="max-w-[280px] text-xs leading-relaxed text-muted-foreground">
        The specification system (task navigation, ADR timeline, integrity
        signal) lives in a specs/ tree at the project root.
      </div>
      <code className="max-w-full truncate rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground">
        {root}
      </code>
      <button
        type="button"
        data-testid="specs-initialize-cta"
        data-skill={INITIALIZE_ACTION.skill}
        onClick={(e) => dispatch(INITIALIZE_ACTION, e.shiftKey)}
        title={`${INITIALIZE_ACTION.fullLabel ?? INITIALIZE_ACTION.label} (${INITIALIZE_ACTION.skill}) — Shift = new session`}
        className="inline-flex items-center gap-1.5 rounded-md border border-border bg-background px-3 py-1.5 text-xs text-foreground transition-colors hover:bg-muted"
      >
        <BookPlus className="size-3.5 text-muted-foreground" />
        {INITIALIZE_ACTION.label}
      </button>
    </div>
  )
}
