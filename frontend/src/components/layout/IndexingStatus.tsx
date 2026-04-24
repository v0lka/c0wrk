import { cn } from '@/lib/utils'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { CheckCircle2, Loader2 } from 'lucide-react'

export function IndexingStatus() {
  const status = useVectorIndexStore((s) => s.status)

  if (status.state === 'idle') return null

  if (status.state === 'ready') {
    return (
      <div className="flex items-center gap-1 text-xs text-success">
        <CheckCircle2 className="size-3" />
        <span>Index ready</span>
      </div>
    )
  }

  const isReindexing = status.state === 'reindexing'
  const pct = Math.round(status.progress * 100)

  return (
    <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
      {isReindexing ? (
        <>
          <Loader2 className="size-3 animate-spin" />
          <span>Updating...</span>
        </>
      ) : (
        <>
          <span className="relative flex size-3">
            <span className="absolute inline-flex size-full animate-ping rounded-full bg-info opacity-75" />
            <span className="relative inline-flex size-3 rounded-full bg-info" />
          </span>
          <span>Indexing...</span>
        </>
      )}
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
