import { useState } from 'react'
import { Check, ChevronDown, ChevronRight, Circle, Sigma } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useE2SSnapshot } from '@/stores/e2sStore'
import type { E2SSnapshot } from '@/stores/e2sStore'

/**
 * Execution State panel — the E2S session's live Σₜ display, rendered INSTEAD
 * of the plan view (an E2S session has no plan DAG). Collapsible; zoom-safe by
 * construction: the scrollable body is capped with a rem-based max-height
 * (absolute lengths intentionally scale with the UI zoom — never a viewport
 * unit; see specs/domains/frontend/ui-scale.md).
 */

/** Map a status string to a Tailwind badge class. Covers both the Σ lifecycle
 *  statuses the backend defines (active/paused/met/failed/cancelled) and the
 *  forward top-level statuses (running/done); unknown values default to the
 *  info accent. */
function statusBadgeClass(status: string): string {
  switch (status) {
    case 'done':
    case 'completed':
    case 'success':
    case 'met':
    case 'finished':
      return 'text-success border-success/40 bg-success/10'
    case 'failed':
    case 'error':
    case 'aborted':
      return 'text-destructive border-destructive/40 bg-destructive/10'
    case 'paused':
    case 'blocked':
    case 'waiting':
      return 'text-warning border-warning/40 bg-warning/10'
    case 'cancelled':
      return 'text-muted-foreground border-border bg-background/50'
    default:
      return 'text-info border-info/40 bg-info/10'
  }
}

/** One key field rendered as a label + bullet list (omitted when empty). */
function FieldList({ label, items }: { label: string; items: readonly string[] }) {
  if (items.length === 0) return null
  return (
    <div>
      <span className="block text-xs uppercase tracking-wide text-muted-foreground/70">{label}</span>
      <ul className="mt-0.5 space-y-0.5">
        {items.map((item, i) => (
          <li key={`${label}-${i}`} className="text-xs text-foreground/90 break-words">{item}</li>
        ))}
      </ul>
    </div>
  )
}

/** The Σ checklist: checked items get a success check, unchecked ones a muted
 *  open circle — the panel's primary progress signal. */
function Checklist({ items }: { items: ReadonlyArray<{ text: string; checked: boolean }> }) {
  if (items.length === 0) return null
  return (
    <ul className="space-y-0.5">
      {items.map((item, i) => (
        <li key={`item-${i}`} className="flex items-start gap-1.5">
          {item.checked ? (
            <Check className="mt-0.5 h-3.5 w-3.5 shrink-0 text-success" aria-label="checked" />
          ) : (
            <Circle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground/60" aria-label="unchecked" />
          )}
          <span className={cn('text-xs break-words', item.checked ? 'text-muted-foreground line-through' : 'text-foreground')}>
            {item.text}
          </span>
        </li>
      ))}
    </ul>
  )
}

/** The panel body — Σ fields + checklist. Pure presentational helper fed by
 *  the session's E2SSnapshot so both the live panel and tests render from the
 *  same shape. Exported for tests. */
export function ExecutionStateBody({ snapshot }: { snapshot: E2SSnapshot }) {
  const sigma = snapshot.state
  return (
    <div className="max-h-48 space-y-2 overflow-y-auto px-3 pb-2 custom-scrollbar">
      {sigma.objective ? (
        <div>
          <span className="block text-xs uppercase tracking-wide text-muted-foreground/70">Objective</span>
          <p className="mt-0.5 text-xs text-foreground break-words">{sigma.objective}</p>
        </div>
      ) : null}
      <Checklist items={sigma.checklist ?? []} />
      {sigma.status ? (
        <div>
          <span className="block text-xs uppercase tracking-wide text-muted-foreground/70">Status</span>
          <p className="mt-0.5 text-xs text-foreground/90 break-words">{sigma.status}</p>
        </div>
      ) : null}
      <FieldList label="Files touched" items={sigma.files_touched ?? []} />
      <FieldList label="Findings" items={sigma.findings ?? []} />
      <FieldList label="Decisions" items={sigma.decisions ?? []} />
      <FieldList label="Done criteria" items={sigma.done_criteria ?? []} />
      <FieldList label="Next steps" items={sigma.next_steps ?? []} />
    </div>
  )
}

export function ExecutionStatePanel({ sessionId }: { sessionId: string }) {
  const snapshot = useE2SSnapshot(sessionId)
  // Default OPEN: the panel is the E2S session's primary progress display —
  // the user opted into watching Σₜ evolve step by step.
  const [open, setOpen] = useState(true)

  if (!snapshot || !snapshot.active) return null

  // The badge's effective status — same value for the visible label, the
  // data-status test hook, and the color class, so the three can never
  // disagree when the payload carries no status at all.
  const badgeStatus = snapshot.status || 'active'

  return (
    <div className="group">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 w-full px-3 py-2 text-left text-foreground hover:bg-muted transition-colors rounded-sm"
        aria-expanded={open}
      >
        <span className="opacity-0 group-hover:opacity-100 transition-opacity inline-flex">
          {open
            ? <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
            : <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />}
        </span>
        <Sigma className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-sm font-medium">Execution state</span>
        <span className="text-xs text-muted-foreground">
          {/* maxSteps 0 = unbudgeted run (no max_turns in the payload) — show
              the bare turn counter instead of a misleading "turn N/0". */}
          {snapshot.maxSteps > 0 ? `turn ${snapshot.turn}/${snapshot.maxSteps}` : `turn ${snapshot.turn}`}
        </span>
        <span
          className={`inline-flex items-center rounded border px-1.5 py-0.5 text-xs font-medium uppercase tracking-wide ${statusBadgeClass(badgeStatus)}`}
          data-status={badgeStatus}
        >
          {badgeStatus}
        </span>
      </button>
      {open && <ExecutionStateBody snapshot={snapshot} />}
    </div>
  )
}
