import { useEffect, useCallback, useMemo } from 'react'
import { cn } from '@/lib/utils'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { listDirectory, getGitStatus, watchDirectory, unwatchDirectory } from '@/api/workspace'
import { subscribe } from '@/api/runtime'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { FileIcon } from './FileIcon'
import { ChevronRight, Loader2, Regex } from 'lucide-react'
import { useFileSearch } from '@/hooks/useFileSearch'
import type { FileEntry, GitStatusEntry } from '@/types/models'

/** Propagate git status up to parent directories so folders show change indicators.
 *  A directory inherits a uniform status if all nested files share the same (status, staged)
 *  pair; otherwise it falls back to modified (M).
 */
function propagateGitStatus(gitStatus: Record<string, GitStatusEntry>): {
  status: Record<string, GitStatusEntry>
  propagated: Set<string>
} {
  const result: Record<string, GitStatusEntry> = { ...gitStatus }
  const propagated = new Set<string>()
  const dirSignatures = new Map<string, Set<string>>()

  for (const [filePath, entry] of Object.entries(gitStatus)) {
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
        result[dir] = { status: parts[0]!, staged: parts[1] === 'true' }
        break
      }
    } else {
      result[dir] = { status: 'M', staged: false }
    }
  }

  return { status: result, propagated }
}

interface TreeNodeProps {
  entry: FileEntry
  depth: number
  gitStatus: Record<string, GitStatusEntry>
  propagatedPaths: Set<string>
}

function gitColorClass(s: GitStatusEntry | undefined): string {
  if (!s) return ''
  if (s.staged) return 'text-info'
  if (s.status === 'A') return 'text-success'
  if (s.status === 'M' || s.status === 'R' || s.status === 'C' || s.status === 'U') return 'text-warning'
  return ''
}

function TreeNode({ entry, depth, gitStatus, propagatedPaths }: TreeNodeProps) {
  const expanded = useFileTreeStore((s) => s.expandedDirs[entry.path] === true)
  const loading = useFileTreeStore((s) => s.loadingDirs[entry.path] === true)
  const children = useFileTreeStore((s) => s.tree[entry.path])
  const toggleDir = useFileTreeStore((s) => s.toggleDir)
  const setEntries = useFileTreeStore((s) => s.setEntries)
  const setLoading = useFileTreeStore((s) => s.setLoading)
  const openFile = useFileViewerStore((s) => s.openFile)

  const handleClick = useCallback(async () => {
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
  }, [entry.path, entry.is_dir, expanded, children, toggleDir, setEntries, setLoading])

  const handleDoubleClick = useCallback(() => {
    if (entry.is_dir) return
    openFile(entry.path)
  }, [entry.path, entry.is_dir, openFile])

  const statusEntry = gitStatus[entry.path]
  const colorCls = gitColorClass(statusEntry)
  const isPropagated = statusEntry ? propagatedPaths.has(entry.path) : false
  const isMuted = !colorCls && (entry.hidden || entry.gitignored)
  const mutedCls = isMuted ? 'text-hljs-comment' : ''

  return (
    <>
      <div
        className="flex cursor-pointer items-center gap-0.5 py-0.5 pr-4 text-sm hover:bg-muted/40"
        style={{ paddingLeft: `${depth * 16 + 4}px` }}
        onClick={handleClick}
        onDoubleClick={handleDoubleClick}
        role="treeitem"
        aria-expanded={entry.is_dir ? expanded : undefined}
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
      {entry.is_dir && expanded && children && (
        <div role="group">
          {children.map((child) => (
            <TreeNode key={child.path} entry={child} depth={depth + 1} gitStatus={gitStatus} propagatedPaths={propagatedPaths} />
          ))}
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

  const { filterText, filterMode, handleFilterChange, toggleFilterMode } = useFileSearch()

  const { status: gitStatus, propagated: propagatedPaths } = useMemo(() => propagateGitStatus(gitStatusRaw), [gitStatusRaw])

  const workspacePath = activeProjectId && projects
    ? projects.find((p) => p.id === activeProjectId)?.workspace_path ?? null
    : null

  // Load root on project change
  useEffect(() => {
    if (!workspacePath) { clearTree(); return }
    let cancelled = false

    const activeProject = projects?.find((p) => p.id === activeProjectId)
    const isNoProject = activeProject?.is_no_project === true

    setRootPath(workspacePath)
    const ops: Promise<unknown>[] = [
      listDirectory(workspacePath).then((entries) => { if (!cancelled) setEntries(workspacePath, entries) }),
    ]
    // Skip file watching for No Project (back-end has no active watcher).
    if (!isNoProject) {
      ops.push(watchDirectory(workspacePath))
    }
    // Skip git status for No Project
    if (!isNoProject) {
      ops.push(getGitStatus(workspacePath).then((status) => { if (!cancelled) setGitStatus(status) }))
    } else {
      setGitStatus({})
    }
    Promise.all(ops).catch(() => { /* ignore */ })

    return () => {
      cancelled = true
      unwatchDirectory(workspacePath).catch(() => { })
    }
  }, [workspacePath, activeProjectId, projects, clearTree, setRootPath, setEntries, setGitStatus])

  // Refresh on workspace:tree_changed
  useEffect(() => {
    const unsub = subscribe('workspace:tree_changed', () => {
      const rp = useFileTreeStore.getState().rootPath
      if (!rp) return
      listDirectory(rp).then((entries) => setEntries(rp, entries)).catch(() => { })
      const activeProject = useProjectStore.getState().projects?.find(
        (p) => p.id === useProjectStore.getState().activeProjectId
      )
      if (activeProject?.is_no_project !== true) {
        getGitStatus(rp).then(setGitStatus).catch(() => { })
      }
    })
    return unsub
  }, [setEntries, setGitStatus])

  const isFiltering = filterText.trim().length > 0
  const displayEntries = isFiltering ? searchEntries : rootEntries

  return (
    <div className="flex flex-col flex-1 overflow-hidden">
      <div className="flex shrink-0 gap-1 border-b border-border px-2 py-1">
        <Input
          value={filterText}
          onChange={(e) => handleFilterChange(e.target.value)}
          placeholder={"Filter files... (" + filterMode + ")"}
          className="h-7 text-xs"
        />
        <Button
          variant={filterMode === 'regex' ? 'default' : 'ghost'}
          size="sm"
          onClick={toggleFilterMode}
          className="h-7 px-2"
          title={filterMode === 'glob' ? 'Switch to regex' : 'Switch to glob'}
        >
          <Regex className="size-3.5" />
        </Button>
      </div>
      {displayEntries && displayEntries.length > 0 ? (
        <div className="custom-scrollbar flex-1 overflow-y-auto py-1" role="tree">
          {displayEntries.map((entry) => (
            <TreeNode key={entry.path} entry={entry} depth={0} gitStatus={gitStatus} propagatedPaths={propagatedPaths} />
          ))}
        </div>
      ) : isFiltering ? (
        <p className="flex-1 p-4 text-center text-xs text-muted-foreground">No matching files</p>
      ) : rootEntries ? (
        <p className="flex-1 p-4 text-center text-xs text-muted-foreground">Empty directory</p>
      ) : (
        <p className="flex-1 p-4 text-center text-xs text-muted-foreground">
          {activeProjectId ? 'Loading workspace...' : 'Select a project to browse files'}
        </p>
      )}
    </div>
  )
}
