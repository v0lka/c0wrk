import { ChevronDown, ChevronRight, Loader2 } from 'lucide-react'
import { shortSha, type GraphNode } from '@/lib/gitGraphLayout'
import { statBucket, statColorClass, countFileStats } from '@/lib/commitStats'
import { Markdown } from '@/lib/markdownConfig'
import { cn } from '@/lib/utils'
import { ROW_SPACING } from '@/components/GitPanel/gitGraphRender'
import type { CommitFile, CommitStats, GitHistoryCommit } from '@/types/models'
import { FILE_LINE_HEIGHT, EXPANDED_TOP_PAD, EXPANDED_BOTTOM_PAD, LOADING_LINE_HEIGHT, NO_FILES_HEIGHT, STATUS_LINE_HEIGHT, messageAreaHeight, fileDiffAreaHeight, type FileDiffUiState } from './gitGraphCardHeights'
import { CommitFileDiff } from './CommitFileDiff'
import { GitGraphCardMeta } from './GitGraphCardMeta'

/** Render the full commit message as a Markdown blockquote: every line gets
 *  the `> ` prefix, so the shared Markdown pipeline styles it via
 *  `.prose blockquote` (muted text, left rule) and interprets any markdown
 *  the message carries. */
function asQuote(message: string): string {
  return message
    .split('\n')
    .map((line) => `> ${line}`)
    .join('\n')
}

interface GitGraphCardProps {
  /** Lane-laid-out graph node (sha, message, refs, merge flag, parents). */
  node: GraphNode
  /** The raw history commit (author, date, optional backend stats triple). */
  commit: GitHistoryCommit
  /** Reserved pixel height of the whole card (cardHeight — header + expansion). */
  height: number
  /** Whether the card is expanded below its two-line header. */
  expanded: boolean
  /** All CardExpansionState fields, mirrored as flat props. */
  messageLoading: boolean
  messageFailed: boolean
  fullMessage: string | undefined
  files: CommitFile[] | undefined
  filesLoading: boolean
  fileDiffs: Record<string, FileDiffUiState>
  /** Unified or split rendering — the shared reviewStore preference. */
  diffViewMode: 'unified' | 'split'
  /** Toggle the card expansion (the parent lazily loads message + files). */
  onToggle: () => void
  /** Toggle ONE file's inline diff (the parent lazily loads the commit diff). */
  onToggleFile: (path: string) => void
  /**
   * Right-click on a changed file row — opens the per-file context menu
   * ("Open in Viewer" → the commit-version diff). Omitting it leaves the
   * file rows context-menu-less. The handler must stop propagation so the
   * card-level commit menu does not also fire.
   */
  onFileContextMenu?: (e: React.MouseEvent, sha: string, path: string) => void
}

/**
 * One commit card in the Git-graph page: the two-line collapsed card —
 * subject + sha above, the author · time / stats / ref decorations line
 * below (GitGraphCardMeta) — that expands — via the chevron — into the full
 * commit message, then the changed-file list (status letter + path), where
 * every file toggles its own inline diff.
 *
 * Purely presentational: all fetch state arrives as props, and every
 * rendered section takes an EXPLICIT pixel height derived from the shared
 * deterministic height math (gitGraphCardHeights), so the DOM height is
 * provably equal to the height the page reserved for the row — no DOM
 * measurement anywhere.
 *
 * Empty states (files list loaded empty): a merge commit shows "No diff
 * (merge commit)" (diff-tree emits no merge patch), a root commit shows
 * "No files (root commit)" (diff-tree's --name-status emits nothing for a
 * parentless commit), anything else "No files".
 */
export function GitGraphCard({
  node,
  commit,
  height,
  expanded,
  messageLoading,
  messageFailed,
  fullMessage,
  files,
  filesLoading,
  fileDiffs,
  diffViewMode,
  onToggle,
  onToggleFile,
  onFileContextMenu,
}: GitGraphCardProps) {
  const messageHeight = messageAreaHeight({ messageLoading, messageFailed, fullMessage })
  const emptyText = node.isMerge
    ? 'No diff (merge commit)'
    : node.parents.length === 0
      ? 'No files (root commit)'
      : 'No files'

  // Per-commit stats: the backend's triple when present, else derived from
  // the loaded file list (same bucketing). Hidden while files load and the
  // backend sent no stats (nothing to derive from yet).
  const stats: CommitStats | undefined =
    commit.stats ?? (files !== undefined && files.length > 0 ? countFileStats(files) : undefined)

  return (
    <div style={{ height }}>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={expanded}
        title={expanded ? 'Collapse commit details' : 'Expand commit details'}
        className="flex w-full flex-col justify-start gap-0.5 px-2 pt-1 text-left"
        style={{ height: ROW_SPACING }}
      >
        <div className="flex items-center gap-2 text-sm leading-none">
          {expanded ? (
            <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
          )}
          <span className="min-w-0 truncate flex-1 select-text">{node.message}</span>
          <span className="shrink-0 font-mono text-xs text-info select-text">{shortSha(node.sha)}</span>
        </div>
        {/* Second header line: author · time, stats, refs (GitGraphCardMeta). */}
        <GitGraphCardMeta author={commit.author} date={commit.date} refs={node.refs} stats={stats} />
      </button>
      {expanded && (
        <div className="pl-7 pr-2 overflow-hidden">
          {/* Full commit message — a Markdown quote in the same capped
              scrollable box (the height estimate stays line-count based in
              gitGraphCardHeights; overflow scrolls inside), or a placeholder. */}
          {fullMessage !== undefined ? (
            <div style={{ height: messageHeight }} className="overflow-y-auto custom-scrollbar px-3 py-1 select-text">
              <Markdown compact className="prose-tooltip" content={asQuote(fullMessage)} />
            </div>
          ) : messageFailed ? (
            <PlaceholderLine textColor="text-destructive">Failed to load message</PlaceholderLine>
          ) : (
            <PlaceholderLine>
              <Loader2 className="size-3 animate-spin" /> Loading message…
            </PlaceholderLine>
          )}
          <div style={{ height: EXPANDED_TOP_PAD }} />
          {/* Changed files / empty state */}
          {filesLoading || files === undefined ? (
            <PlaceholderLine lineHeight={LOADING_LINE_HEIGHT}>
              <Loader2 className="size-3 animate-spin" /> Loading files…
            </PlaceholderLine>
          ) : files.length === 0 ? (
            <PlaceholderLine lineHeight={NO_FILES_HEIGHT}>{emptyText}</PlaceholderLine>
          ) : (
            files.map((f) => {
              const st = fileDiffs[f.path]
              return (
                <div key={f.path}>
                  <button
                    type="button"
                    onClick={() => onToggleFile(f.path)}
                    onContextMenu={onFileContextMenu ? (e) => onFileContextMenu(e, node.sha, f.path) : undefined}
                    aria-expanded={st?.expanded ?? false}
                    title={f.path}
                    className="flex w-full items-center gap-1.5 text-left"
                    style={{ height: FILE_LINE_HEIGHT }}
                  >
                    {st?.expanded ? (
                      <ChevronDown className="size-3 shrink-0 text-muted-foreground" />
                    ) : (
                      <ChevronRight className="size-3 shrink-0 text-muted-foreground" />
                    )}
                    <span className={cn('shrink-0 font-mono font-semibold', statColorClass(statBucket(f.status)))}>
                      {f.status}
                    </span>
                    <span className="min-w-0 truncate text-xs leading-none text-muted-foreground select-text">
                      {f.path}
                    </span>
                  </button>
                  {st?.expanded && (
                    <CommitFileDiff
                      filePath={f.path}
                      slice={st.slice}
                      loading={st.loading}
                      failed={st.failed}
                      diffViewMode={diffViewMode}
                      height={fileDiffAreaHeight(st)}
                    />
                  )}
                </div>
              )
            })
          )}
          <div style={{ height: EXPANDED_BOTTOM_PAD }} />
        </div>
      )}
    </div>
  )
}

/** One fixed-height placeholder line (loading / error / empty-state text). */
function PlaceholderLine({
  children,
  textColor = 'text-muted-foreground',
  lineHeight = STATUS_LINE_HEIGHT,
}: {
  children: React.ReactNode
  textColor?: string
  lineHeight?: number
}) {
  return (
    <div
      style={{ height: lineHeight }}
      className={cn('flex items-center gap-1.5 px-3 text-xs leading-4', textColor)}
    >
      {children}
    </div>
  )
}
