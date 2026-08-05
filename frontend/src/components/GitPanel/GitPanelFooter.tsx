import { useState, useCallback } from 'react'
import { DownloadCloud, UploadCloud, RefreshCw, Loader2, AlertCircle, ChevronDown, Terminal } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'
import { pull, push, fetch } from '@/api/git'
import { useGitPanelStore } from '@/stores/gitPanelStore'

type RemoteOp = 'pull' | 'push' | 'fetch'

const OP_LABEL: Record<RemoteOp, string> = {
  pull: 'Pull',
  push: 'Push',
  fetch: 'Fetch',
}

/** Additional flag options offered per remote operation via the chevron dropdown. */
const OP_FLAGS: Record<RemoteOp, { label: string; flags: string[] }[]> = {
  pull: [
    { label: '--ff-only', flags: ['--ff-only'] },
    { label: '--rebase', flags: ['--rebase'] },
    { label: '--rebase --autostash', flags: ['--rebase', '--autostash'] },
  ],
  push: [
    { label: '--force', flags: ['--force'] },
    { label: '--force-with-lease', flags: ['--force-with-lease'] },
    { label: '--no-verify', flags: ['--no-verify'] },
  ],
  fetch: [
    { label: '--tags', flags: ['--tags'] },
    { label: '--prune', flags: ['--prune'] },
  ],
}

/**
 * Remote operations footer (Phase 5): Pull / Push / Fetch.
 *
 * Each operation is a split button: the main part runs the default
 * operation, and the chevron opens a dropdown of additional flag options
 * (e.g. pull --rebase, push --force-with-lease). Parallel remote ops are
 * blocked via the shared `remoteOperationInProgress` store flag. An empty
 * `remote` argument lets git use the configured upstream. The backend
 * emits `git:status_changed` after each op, so `useGitStatusEvents`
 * auto-refreshes.
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
    async (op: RemoteOp, flags: string[] = []) => {
      setActiveOp(op)
      setError(null)
      setRemoteOperationInProgress(true)
      try {
        // Empty remote → backend uses the configured upstream.
        const result =
          op === 'pull' ? await pull('', flags) :
          op === 'push' ? await push('', flags) :
          await fetch('', flags)
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

  const busy = remoteOperationInProgress

  const buttons: { op: RemoteOp; icon: typeof DownloadCloud }[] = [
    { op: 'fetch', icon: RefreshCw },
    { op: 'pull', icon: DownloadCloud },
    { op: 'push', icon: UploadCloud },
  ]

  return (
    <div className="shrink-0 border-t border-border bg-secondary/30">
      <div className="flex items-center gap-1 px-2 py-1">
        {buttons.map(({ op, icon: Icon }) => (
          <DropdownMenu key={op}>
            <div className="flex items-center">
              <Button
                variant="ghost"
                size="xs"
                disabled={busy}
                onClick={() => void runOp(op)}
                className="gap-1 rounded-r-none text-xs"
                title={OP_LABEL[op]}
              >
                {activeOp === op ? (
                  <Loader2 className="size-3.5 animate-spin" />
                ) : (
                  <Icon className="size-3.5" />
                )}
                {OP_LABEL[op]}
              </Button>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="xs"
                  disabled={busy}
                  className="rounded-l-none border-r border-border/50 px-1"
                  aria-label={`${OP_LABEL[op]} options`}
                >
                  <ChevronDown className="size-3" />
                </Button>
              </DropdownMenuTrigger>
            </div>
            <DropdownMenuContent align="start">
              <DropdownMenuLabel className="text-xs text-muted-foreground">
                {OP_LABEL[op]} options
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              {OP_FLAGS[op].map(({ label, flags }) => (
                <DropdownMenuItem
                  key={label}
                  className="gap-2 font-mono text-xs"
                  onClick={() => void runOp(op, flags)}
                >
                  {label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
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
            className="ml-auto text-muted-foreground gap-0.5 px-1"
            onClick={() => setShowOutput((v) => !v)}
            aria-expanded={showOutput}
            aria-label={showOutput ? 'Hide output' : 'Show output'}
            title={showOutput ? 'Hide output' : 'Show output'}
          >
            <Terminal className="size-3" />
            <ChevronDown className={cn('size-3 transition-transform', showOutput && 'rotate-180')} />
          </Button>
        )}
      </div>

      {showOutput && output && (
        <pre className="mx-2 mb-1.5 max-h-32 overflow-auto custom-scrollbar rounded bg-muted/60 p-1.5 text-[10px] leading-tight font-mono text-muted-foreground whitespace-pre-wrap">
          {output}
        </pre>
      )}
    </div>
  )
}
