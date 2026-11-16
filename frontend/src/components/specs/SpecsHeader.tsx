// Specs panel header: the integrity badge (the findings count — a BUTTON only
// when the active framework declared the findings-fix gesture), the aggregate
// model counts, the framework selector while more than one framework is
// detected, and the icon-only manual-refresh button. Pure presentational over
// the payload: every label it shows (framework names, counts) comes from the
// backend's parse, not from a frontend vocabulary.

import { AlertTriangle, CheckCircle2, RefreshCw } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { SDDAction, SDDFramework, SpecsStatus } from '@/types/models'
import { NO_FIX_FINDINGS_DECLARED_REASON, inertActionReason } from './specsActions'
import { SpecsFrameworkSelector } from './SpecsFrameworkSelector'

/**
 * The integrity badge: 0 findings → a success "Integrity OK" chip; any finding
 * → a warning chip naming the count. A tree with NO documents gets neither: an
 * empty tree has nothing to check, so claiming "Integrity OK" there would
 * assert a clean result nobody computed. It is a BUTTON only when the framework
 * declared the findings-fix gesture (clean pass-through `onFixFindings`);
 * without a declared action the badge stays INERT — it reports, it does not
 * act, and the panel never invents a skill the framework did not offer.
 */
function IntegrityBadge({
  findings,
  documents,
  fixAction,
  onFixFindings,
}: {
  findings: number
  /** Document count of the parsed tree: 0 means there was nothing to check. */
  documents: number
  /** The framework's DECLARED findings-fix action, or null when it declared
   *  none. Declared-but-unrunnable (no skill, or its requirements unmet)
   *  renders INERT WITH THE REASON — the badge never looks live while the
   *  dispatch surfaces refuse it, and never hides why. */
  fixAction: SDDAction | null
  onFixFindings?: (newSession: boolean) => void
}) {
  if (findings === 0) {
    if (documents === 0) {
      return (
        <span
          data-testid="specs-integrity-badge"
          data-inert="true"
          data-empty="true"
          title="No documents were parsed, so no check ran"
          className="inline-flex items-center gap-1 rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground"
        >
          <CheckCircle2 className="size-3" />
          Nothing to check
        </span>
      )
    }
    return (
      <span
        data-testid="specs-integrity-badge"
        data-inert="true"
        className="inline-flex items-center gap-1 rounded bg-success/10 px-1.5 py-0.5 text-xs text-success"
      >
        <CheckCircle2 className="size-3" />
        Integrity OK
      </span>
    )
  }
  const label = (
    <>
      <AlertTriangle className="size-3" />
      {findings} {findings === 1 ? 'finding' : 'findings'}
    </>
  )
  if (onFixFindings === undefined) {
    // Declared but not runnable, or not declared at all: the same chip,
    // inert, naming which of the two.
    const inertTitle =
      fixAction !== null
        ? inertActionReason(fixAction)
        : NO_FIX_FINDINGS_DECLARED_REASON
    return (
      <span
        data-testid="specs-integrity-badge"
        data-inert="true"
        title={inertTitle}
        className="inline-flex items-center gap-1 rounded bg-warning/10 px-1.5 py-0.5 text-xs text-warning"
      >
        {label}
      </span>
    )
  }
  return (
    <button
      type="button"
      data-testid="specs-integrity-badge"
      data-inert="false"
      onClick={(e) => onFixFindings(e.shiftKey)}
      title={`Fix all ${findings} integrity findings in the chat — Shift = new session`}
      className={cn(
        'inline-flex items-center gap-1 rounded bg-warning/10 px-1.5 py-0.5 text-xs text-warning',
        'transition-colors hover:bg-warning/20',
      )}
    >
      {label}
    </button>
  )
}

/**
 * The header: the integrity badge (the findings count doubles as the
 * findings-fix dispatch when the framework declares it), the aggregate counts,
 * the framework selector (only while more than one framework is detected) and
 * the icon-only manual-refresh button at the right edge. The absolute active
 * root rides along as the row's tooltip. The row renders in BOTH header
 * branches (present / !present — a tree-less project can still be re-parsed)
 * and the refresh button is inert while a fetch is in flight.
 */
export function SpecsHeader({
  status,
  frameworks,
  activeId,
  onSelectFramework,
  switching = false,
  fixAction = null,
  onFixFindings,
  onRefresh,
  isLoading = false,
}: {
  status: SpecsStatus
  /** Detected frameworks (the selector's list); rendered only when >1. */
  frameworks: SDDFramework[]
  /** The selection to render — the operator's intent, pending switches
   *  included. */
  activeId: string
  onSelectFramework: (id: string) => void
  switching?: boolean
  /** The framework's declared findings-fix action (null when none) — what
   *  the inert badge explains when the gesture is not runnable. */
  fixAction?: SDDAction | null
  /** Present only when the gesture is DISPATCHABLE (skill + available) — the
   *  badge then renders a button; otherwise it stays inert with the reason. */
  onFixFindings?: (newSession: boolean) => void
  onRefresh?: () => void
  isLoading?: boolean
}) {
  const { counts, root, active } = status
  return (
    <div
      data-testid="specs-header"
      title={root}
      className="flex shrink-0 flex-col gap-1 border-b border-border bg-secondary/30 px-2 py-1"
    >
      <div className="flex min-h-[22px] items-center gap-2">
        <IntegrityBadge
          findings={counts.findings}
          documents={counts.documents}
          fixAction={fixAction}
          onFixFindings={onFixFindings}
        />
        <span className="min-w-0 truncate text-xs text-muted-foreground">
          {counts.documents} {counts.documents === 1 ? 'document' : 'documents'}
          {active !== undefined ? ` · ${active.label}` : ''}
        </span>
        <button
          type="button"
          data-testid="specs-refresh"
          onClick={onRefresh}
          disabled={!onRefresh || isLoading}
          aria-label="Re-parse the specification tree and re-check integrity"
          title="Re-parse the specification tree and re-check integrity"
          className={cn(
            'ml-auto inline-flex shrink-0 items-center rounded p-1 text-muted-foreground',
            'transition-colors hover:bg-secondary hover:text-foreground',
            'disabled:pointer-events-none disabled:opacity-50',
          )}
        >
          <RefreshCw className={cn('size-3.5', isLoading && 'animate-spin')} aria-hidden />
        </button>
      </div>
      {/* The selector renders itself only for >1 detected framework. */}
      <SpecsFrameworkSelector
        frameworks={frameworks}
        activeId={activeId}
        onSelect={onSelectFramework}
        switching={switching}
      />
    </div>
  )
}
