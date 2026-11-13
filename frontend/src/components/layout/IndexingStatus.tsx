import { cn } from '@/lib/utils'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { CheckCircle2 } from 'lucide-react'
import { deriveDotStatus, type DotState } from '@/lib/indexPhaseStatus'
import type { VectorIndexStatus } from '@/types/models'

function dotLabel(side: 'vector' | 'lexical', dot: DotState): string {
  const name = side === 'vector' ? 'Vector' : 'Lexical'
  switch (dot) {
    case 'green':
      return `${name}: ready`
    case 'active':
      return `${name}: building`
    case 'idle':
      return `${name}: idle`
    default:
      return name
  }
}

/**
 * The diagnosis suffix for an `unavailable` pill: the backend's cause
 * (`last_error`) and, when it is classified, the kind (`error_kind`). The
 * visible label stays the short "Index unavailable" — the hover carries the
 * reason, so a broken index is never a dead end the user has to dig out of
 * `~/.c0wrk` by hand. Both parts are the backend's own words: the pill must
 * not paraphrase a failure it does not understand.
 */
function unavailableDetail(status: VectorIndexStatus): string {
  const reason = status.last_error?.trim()
  const kind = status.error_kind?.trim()
  if (reason && kind) return `${reason}\nKind: ${kind}`
  return reason || (kind ? `Kind: ${kind}` : '')
}

function Dot({ state, label }: { state: DotState; label: string }) {
  return (
    <span className="relative flex size-2.5 shrink-0" title={label} aria-label={label}>
      {state === 'active' && (
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-info opacity-75" />
      )}
      <span
        className={cn(
          'relative inline-flex size-2.5 rounded-full',
          state === 'green' && 'bg-success',
          state === 'active' && 'bg-info',
          state === 'idle' && 'bg-muted-foreground/40',
        )}
      />
    </span>
  )
}

export function IndexingStatus() {
  const status = useVectorIndexStore((s) => s.status)
  const phase = status.phase

  if (status.state === 'idle') return null

  // The ADR-122 trip's EMPTY phase (a suspension is not a pass report)
  // normalizes to "no phase" here — deriveDotStatus reasons about real pass
  // phases only.
  const derived = deriveDotStatus(status.state, phase || undefined)
  const vectorLabel = dotLabel('vector', derived.vectorDot)
  const lexicalLabel = dotLabel('lexical', derived.lexicalDot)
  const tooltip = `${vectorLabel}\n${lexicalLabel}`

  if (derived.bothReady) {
    return (
      <div
        className="flex shrink-0 items-center gap-1.5 text-xs text-success"
        title={tooltip}
      >
        <div className="flex items-center gap-1">
          <Dot state="green" label={vectorLabel} />
          <Dot state="green" label={lexicalLabel} />
        </div>
        <CheckCircle2 className="size-3 shrink-0" />
        <span>Index ready</span>
      </div>
    )
  }

  if (status.state === 'unavailable') {
    // A failed DB open or init failure left no queryable collection. Render it
    // honestly — idle dots and a distinct label, never the "Indexing…" 0% bar
    // this state used to fall through to (the same dishonesty ADR-064 removed
    // for `loading`). Recovery is the search panel's reindex action, which the
    // backend escalates into a rebuild of an unopenable corpus (the action is
    // deliberately NOT disabled in this state); a branch change or project
    // switch no longer helps on its own, because a durable corpus defect
    // survives both. The hover carries the backend's own cause and its
    // classification (the "unavailable always explains itself" invariant), so
    // the pill itself stays the short label while the diagnosis stays one
    // hover away — never a modal, never a dead end.
    const detail = unavailableDetail(status)
    return (
      <div
        className="flex shrink-0 items-center gap-1.5 text-xs text-warning"
        title={detail ? `${tooltip}\n${detail}` : tooltip}
      >
        <div className="flex items-center gap-1">
          <Dot state={derived.vectorDot} label={vectorLabel} />
          <Dot state={derived.lexicalDot} label={lexicalLabel} />
        </div>
        <span>Index unavailable</span>
      </div>
    )
  }

  if (status.state === 'loading' || status.state === 'preparing') {
    // ADR-064: the branch-scoped persistent DB is being opened. Show an honest
    // "Preparing index…" — no progress bar and no n/m, never the "Indexing" /
    // "building" shape that implied a pass was already running.
    // `preparing` (ADR-104 stage 2) renders the same way: the view-model
    // attach window (snapshot identity resolution, corpus lease acquisition)
    // is pre-work, not a pass — idle dots and the honest label, no fake bar.
    return (
      <div
        className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground"
        title={tooltip}
      >
        <div className="flex items-center gap-1">
          <Dot state={derived.vectorDot} label={vectorLabel} />
          <Dot state={derived.lexicalDot} label={lexicalLabel} />
        </div>
        <span>Preparing index…</span>
      </div>
    )
  }

  if (status.state === 'migration-rebuild') {
    // ADR-104 stage 2: the one-time promotion of a legacy branch-keyed store
    // into the shared snapshot corpus (a rename, or the rebuild pass that
    // replaces it). Store-shape repair, not fresh content — its own honest
    // label with idle dots, never the "Indexing…" progress-bar shape.
    return (
      <div
        className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground"
        title={tooltip}
      >
        <div className="flex items-center gap-1">
          <Dot state={derived.vectorDot} label={vectorLabel} />
          <Dot state={derived.lexicalDot} label={lexicalLabel} />
        </div>
        <span>Migrating store…</span>
      </div>
    )
  }

  if (status.state === 'disabled') {
    // ADR-103: indexing is off via the master switch. Render the explicit off
    // state — idle dots and a plain label, no bar — distinct from
    // `unavailable` (a failed backend) and from hiding entirely (the user
    // turned this off on purpose; the pill says why search is gone).
    return (
      <div
        className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground"
        title={tooltip}
      >
        <div className="flex items-center gap-1">
          <Dot state={derived.vectorDot} label={vectorLabel} />
          <Dot state={derived.lexicalDot} label={lexicalLabel} />
        </div>
        <span>Indexing off</span>
      </div>
    )
  }

  if (status.state === 'indexing' || status.state === 'reindexing') {
    const isReindexing = status.state === 'reindexing'
    const pct = Math.round(status.progress * 100)
    const phaseLabel =
      phase === 'lexical' ? 'Lexical'
        : phase === 'embedding' ? 'Embedding'
          : null
    const actionLabel = isReindexing ? 'Updating' : 'Indexing'
    const text = phaseLabel ? `${actionLabel} ${phaseLabel}...` : `${actionLabel}...`

    return (
      <div
        className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground"
        title={tooltip}
      >
        <div className="flex items-center gap-1">
          <Dot state={derived.vectorDot} label={vectorLabel} />
          <Dot state={derived.lexicalDot} label={lexicalLabel} />
        </div>
        <span>{text}</span>
        <div className="h-1.5 w-16 overflow-hidden rounded-full bg-muted">
          <div
            className={cn('h-full rounded-full transition-all', isReindexing ? 'bg-muted-foreground' : 'bg-info')}
            style={{ width: `${pct}%` }}
          />
        </div>
        {status.total_files > 0 && (
          <span>{status.files_indexed}/{status.total_files}</span>
        )}
      </div>
    )
  }

  // Unknown states (a backend literal this frontend does not know yet): fall
  // to the honest preparing shape — idle dots and a neutral label, never a
  // fake progress bar or a "building" claim for a state we cannot describe.
  return (
    <div
      className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground"
      title={tooltip}
    >
      <div className="flex items-center gap-1">
        <Dot state={derived.vectorDot} label={vectorLabel} />
        <Dot state={derived.lexicalDot} label={lexicalLabel} />
      </div>
      <span>Preparing index…</span>
    </div>
  )
}
