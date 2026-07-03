import { useState, useCallback } from 'react'
import { DownloadCloud, UploadCloud, RefreshCw, Loader2, AlertCircle, ChevronDown } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { pull, push, fetch } from '@/api/git'
import { useGitPanelStore } from '@/stores/gitPanelStore'

type RemoteOp = 'pull' | 'push' | 'fetch'

const OP_LABEL: Record<RemoteOp, string> = {
  pull: 'Pull',
  push: 'Push',
  fetch: 'Fetch',
}

/**
 * Remote operations footer (Phase 5): Pull / Push / Fetch.
 *
 * Parallel remote ops are blocked via the shared `remoteOperationInProgress`
 * store flag (Zed `pending_remote_operation` pattern). An empty `remote`
 * argument lets git use the configured upstream. The backend emits
 * `git:status_changed` after each op, so `useGitStatusEvents` auto-refreshes.
 */
export function GitPanelFooter() {
  const remoteOperationInProgress = useGitPanelStore(
    (s) => s.remoteOperationInProgress,
  )
  const setRemoteOperationInProgress = useGitPanelStore(
    (s) => s.setRemoteOperationInProgress,
  )

  const [activeOp, setActiveOp] = useState<RemoteOp | null>(null)
  const [output, setOutput] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [showOutput, setShowOutput] = useState(false)

  const runOp = useCallback(
    async (op: RemoteOp) => {
      setActiveOp(op)
      setError(null)
      setRemoteOperationInProgress(true)
      try {
        // Empty remote → backend uses the configured upstream.
        const result = op === 'pull' ? await pull('') : op === 'push' ? await push('') : await fetch('')
        setOutput(result || `${OP_LABEL[op]} completed.`)
        setShowOutput(true)
      } catch (err) {
        const message = err instanceof Error ? err.message : `${OP_LABEL[op]} failed`
        setError(message)
        setOutput(message)
        setShowOutput(true)
      } finally {
        setActiveOp(null)
        setRemoteOperationInProgress(false)
      }
    },
    [setRemoteOperationInProgress],
  )

  const handlePull = useCallback(() => void runOp('pull'), [runOp])
  const handlePush = useCallback(() => void runOp('push'), [runOp])
  const handleFetch = useCallback(() => void runOp('fetch'), [runOp])

  const busy = remoteOperationInProgress

  const buttons: { op: RemoteOp; icon: typeof DownloadCloud; onClick: () => void }[] = [
    { op: 'pull', icon: DownloadCloud, onClick: handlePull },
    { op: 'push', icon: UploadCloud, onClick: handlePush },
    { op: 'fetch', icon: RefreshCw, onClick: handleFetch },
  ]

  return (
    <div className="shrink-0 border-t border-border bg-secondary/30">
      <div className="flex items-center gap-1 px-2 py-1">
        {buttons.map(({ op, icon: Icon, onClick }) => (
          <Button
            key={op}
            variant="ghost"
            size="xs"
            disabled={busy}
            onClick={onClick}
            className="gap-1 text-xs"
            title={OP_LABEL[op]}
          >
            {activeOp === op ? (
              <Loader2 className="size-3.5 animate-spin" />
            ) : (
              <Icon className="size-3.5" />
            )}
            {OP_LABEL[op]}
          </Button>
        ))}

        {error && (
          <span className="ml-1 flex items-center gap-1 text-[10px] text-destructive truncate max-w-[140px]" title={error}>
            <AlertCircle className="size-3 shrink-0" />
            {error}
          </span>
        )}

        {(output || error) && (
          <Button
            variant="ghost"
            size="xs"
            className="ml-auto text-[10px] text-muted-foreground gap-0.5 px-1"
            onClick={() => setShowOutput((v) => !v)}
            aria-expanded={showOutput}
          >
            Output
            <ChevronDown className={cn('size-3 transition-transform', showOutput && 'rotate-180')} />
          </Button>
        )}
      </div>

      {showOutput && output && (
        <pre className="mx-2 mb-1.5 max-h-32 overflow-auto rounded bg-muted/60 p-1.5 text-[10px] leading-tight font-mono text-muted-foreground whitespace-pre-wrap">
          {output}
        </pre>
      )}
    </div>
  )
}
