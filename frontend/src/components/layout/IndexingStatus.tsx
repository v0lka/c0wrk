import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { Database, Loader2, CheckCircle2 } from 'lucide-react'
import { Separator } from '@/components/ui/separator'

function truncateFile(path: string, maxLen = 40): string {
  if (path.length <= maxLen) return path
  return '…' + path.slice(-maxLen)
}

function formatCount(n: number): string {
  return n.toLocaleString()
}

export function IndexingStatus() {
  const status = useVectorIndexStore(s => s.status)
  const progress = useVectorIndexStore(s => s.progress)
  const filesIndexed = useVectorIndexStore(s => s.filesIndexed)
  const totalFiles = useVectorIndexStore(s => s.totalFiles)
  const currentFile = useVectorIndexStore(s => s.currentFile)
  const branch = useVectorIndexStore(s => s.branch)

  if (status === 'idle') return null

  if (status === 'indexing') {
    return (
      <>
        <Separator orientation="vertical" className="h-4" />
        <div className="flex items-center gap-2 min-w-0">
          <div className="relative flex items-center justify-center h-3 w-3">
            <span className="absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75 animate-ping" />
            <Database className="relative h-3 w-3 text-blue-500" />
          </div>
          <span className="text-muted-foreground whitespace-nowrap">
            Indexing: {formatCount(filesIndexed)}/{formatCount(totalFiles)} ({Math.round(progress)}%)
          </span>
          {/* Progress bar */}
          <div className="w-16 h-1.5 rounded-full bg-muted overflow-hidden">
            <div
              className="h-full rounded-full bg-blue-500 transition-all duration-300 ease-out"
              style={{ width: `${Math.min(100, Math.max(0, progress))}%` }}
            />
          </div>
          {currentFile && (
            <span className="text-muted-foreground/60 truncate max-w-[160px] text-[10px]" title={currentFile}>
              {truncateFile(currentFile)}
            </span>
          )}
        </div>
      </>
    )
  }

  if (status === 'reindexing') {
    return (
      <>
        <Separator orientation="vertical" className="h-4" />
        <div className="flex items-center gap-1.5">
          <Loader2 className="h-3 w-3 animate-spin text-muted-foreground" />
          <span className="text-muted-foreground">Updating index…</span>
        </div>
      </>
    )
  }

  // status === 'ready'
  return (
    <>
      <Separator orientation="vertical" className="h-4" />
      <div className="flex items-center gap-1.5">
        <CheckCircle2 className="h-3 w-3 text-emerald-500" />
        <span className="text-muted-foreground">
          Index ready{branch ? ` · ${branch}` : ''}{totalFiles > 0 ? ` · ${formatCount(totalFiles)} files` : ''}
        </span>
      </div>
    </>
  )
}
