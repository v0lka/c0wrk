import { ChevronRight, ChevronDown, GitCommit, Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { CommitInfo, CommitFile } from '@/types/models'

/** Map a single-letter commit-file status to a theme text color. */
function fileStatusColor(status: string): string {
  switch (status) {
    case 'A':
      return 'text-success'
    case 'D':
      return 'text-destructive'
    case 'R':
    case 'C':
      return 'text-info'
    case 'M':
      return 'text-warning'
    default:
      return 'text-muted-foreground'
  }
}

interface CommitRowProps {
  commit: CommitInfo
  expanded: boolean
  files: CommitFile[] | undefined
  loadingFiles: boolean
  onClick: () => void
}

/** A single commit row in the history list; expands to show changed files. */
export function CommitRow({ commit, expanded, files, loadingFiles, onClick }: CommitRowProps) {
  const shortSha = commit.sha.slice(0, 7)
  return (
    <div className="border-b border-border/40">
      <button
        type="button"
        onClick={onClick}
        className="flex w-full items-start gap-1.5 px-2 py-1.5 text-left hover:bg-muted/50 transition-colors"
      >
        {expanded ? (
          <ChevronDown className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        )}
        <GitCommit className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm leading-tight">{commit.message}</div>
          <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-muted-foreground">
            <span className="shrink-0 font-mono text-info">{shortSha}</span>
            <span className="truncate">{commit.author}</span>
            <span className="ml-auto shrink-0 truncate max-w-[120px]">{commit.date}</span>
          </div>
        </div>
      </button>
      {expanded && (
        <div className="px-2 pb-1.5 pl-7">
          {loadingFiles ? (
            <div className="flex items-center gap-1.5 py-1 text-[11px] text-muted-foreground">
              <Loader2 className="size-3 animate-spin" /> Loading files…
            </div>
          ) : files && files.length > 0 ? (
            files.map((f) => (
              <div
                key={f.path}
                className="flex items-center gap-1.5 py-0.5 text-[11px] leading-none"
              >
                <span className={cn('shrink-0 font-mono font-semibold', fileStatusColor(f.status))}>
                  {f.status}
                </span>
                <span className="min-w-0 truncate text-muted-foreground">{f.path}</span>
              </div>
            ))
          ) : (
            <div className="py-1 text-[11px] text-muted-foreground">No files</div>
          )}
        </div>
      )}
    </div>
  )
}
