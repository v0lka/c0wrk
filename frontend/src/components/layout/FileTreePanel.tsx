import { useEffect, useCallback, useMemo, useState, useRef } from 'react'
import { cn } from '@/lib/utils'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { selectionPersisted } from '@/stores/sessionStore'
import {
  listDirectory,
  getGitStatusLite,
  watchDirectory,
  unwatchDirectory,
  createFile,
  createDirectory,
  renameEntry,
  deleteEntry,
  revealInFileManager,
} from '@/api/workspace'
import { subscribe, emit } from '@/api/runtime'
import { useWorkspaceRootState } from '@/hooks/useWorkspacePath'
import { joinTreePath, parentTreeDir, validateEntryName } from '@/lib/fileTreeNames'
import { generateUUID } from '@/lib/ids'
import { logger } from '@/lib/logger'
import { FilterBar } from '@/components/ui/FilterBar'
import { Button } from '@/components/ui/button'
import { FileIcon } from './FileIcon'
import { FileTreeContextMenu } from './FileTreeContextMenu'
import { FileTreeInlineInput, type InlineEditMode } from './FileTreeInlineInput'
import { FileTreeDeleteConfirmDialog } from './FileTreeDeleteConfirmDialog'
import { FileTreeToolbar } from './FileTreeToolbar'
import { ChevronRight, Loader2, FolderTree, RotateCw, TriangleAlert } from 'lucide-react'
import { useFileSearch } from '@/hooks/useFileSearch'
import type { FileEntry, GitStatusEntry } from '@/types/models'

/** Windows separator normalization shared by the lite-status helpers below:
 *  both sides of every lookup are normalized to slash form before compare. */
function normalizeStatusPath(p: string): string {
  return p.replace(/\\/g, '/')
}

/** The inline create/rename input's transient state — owned by the panel,
 *  rendered inside the tree at the target location (issue #60). */
interface InlineEditState {
  parentDir: string
  initialName: string
  mode: InlineEditMode
  /** The rename source; absent for creation. */
  targetPath?: string
  busy: boolean
  /** Server-side error from the last failed commit; null when clean. */
  error: string | null
}

/** Human-readable message for a rejected RPC (Wails rejections arrive as
 *  Error objects or plain strings). */
function describeRpcError(err: unknown): string {
  if (err instanceof Error && err.message) return err.message
  if (typeof err === 'string' && err) return err
  return 'Operation failed'
}
/** Propagate git status up to parent directories so folders show change indicators.
 *  A directory inherits a uniform status if all nested files share the same (status, staged)
 *  pair; otherwise it falls back to modified (M).
 *
 *  The status map comes from the LITE source: untracked directories arrive as
 *  one collapsed entry (worktree_status '?') instead of a per-file enumeration,
 *  so the directories carrying an untracked verdict are returned separately —
 *  the tree inherits that verdict down to the entries it actually lists (a
 *  fresh untracked module keeps its green badges without paying the
 *  untracked-file enumeration on the git side).
 *
 *  Both the lite status keys and the tree entry paths are built with Go
 *  filepath.Join on the backend, so on Windows they carry backslash
 *  separators while every parent walk and map lookup below is slash-based —
 *  keys are normalized once here (mirroring isPathCovered in
 *  useFileViewerData.ts) so lookups and the walk meet in slash form.
 */
function propagateGitStatus(gitStatus: Record<string, GitStatusEntry>): {
  status: Record<string, GitStatusEntry>
  propagated: Set<string>
  untrackedDirs: Set<string>
} {
  const result: Record<string, GitStatusEntry> = {}
  const propagated = new Set<string>()
  const untrackedDirs = new Set<string>()
  const dirSignatures = new Map<string, Set<string>>()

  for (const [rawPath, entry] of Object.entries(gitStatus)) {
    const filePath = normalizeStatusPath(rawPath)
    result[filePath] = entry
    if (entry.worktree_status === '?') {
      untrackedDirs.add(filePath)
    }
    let dir = filePath
    while ((dir = dir.substring(0, dir.lastIndexOf('/'))) && dir) {
      let sigs = dirSignatures.get(dir)
      if (!sigs) {
        sigs = new Set<string>()
        dirSignatures.set(dir, sigs)
      }
      sigs.add(`${entry.status}:${entry.staged}`)

      if (!result[dir]) {
        propagated.add(dir)
      }
    }
  }

  for (const [dir, signatures] of dirSignatures) {
    if (signatures.size === 1) {
      for (const sig of signatures) {
        const parts = sig.split(':')
          result[dir] = { status: parts[0]!, staged: parts[1] === 'true', index_status: parts[1] === 'true' ? parts[0]! : '', worktree_status: parts[1] === 'true' ? '' : parts[0]! }
        break
      }
    } else {
      result[dir] = { status: 'M', staged: false, index_status: '', worktree_status: 'M' }
    }
  }

  return { status: result, propagated, untrackedDirs }
}

/** Status lookup for one tree entry: the exact map entry, or — when the entry
 *  sits beneath a collapsed untracked directory — the inherited untracked
 *  verdict. Nothing nests beneath a file path, so a '?' on a file entry can
 *  never match a descendant and the prefix walk stays exact. */
function lookupGitStatus(
  rawPath: string,
  gitStatus: Record<string, GitStatusEntry>,
  untrackedDirs: Set<string>,
): GitStatusEntry | undefined {
  const path = normalizeStatusPath(rawPath)
  const exact = gitStatus[path]
  if (exact) return exact
  let dir = path
  while ((dir = dir.substring(0, dir.lastIndexOf('/'))) && dir) {
    if (untrackedDirs.has(dir)) {
      return { status: 'A', staged: false, index_status: '', worktree_status: '?' }
    }
  }
  return undefined
}

interface TreeNodeProps {
  entry: FileEntry
  depth: number
  gitStatus: Record<string, GitStatusEntry>
  propagatedPaths: Set<string>
  untrackedDirs: Set<string>
  onContextMenu: (entry: FileEntry, x: number, y: number) => void
  /** The panel-owned inline input state; rendered at its target location. */
  inlineEdit: InlineEditState | null
  onInlineCommit: (name: string) => void
  onInlineCancel: () => void
}

function gitColorClass(s: GitStatusEntry | undefined): string {
  if (!s) return ''
  if (s.staged) return 'text-info'
  if (s.status === 'A') return 'text-success'
  if (s.status === 'M' || s.status === 'R' || s.status === 'C' || s.status === 'U') return 'text-warning'
  return ''
}

/**
 * Collect the directory paths that must be reloaded when the workspace tree
 * changes: the root plus every expanded subdirectory, deduplicated. Reloading
 * only the root leaves expanded folders stale — new/removed/renamed files
 * inside an open folder never appear until the user collapses and re-expands
 * it.
 */
// eslint-disable-next-line react-refresh/only-export-components
export function collectDirsToReload(
  rootPath: string,
  expandedDirs: Record<string, true>,
): string[] {
  return Array.from(new Set([rootPath, ...Object.keys(expandedDirs)]))
}

/** Human-readable message for a rejected listDirectory/getGitStatus call.
 *  Wails rejections arrive as Error objects or plain strings. */
// eslint-disable-next-line react-refresh/only-export-components
export function describeListError(err: unknown): string {
  if (err instanceof Error && err.message) return err.message
  if (typeof err === 'string' && err) return err
  return 'Failed to list directory'
}

/**
 * Reload the root directory and every expanded subdirectory from the current
 * store state. Shared by `workspace:tree_changed` (via reloadWorkspaceTree),
 * `workspace:ignore_ready` (flags arrived without a tree change — listings
 * only, no git status), the error-state Retry action and the manual Refresh
 * button. A root listing failure is surfaced via `rootLoadError` (with the
 * message) instead of being cached as an empty entry list — the old behavior
 * rendered a silently "empty" workspace that never recovered, because a
 * static workspace emits no tree_changed events.
 */
// eslint-disable-next-line react-refresh/only-export-components
export async function reloadExpandedDirs(): Promise<void> {
  const state = useFileTreeStore.getState()
  const rootPath = state.rootPath
  if (!rootPath) return
  const dirs = collectDirsToReload(rootPath, state.expandedDirs)
  await Promise.all(
    dirs.map((dir) =>
      listDirectory(dir)
        .then((entries) => {
          state.setEntries(dir, entries)
          if (dir === rootPath) state.setRootLoadError(null)
        })
        .catch((err) => {
          // Nested dirs may have been removed — only the root failure is
          // user-visible state; nested failures keep their cached children.
          if (dir === rootPath) state.setRootLoadError(describeListError(err))
        }),
    ),
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export async function reloadWorkspaceTree(): Promise<void> {
  await reloadExpandedDirs()
  const rootPath = useFileTreeStore.getState().rootPath
  if (!rootPath) return
  const projectState = useProjectStore.getState()
  const activeProject = projectState.projects?.find((p) => p.id === projectState.activeProjectId)
  if (activeProject?.is_no_project !== true) {
    getGitStatusLite(rootPath)
      .then(useFileTreeStore.getState().setGitStatus)
      .catch(() => { })
  }
}

function TreeNode({ entry, depth, gitStatus, propagatedPaths, untrackedDirs, onContextMenu, inlineEdit, onInlineCommit, onInlineCancel }: TreeNodeProps) {
  const expanded = useFileTreeStore((s) => s.expandedDirs[entry.path] === true)
  const loading = useFileTreeStore((s) => s.loadingDirs[entry.path] === true)
  const children = useFileTreeStore((s) => s.tree[entry.path])
  const toggleDir = useFileTreeStore((s) => s.toggleDir)
  const setEntries = useFileTreeStore((s) => s.setEntries)
  const setLoading = useFileTreeStore((s) => s.setLoading)
  const isSelected = useFileTreeStore((s) => s.selectedPath === entry.path)
  const setSelectedPath = useFileTreeStore((s) => s.setSelectedPath)
  const openFile = useFileViewerStore((s) => s.openFile)
  // Sibling names for the inline input's collision pre-check: a direct store
  // reference (never a fresh array inside the selector).
  const inlineSiblingsRaw = useFileTreeStore((s) => (inlineEdit ? s.tree[inlineEdit.parentDir] : undefined))
  const inlineSiblings = useMemo(() => inlineSiblingsRaw?.map((e) => e.name) ?? [], [inlineSiblingsRaw])
  const inlineTargetsEntry =
    inlineEdit !== null &&
    (inlineEdit.mode === 'rename'
      ? inlineEdit.targetPath === entry.path
      : inlineEdit.parentDir === entry.path)

  // When this node becomes the transient "selected" target (set by
  // "Reveal in Workspace"), scroll it into view so the user actually sees it.
  const rowRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (isSelected && rowRef.current) {
      rowRef.current.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    }
  }, [isSelected])

  const handleClick = useCallback(async () => {
    // Clear the transient reveal selection once the user interacts with the tree.
    if (useFileTreeStore.getState().selectedPath !== null) {
      setSelectedPath(null)
    }
    if (!entry.is_dir) return
    const willExpand = !expanded
    toggleDir(entry.path)
    if (willExpand && !children) {
      setLoading(entry.path, true)
      try {
        const entries = await listDirectory(entry.path)
        setEntries(entry.path, entries)
      } catch {
        // ignore
      } finally {
        setLoading(entry.path, false)
      }
    }
  }, [entry.path, entry.is_dir, expanded, children, toggleDir, setEntries, setLoading, setSelectedPath])

  const handleDoubleClick = useCallback(() => {
    if (entry.is_dir) return
    openFile(entry.path)
  }, [entry.path, entry.is_dir, openFile])

  const handleContextMenu = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault()
      e.stopPropagation()
      onContextMenu(entry, e.clientX, e.clientY)
    },
    [entry, onContextMenu],
  )

  const pathKey = normalizeStatusPath(entry.path)
  const statusEntry = lookupGitStatus(pathKey, gitStatus, untrackedDirs)
  const colorCls = gitColorClass(statusEntry)
  const isPropagated = statusEntry ? propagatedPaths.has(pathKey) : false
  const isMuted = !colorCls && (entry.hidden || entry.gitignored)
  const mutedCls = isMuted ? 'text-hljs-comment' : ''
  const renaming = inlineEdit !== null && inlineEdit.mode === 'rename' && inlineTargetsEntry

  return (
    <>
      {renaming && inlineEdit ? (
        <FileTreeInlineInput
          mode="rename"
          initialName={inlineEdit.initialName}
          depth={depth}
          busy={inlineEdit.busy}
          error={inlineEdit.error}
          siblings={inlineSiblings.filter((n) => n !== inlineEdit.initialName)}
          onCommit={onInlineCommit}
          onCancel={onInlineCancel}
        />
      ) : (
        <div
          ref={rowRef}
          className={cn(
            "flex cursor-pointer items-center gap-0.5 py-0.5 pr-4 text-sm hover:bg-muted/40",
            isSelected && "bg-primary/10 hover:bg-primary/10",
          )}
          style={{ paddingLeft: `${depth * 16 + 4}px` }}
          onClick={handleClick}
          onDoubleClick={handleDoubleClick}
          onContextMenu={handleContextMenu}
          role="treeitem"
          aria-expanded={entry.is_dir ? expanded : undefined}
          aria-current={isSelected ? 'true' : undefined}
        >
          {entry.is_dir ? (
            loading ? (
              <Loader2 className="size-3.5 shrink-0 animate-spin text-muted-foreground" />
            ) : (
              <ChevronRight
                className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', expanded && 'rotate-90')}
              />
            )
          ) : (
            <span className="w-3.5 shrink-0" />
          )}
          {!entry.is_dir && <FileIcon icon={entry.icon} iconColor={entry.icon_color} className="shrink-0" />}
          <span className={cn('truncate ml-1', colorCls, mutedCls)}>{entry.name}</span>
          {statusEntry && colorCls && (
            <span className={cn('ml-auto shrink-0 font-bold text-xs', colorCls)}>
              {isPropagated ? '\u2022' : statusEntry.status}
            </span>
          )}
        </div>
      )}
      {entry.is_dir && expanded && children && (
        <div role="group">
          {children.map((child) => (
            <TreeNode key={child.path} entry={child} depth={depth + 1} gitStatus={gitStatus} propagatedPaths={propagatedPaths} untrackedDirs={untrackedDirs} onContextMenu={onContextMenu} inlineEdit={inlineEdit} onInlineCommit={onInlineCommit} onInlineCancel={onInlineCancel} />
          ))}
          {inlineEdit !== null && inlineEdit.mode !== 'rename' && inlineEdit.parentDir === entry.path && (
            <FileTreeInlineInput
              mode={inlineEdit.mode}
              initialName={inlineEdit.initialName}
              depth={depth + 1}
              busy={inlineEdit.busy}
              error={inlineEdit.error}
              siblings={inlineSiblings}
              onCommit={onInlineCommit}
              onCancel={onInlineCancel}
            />
          )}
        </div>
      )}
    </>
  )
}

export function FileTreePanel() {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const projects = useProjectStore((s) => s.projects)
  const rootPath = useFileTreeStore((s) => s.rootPath)
  const rootEntries = useFileTreeStore((s) => (rootPath ? s.tree[rootPath] : undefined))
  const gitStatusRaw = useFileTreeStore((s) => s.gitStatus)
  const setRootPath = useFileTreeStore((s) => s.setRootPath)
  const setEntries = useFileTreeStore((s) => s.setEntries)
  const setGitStatus = useFileTreeStore((s) => s.setGitStatus)
  const clearTree = useFileTreeStore((s) => s.clearTree)
  const searchEntries = useFileTreeStore((s) => s.searchEntries)
  const rootLoadError = useFileTreeStore((s) => s.rootLoadError)
  const setRootLoadError = useFileTreeStore((s) => s.setRootLoadError)

  const [contextMenu, setContextMenu] = useState<{ entry: FileEntry; x: number; y: number; rootMode: boolean } | null>(null)
  const [inlineEdit, setInlineEdit] = useState<InlineEditState | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<FileEntry | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const activeSessionId = useSessionStore((s) => s.activeSessionId)

  const handleContextMenu = useCallback((entry: FileEntry, x: number, y: number) => {
    setContextMenu({ entry, x, y, rootMode: false })
  }, [])

  const handleCloseContextMenu = useCallback(() => setContextMenu(null), [])

  // --- Tree mutations (issue #60). Every success path refreshes through the
  // watcher alone: the backend's directory watcher emits workspace:tree_changed
  // and the tree reloads its expanded dirs — a parallel in-place store update
  // would only double the reloads. Failures surface inline and keep the edit
  // open; the backend stays the containment/collision authority.
  const sessionId = activeSessionId ?? ''

  const expandAndLoad = useCallback(async (dir: string) => {
    const state = useFileTreeStore.getState()
    if (!state.expandedDirs[dir]) state.toggleDir(dir)
    if (!useFileTreeStore.getState().tree[dir]) {
      state.setLoading(dir, true)
      try {
        state.setEntries(dir, await listDirectory(dir))
      } catch {
        // ignore — the row shows its cached/empty children
      } finally {
        useFileTreeStore.getState().setLoading(dir, false)
      }
    }
  }, [])

  const handleRequestCreate = useCallback(
    (parentDir: string, kind: 'file' | 'folder') => {
      setInlineEdit({ parentDir, initialName: '', mode: kind, busy: false, error: null })
      // A collapsed target must open so the input (rendered inside its
      // children group) becomes visible.
      if (parentDir !== useFileTreeStore.getState().rootPath) void expandAndLoad(parentDir)
    },
    [expandAndLoad],
  )

  const handleRequestRename = useCallback((entry: FileEntry) => {
    setInlineEdit({
      parentDir: parentTreeDir(entry.path),
      initialName: entry.name,
      mode: 'rename',
      targetPath: entry.path,
      busy: false,
      error: null,
    })
  }, [])

  const handleRequestDelete = useCallback((entry: FileEntry) => {
    setDeleteError(null)
    setDeleteTarget(entry)
  }, [])

  const handleInlineCommit = useCallback(
    async (name: string) => {
      if (!inlineEdit || inlineEdit.busy) return
      // Pre-flight with the CURRENT siblings (the backend re-checks
      // authoritatively): a rename must not collide with itself.
      const siblings = (useFileTreeStore.getState().tree[inlineEdit.parentDir] ?? [])
        .map((e) => e.name)
        .filter((n) => inlineEdit.mode !== 'rename' || n !== inlineEdit.initialName)
      const invalid = validateEntryName(name, siblings)
      if (invalid) {
        setInlineEdit((e) => (e ? { ...e, error: invalid } : e))
        return
      }
      const createdPath = joinTreePath(inlineEdit.parentDir, name)
      setInlineEdit((e) => (e ? { ...e, busy: true, error: null } : e))
      try {
        if (inlineEdit.mode === 'file') {
          await createFile(sessionId, createdPath)
        } else if (inlineEdit.mode === 'folder') {
          await createDirectory(sessionId, createdPath)
        } else {
          await renameEntry(sessionId, inlineEdit.targetPath ?? '', createdPath)
        }
        setInlineEdit(null)
      } catch (err) {
        logger.error('Tree mutation failed:', err)
        setInlineEdit((e) => (e ? { ...e, busy: false, error: describeRpcError(err) } : e))
      }
    },
    [inlineEdit, sessionId],
  )

  const handleInlineCancel = useCallback(() => setInlineEdit(null), [])

  const handleConfirmDelete = useCallback(async () => {
    if (!deleteTarget) return
    setDeleteBusy(true)
    setDeleteError(null)
    try {
      await deleteEntry(sessionId, deleteTarget.path)
      setDeleteTarget(null)
    } catch (err) {
      logger.error('Tree delete failed:', err)
      setDeleteError(describeRpcError(err))
    } finally {
      setDeleteBusy(false)
    }
  }, [deleteTarget, sessionId])

  const handleCloseDeleteDialog = useCallback(() => {
    if (deleteBusy) return
    setDeleteTarget(null)
    setDeleteError(null)
  }, [deleteBusy])

  const handleRevealContaining = useCallback(
    (entry: FileEntry) => {
      revealInFileManager(sessionId, 'workspace', entry.path).catch((err) => {
        logger.error('Failed to open containing folder:', err)
        emit('runtime_error', {
          id: generateUUID(),
          message: 'Failed to open the containing folder',
        })
      })
    },
    [sessionId],
  )

  const handleBackgroundContextMenu = useCallback(
    (e: React.MouseEvent) => {
      // Rows and the inline input own their context behavior (their handlers
      // fire first); the background — including the empty-workspace
      // placeholder — opens the root create menu (the tree renders no root
      // row, so creation at the workspace root enters here).
      if (!rootPath) return
      const target = e.target as HTMLElement
      if (target.closest('[role="treeitem"], [data-testid="file-tree-inline-input"]')) return
      e.preventDefault()
      setContextMenu({ entry: { name: '', path: rootPath, is_dir: true }, x: e.clientX, y: e.clientY, rootMode: true })
    },
    [rootPath],
  )

  const { filterText, filterMode, isInvalidFilter, handleFilterChange, toggleFilterMode } = useFileSearch()

  const { status: gitStatus, propagated: propagatedPaths, untrackedDirs } = useMemo(() => propagateGitStatus(gitStatusRaw), [gitStatusRaw])

  const isNoProject = useMemo(() => {
    return projects?.find((p) => p.id === activeProjectId)?.is_no_project === true
  }, [activeProjectId, projects])

  // The workspace root follows the ACTIVE SESSION (specs/domains/
  // session-worktrees.md, User-facing workspace roots) through the SHARED
  // resolver — the same binding-first rule the chat links and the file
  // viewer resolve with, so the three surfaces cannot drift.
  //
  // While the root is PENDING (a session is active and its workspace has not
  // settled — a managed binding whose path has not landed, a session missing
  // from the list, or a No Project session whose RPC is in flight) the panel
  // shows NO tree instead of falling back to the project-level path: a
  // checkout-rooted flash would re-scope the backend watcher onto the WRONG
  // root (tearing down the live one and dropping its events until the next
  // change) and list/git-status the checkout for nothing. The No Project
  // contract was already strict — "a CHAT session whose root has not
  // resolved yet simply shows no tree" — and the same deferral now covers
  // managed sessions, so each mount/switch causes at most one re-scope, to
  // the correct root.
  const { path: resolvedRoot, pending: rootPending } = useWorkspaceRootState()
  const workspacePath = rootPending ? null : resolvedRoot

  // Load root on project or session change
  useEffect(() => {
    if (!workspacePath) { clearTree(); return }
    let cancelled = false

    setRootPath(workspacePath)
    const ops: Promise<unknown>[] = [
      listDirectory(workspacePath)
        .then((entries) => {
          if (cancelled) return
          // Guard: never clobber a snapshot-hydrated root with an EMPTY
          // listing. Immediately after a project switch the backend may not
          // have finished activating the project yet, and an empty response is
          // indistinguishable from that transient state — the hydrated tree is
          // the better data. A genuinely emptied workspace still self-heals on
          // the next workspace:tree_changed reload (which writes unconditionally).
          const hydratedRoot = useFileTreeStore.getState().tree[workspacePath]
          if (entries.length === 0 && hydratedRoot && hydratedRoot.length > 0) return
          setEntries(workspacePath, entries)
          setRootLoadError(null)
        })
        .catch((err) => {
          // Surface the failure instead of caching an empty listing: a
          // silently empty tree never self-heals (a static workspace emits
          // no tree_changed events) and offers no recovery affordance.
          if (!cancelled) setRootLoadError(describeListError(err))
        }),
    ]
    // Start watching the workspace root for changes. Ordered AFTER the
    // project's selection persist: the backend's WatchDirectory freshness
    // check resolves the active session from the PERSISTED selection
    // (project_ui_state.saved_session_id, ADR-101), so a watch sent while the
    // persist of the selection this root is based on is still in flight could
    // be (correctly) skipped as stale — awaiting it first keeps the re-scope
    // on the tree the user actually selected.
    ops.push(selectionPersisted(activeProjectId).then(() => watchDirectory(workspacePath)))
    // Skip git status for No Project
    if (!isNoProject) {
      ops.push(getGitStatusLite(workspacePath).then((status) => { if (!cancelled) setGitStatus(status) }))
    } else {
      setGitStatus({})
    }
    Promise.all(ops).catch(() => { /* ignore */ })

    return () => {
      cancelled = true
      unwatchDirectory(workspacePath).catch(() => { })
    }
  }, [workspacePath, isNoProject, activeProjectId, clearTree, setRootPath, setEntries, setRootLoadError, setGitStatus])

  // Refresh on workspace:tree_changed — reload the root AND every expanded
  // subdirectory so changes inside open folders are reflected, not just the
  // top level. Previously only the root was reloaded, leaving expanded
  // subdirectories stale: new/removed/renamed files inside an open folder
  // never appeared until the user collapsed and re-expanded it.
  useEffect(() => {
    const unsub = subscribe('workspace:tree_changed', () => {
      void reloadWorkspaceTree()
    })
    return unsub
  }, [])

  // Ignore flags may arrive AFTER the listing that needs them: a not-yet-built
  // ignore resolver ships .aiignore flags empty and the backend delivers this
  // event once the walk lands. Reload the listings (no git status — the flags
  // are the only delta), but only for the root the event names: a stale event
  // from a previous project must not refetch the current tree.
  useEffect(() => {
    const unsub = subscribe('workspace:ignore_ready', (root: unknown) => {
      const state = useFileTreeStore.getState()
      if (typeof root === 'string' && state.rootPath && !state.rootPath.startsWith(root)) return
      void reloadExpandedDirs()
    })
    return unsub
  }, [])

  const isFiltering = filterText.trim().length > 0
  const displayEntries = isFiltering ? searchEntries : rootEntries
  // The inline input lives inside the real tree layout; a filtered view
  // reorders/flattens entries, so the edit input hides while filtering (its
  // state survives — clearing the filter brings it back).
  const inlineInputAtRoot =
    inlineEdit !== null &&
    inlineEdit.mode !== 'rename' &&
    !isFiltering &&
    rootPath !== null &&
    inlineEdit.parentDir === rootPath
  // The tree container renders for a LOADED-EMPTY root too: creation at the
  // workspace root arrives through the background context menu, which needs a
  // right-clickable surface even when nothing is listed yet.
  const showTree =
    displayEntries !== undefined && (displayEntries.length > 0 || (!isFiltering && rootPath !== null))

  return (
    <div className="flex flex-col flex-1 overflow-hidden">
      <FilterBar
        value={filterText}
        onChange={handleFilterChange}
        mode={filterMode}
        onToggleMode={toggleFilterMode}
        placeholder="Filter files"
        leftSlot={<FileTreeToolbar workspaceReady={workspacePath !== null} />}
      />
      {isInvalidFilter ? (
        <p className="flex-1 p-4 text-center text-xs text-destructive">Invalid regex</p>
      ) : showTree ? (
        <div
          className="custom-scrollbar flex flex-1 flex-col overflow-y-auto py-1"
          role="tree"
          onContextMenu={handleBackgroundContextMenu}
        >
          {(displayEntries ?? []).map((entry) => (
            <TreeNode key={entry.path} entry={entry} depth={0} gitStatus={gitStatus} propagatedPaths={propagatedPaths} untrackedDirs={untrackedDirs} onContextMenu={handleContextMenu} inlineEdit={inlineEdit} onInlineCommit={(name) => void handleInlineCommit(name)} onInlineCancel={handleInlineCancel} />
          ))}
          {inlineInputAtRoot && inlineEdit && (
            <RootInlineInputSlot edit={inlineEdit} onCommit={(name) => void handleInlineCommit(name)} onCancel={handleInlineCancel} />
          )}
          {displayEntries !== undefined && displayEntries.length === 0 && !isFiltering && !inlineInputAtRoot && (
            <div className="flex flex-1 items-center justify-center">
              <FolderTree className="size-12 text-muted-foreground/30" />
            </div>
          )}
        </div>
      ) : isFiltering ? (
        <p className="flex-1 p-4 text-center text-xs text-muted-foreground">No matching files</p>
      ) : rootLoadError ? (
        <div className="flex-1 flex flex-col items-center justify-center gap-2 p-4 text-center">
          <TriangleAlert className="size-8 text-destructive/60" />
          <p className="max-w-full break-words text-xs text-muted-foreground">{rootLoadError}</p>
          <Button
            variant="outline"
            size="sm"
            onClick={() => { void reloadWorkspaceTree() }}
          >
            <RotateCw className="size-3.5" />
            Retry
          </Button>
        </div>
      ) : activeProjectId ? (
        <div className="flex-1 flex items-center justify-center">
          <FolderTree className="size-12 text-muted-foreground/30" />
        </div>
      ) : (
        <p className="flex-1 p-4 text-center text-xs text-muted-foreground">
          Select a project to browse files
        </p>
      )}
      <FileTreeContextMenu
        entry={contextMenu?.entry ?? dummyEntry}
        workspaceRoot={rootPath}
        position={contextMenu ? { x: contextMenu.x, y: contextMenu.y } : null}
        onClose={handleCloseContextMenu}
        rootMode={contextMenu?.rootMode ?? false}
        onRequestCreate={handleRequestCreate}
        onRequestRename={handleRequestRename}
        onRequestDelete={handleRequestDelete}
        onRevealContaining={handleRevealContaining}
      />
      <FileTreeDeleteConfirmDialog
        target={deleteTarget}
        deleting={deleteBusy}
        deleteError={deleteError}
        onConfirm={() => void handleConfirmDelete()}
        onClose={handleCloseDeleteDialog}
      />
    </div>
  )
}

/** The root-level inline input (creation at the workspace root): rendered
 *  after the root entries, aligned at depth 0. Siblings come from the store's
 *  root listing. */
function RootInlineInputSlot({
  edit,
  onCommit,
  onCancel,
}: {
  edit: InlineEditState
  onCommit: (name: string) => void
  onCancel: () => void
}) {
  const rootEntriesRaw = useFileTreeStore((s) => (s.rootPath ? s.tree[s.rootPath] : undefined))
  const siblings = useMemo(() => rootEntriesRaw?.map((e) => e.name) ?? [], [rootEntriesRaw])
  return (
    <FileTreeInlineInput
      mode={edit.mode}
      initialName={edit.initialName}
      depth={0}
      busy={edit.busy}
      error={edit.error}
      siblings={siblings}
      onCommit={onCommit}
      onCancel={onCancel}
    />
  )
}

/** Placeholder entry used when the context menu is closed (position is null,
 *  so the menu renders nothing). Avoids conditional rendering of the menu
 *  component and keeps hook order stable. */
const dummyEntry: FileEntry = {
  name: '',
  path: '',
  is_dir: false,
}
