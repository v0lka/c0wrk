// The empty state of a DETECTED framework whose tree holds no documents: the
// declared roots may be ABSENT (nothing on disk yet) or PRESENT but empty (the
// directory exists and no concept matched anything in it). Both are data, not
// errors. It names the root that is missing or empty and offers the
// framework's bootstrap gesture — the declared action carrying no requirement
// at all — when it offers one AND is dispatchable (the one dispatchable
// predicate every surface shares: skill installed, available for the tree). A
// framework without a dispatchable bootstrap gets the explanation and no CTA:
// the panel never invents a lifecycle step and never wires a gesture the
// backend reported inert.

import { BookPlus } from 'lucide-react'
import { actionToDispatch, bootstrapAction, dispatchableAction, type SpecsDispatch } from './specsActions'
import type { SpecsStatus } from '@/types/models'

export function SpecsEmptyState({
  status,
  onDispatch,
  reason = 'absent',
}: {
  status: SpecsStatus
  /** Where the gesture goes: SpecsPanel's preview gate, which shows the
   *  framework-declared prompt and sends on confirm. */
  onDispatch: (pending: SpecsDispatch, newSession: boolean) => void
  /** Which empty state this is: the tree is absent from the workspace, or it
   *  exists and holds no documents yet. The copy must not claim one when it is
   *  the other. */
  reason?: 'absent' | 'empty-tree'
}) {
  const bootstrap = bootstrapAction(status.actions)
  const pending = dispatchableAction(bootstrap) ? actionToDispatch(bootstrap) : null
  const declaredRoot = status.roots[0]?.path ?? ''
  const label = status.active?.label ?? 'specification'
  const rootPath = status.root !== '' ? status.root : declaredRoot

  return (
    <div
      data-testid="specs-empty-state"
      data-reason={reason}
      className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-4 py-8 text-center"
    >
      <div className="flex size-10 items-center justify-center rounded-full bg-secondary">
        <BookPlus className="size-5 text-muted-foreground" />
      </div>
      <div className="text-xs text-foreground">
        {reason === 'empty-tree' ? `No ${label} documents yet` : `No ${label} tree yet`}
      </div>
      <div className="max-w-[280px] text-xs leading-relaxed text-muted-foreground">
        {reason === 'empty-tree'
          ? `The ${label} tree exists in this workspace but holds no documents yet — nothing has been matched against the framework's concepts.`
          : status.active?.description !== undefined && status.active.description !== ''
            ? status.active.description
            : 'The specification tree is not initialized in this workspace.'}
      </div>
      {rootPath !== '' && (
        <code className="max-w-full truncate rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground">
          {rootPath}
        </code>
      )}
      {bootstrap !== null && pending !== null && (
        <button
          type="button"
          data-testid="specs-initialize-cta"
          data-action={bootstrap.kind}
          data-skill={pending.skill}
          onClick={(e) => onDispatch(pending, e.shiftKey)}
          title={`${bootstrap.label} (${pending.skill}) — Shift = new session`}
          className="inline-flex items-center gap-1.5 rounded-md border border-border bg-background px-3 py-1.5 text-xs text-foreground transition-colors hover:bg-muted"
        >
          <BookPlus className="size-3.5 text-muted-foreground" />
          {bootstrap.label}
        </button>
      )}
    </div>
  )
}
