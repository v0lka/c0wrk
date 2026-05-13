import { cn } from '@/lib/utils'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { CheckCircle2 } from 'lucide-react'
import { deriveDotStatus, type DotState } from '@/lib/indexPhaseStatus'

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

  const derived = deriveDotStatus(status.state, phase)
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
