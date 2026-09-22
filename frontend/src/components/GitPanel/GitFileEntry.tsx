import { useCallback, useEffect, useRef, useState } from 'react'
import { File, Check, AlertTriangle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { GitFileContextMenu } from './GitFileContextMenu'
import { isMergeConflict, isUntracked, rowStatusChar } from '@/lib/gitStatus'
import type { StageSide, StageToggleHandler } from '@/lib/gitStatus'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

// --- Props ---

interface GitFileEntryProps {
  entry: GitPanelEntry
  /**
   * The porcelain axis this row represents: 'index' for a Staged Changes row,
   * 'worktree' for a Changes / Untracked Files row. Determines the checkbox
   * state, the toggle action, and the status badge character.
   */
  side: StageSide
  /** Optional workspace root path — when provided, strips it for display rendering */
  workspaceRoot?: string
  onToggle: StageToggleHandler
  onOpenDiff: (path: string) => void
}

// --- Helpers ---

/** Map git status codes to One Dark theme Tailwind text colors */
function statusColorClass(status: string): string {
  switch (status) {
    case 'M':
      return 'text-warning'
    case 'A':
      return 'text-success'
    case 'D':
      return 'text-destructive'
    case 'R':
      return 'text-info'
    case 'C':
      return 'text-hljs-keyword'
    case 'U':
      return 'text-destructive'
    default:
      return 'text-muted-foreground'
  }
}

/** Split a git file path into directory (muted) and basename (normal) parts */
function splitPathParts(filePath: string): { dir: string; name: string } {
  const lastSep = filePath.lastIndexOf('/')
  if (lastSep === -1) {
    return { dir: '', name: filePath }
  }
  return {
    dir: filePath.slice(0, lastSep + 1),
    name: filePath.slice(lastSep + 1),
  }
}

// --- Component ---

export function GitFileEntry({ entry, side, workspaceRoot, onToggle, onOpenDiff }: GitFileEntryProps) {
  const [contextMenuPos, setContextMenuPos] = useState<{ x: number; y: number } | null>(null)

  // The checkbox is server-derived (an index row is staged, a worktree row is
  // not), but a click fires an async RPC and the row is only re-classified
  // once `git:status_changed` re-loads the list. Without a local override the
  // controlled box would flash back to its pre-click value in the meantime, so
  // the intended state is held here until the server catches up. The override
  // is tagged with the server state it was applied against, so it
  // self-invalidates the moment that state changes (the row re-classified into
  // the other section, or a status refresh re-loaded it) — a stale override
  // can never outlive the data it was reacting to.
  const serverChecked = side === 'index'
  const serverKey = `${side}:${entry.indexStatus}${entry.worktreeStatus}`
  const [override, setOverride] = useState<{ key: string; value: boolean } | null>(null)
  // Drop the override whenever the porcelain pair it was applied against
  // changes. The display already ignores a mismatched key, but clearing the
  // stored value too means a stale override can never re-apply should the same
  // pair recur later while the row stays mounted.
  useEffect(() => {
    setOverride(null)
  }, [serverKey])
  const checked =
    override !== null && override.key === serverKey ? override.value : serverChecked

  // Only one stage/unstage RPC may be in flight for THIS ROW at a time — two
  // overlapping calls from the same checkbox could leave the index in the state
  // opposite to the last click (git's index.lock serializes the writes, but the
  // completion order is not guaranteed). A click during flight therefore only
  // records the desired state (`desiredRef`) and flips the box optimistically;
  // the running pump reconciles to that state once the current request settles,
  // so the final state matches the user's last click without overlapping
  // same-row requests. (Contention between different rows is not coordinated
  // here; each row guards only itself.)
  const desiredRef = useRef<boolean | null>(null)
  const pumpingRef = useRef(false)
  // Guards the one post-await state update (the failure revert) against a row
  // that unmounted mid-request (e.g. a section reclassification). Re-armed in
  // the effect body: React StrictMode runs mount→cleanup→mount in dev, so
  // setting it only in the initializer would leave it permanently false.
  const mountedRef = useRef(true)
  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  const handleToggle = useCallback(() => {
    // Flip whatever is currently rendered — the server state, or a pending
    // optimistic flip — and drive the server toward it (un-checking unstages,
    // checking stages).
    const next = !checked
    setOverride({ key: serverKey, value: next })
    desiredRef.current = next
    if (pumpingRef.current) return
    pumpingRef.current = true
    void (async () => {
      try {
        let want = desiredRef.current
        while (want !== null) {
          desiredRef.current = null
          let failed = false
          try {
            const result = await Promise.resolve(
              onToggle(entry.path, want ? 'stage' : 'unstage'),
            )
            failed = result === false
          } catch {
            // A handler that rejects is treated like a `false` return: the
            // handler itself owns recording the failure, and the optimistic
            // flip is reverted below unless a newer click is queued.
            failed = true
          }
          want = desiredRef.current
          if (failed && want === null) {
            // The request failed and no newer click is queued: revert to the
            // server state rather than retrying a rejected operation. A click
            // that arrived while the request was in flight is a newer intent,
            // so it is honoured instead — the last click still wins. Skip the
            // update if the row has since unmounted.
            if (mountedRef.current) setOverride(null)
            return
          }
        }
      } finally {
        pumpingRef.current = false
      }
    })()
  }, [checked, entry.path, onToggle, serverKey])

  const handleDoubleClick = useCallback(() => {
    onOpenDiff(entry.path)
  }, [entry.path, onOpenDiff])

  const handleContextMenu = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    setContextMenuPos({ x: e.clientX, y: e.clientY })
  }, [])

  const closeContextMenu = useCallback(() => setContextMenuPos(null), [])

  // Strip workspace root prefix from the absolute path for display.
  // Match only at a path-separator boundary to avoid a sibling directory
  // sharing the same prefix (e.g. "/repo" vs "/repo-extra").
  const displayPath =
    workspaceRoot && (entry.path === workspaceRoot || entry.path.startsWith(workspaceRoot + '/'))
      ? entry.path.slice(workspaceRoot.length).replace(/^\//, '')
      : entry.path

  const { dir, name } = splitPathParts(displayPath)
  // Untracked paths carry no per-axis change code (worktreeStatus '?'); git
  // reports them as additions, so the badge shows 'A' rather than '?'.
  const badge = isUntracked(entry) ? 'A' : rowStatusChar(entry, side)
  const statusCls = statusColorClass(badge)
  const conflict = isMergeConflict(entry)

  return (
    <>
    <div
      className={cn(
        'group flex h-7 items-center gap-1.5 px-2 hover:bg-muted/50 cursor-pointer select-none',
        conflict && 'bg-destructive/10 hover:bg-destructive/15',
      )}
      onDoubleClick={handleDoubleClick}
      onContextMenu={handleContextMenu}
    >
      {/* Checkbox */}
      <label className="relative flex items-center justify-center shrink-0 size-3.5 rounded border border-muted-foreground/40 cursor-pointer transition-colors hover:border-muted-foreground/60 has-checked:border-info has-checked:bg-info">
        <input
          type="checkbox"
          checked={checked}
          onChange={handleToggle}
          className="sr-only"
        />
        {checked && (
          <Check className="size-2.5 text-background pointer-events-none" strokeWidth={3} />
        )}
      </label>

      {/* File / conflict icon */}
      {conflict ? (
        <AlertTriangle
          className="size-3.5 shrink-0 text-destructive"
          aria-label="Merge conflict"
        />
      ) : (
        <File className="size-3.5 shrink-0 text-muted-foreground" />
      )}

      {/* File name — grows to fill remaining space so the diff stat and
          status badge are pushed to the right edge of the row. The native
          title exposes the untruncated path when the name overflows. */}
      <span
        className="min-w-0 flex-1 truncate text-sm leading-none"
        title={displayPath}
      >
        {dir && (
          <span className="text-muted-foreground/60">{dir}</span>
        )}
        <span>{name}</span>
      </span>

      {/* Diff stat — added/deleted line counts (rendered before the badge) */}
      {entry.diffStat && (
        <span className="shrink-0 flex items-center gap-1 text-[11px] leading-none font-mono">
          {entry.diffStat.added > 0 && (
            <span className="text-success">+{entry.diffStat.added}</span>
          )}
          {entry.diffStat.deleted > 0 && (
            <span className="text-destructive">-{entry.diffStat.deleted}</span>
          )}
        </span>
      )}

      {/* Status badge — change-type letter (M/A/D/R/…) */}
      <span
        className={cn(
          'shrink-0 rounded px-1.5 py-px text-[11px] font-semibold leading-none',
          statusCls,
          'bg-muted/60',
        )}
      >
        {badge}
      </span>
    </div>
    <GitFileContextMenu
      entry={entry}
      side={side}
      workspaceRoot={workspaceRoot}
      position={contextMenuPos}
      onClose={closeContextMenu}
    />
    </>
  )
}
