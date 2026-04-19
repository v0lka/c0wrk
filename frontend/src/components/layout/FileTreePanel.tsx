import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { ChevronRight, Loader2, X, Regex, Asterisk } from 'lucide-react'
import picomatch from 'picomatch'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useFileTreeStore, type FileNode } from '@/stores/fileTreeStore'
import { useProjectStore } from '@/stores/projectStore'
import { FileIcon } from './FileIcon'

type FilterMode = 'glob' | 'regex'

function buildMatcher(
  filterText: string,
  filterMode: FilterMode,
): ((name: string) => boolean) | null {
  const trimmed = filterText.trim()
  if (!trimmed) return null

  if (filterMode === 'glob') {
    try {
      const isMatch = picomatch(trimmed, { nocase: true })
      return isMatch
    } catch {
      return () => false
    }
  }

  // regex mode
  try {
    const re = new RegExp(trimmed, 'i')
    return (name: string) => re.test(name)
  } catch {
    return () => false
  }
}

/**
 * Computes visibility and match info for the full recursive tree when a filter is active.
 *
 * Returns:
 * - matchedPaths: set of node paths that directly match the filter
 * - visiblePaths: set of node paths that should be rendered (matches + ancestors)
 * - expandedByFilter: set of directory paths that should be force-expanded
 */
function useRecursiveFilterState(
  recursiveEntries: Record<string, FileNode[]>,
  rootPath: string,
  matcher: ((name: string) => boolean) | null,
) {
  return useMemo(() => {
    if (!matcher || !rootPath) return null

    const matchedPaths = new Set<string>()
    const visiblePaths = new Set<string>()
    const expandedByFilter = new Set<string>()

    // First pass: find all directly matched nodes
    for (const children of Object.values(recursiveEntries)) {
      for (const node of children) {
        if (matcher(node.name)) {
          matchedPaths.add(node.path)
        }
      }
    }

    // For each matched node, mark ancestors visible & expanded
    for (const matchedPath of matchedPaths) {
      visiblePaths.add(matchedPath)

      // Walk up the path to mark ancestors
      let current = matchedPath
      while (true) {
        const lastSep = current.lastIndexOf('/')
        if (lastSep <= 0) break
        const parent = current.substring(0, lastSep)
        if (parent === rootPath || parent.length < rootPath.length) break
        visiblePaths.add(parent)
        expandedByFilter.add(parent)
        current = parent
      }

      // If matched node is a directory, expand it and show all descendants
      if (recursiveEntries[matchedPath]) {
        expandedByFilter.add(matchedPath)
        markAllDescendants(matchedPath, recursiveEntries, visiblePaths, expandedByFilter)
      }
    }

    return { matchedPaths, visiblePaths, expandedByFilter }
  }, [recursiveEntries, rootPath, matcher])
}

/** Recursively mark all descendants of a directory as visible, expanding subdirectories */
function markAllDescendants(
  dirPath: string,
  entries: Record<string, FileNode[]>,
  visiblePaths: Set<string>,
  expandedByFilter: Set<string>,
) {
  const children = entries[dirPath]
  if (!children) return
  for (const child of children) {
    visiblePaths.add(child.path)
    if (child.is_dir) {
      expandedByFilter.add(child.path)
      markAllDescendants(child.path, entries, visiblePaths, expandedByFilter)
    }
  }
}

/** Visibility computation for the non-filtered (lazy) tree — unchanged from original */
function useNodeVisibility(
  entries: Record<string, FileNode[]>,
  matcher: ((name: string) => boolean) | null,
) {
  return useMemo(() => {
    if (!matcher) return null

    const visible = new Set<string>()

    function walk(nodes: FileNode[] | undefined): boolean {
      if (!nodes) return false
      let any = false
      for (const node of nodes) {
        const selfMatch = matcher!(node.name)
        const childMatch = node.is_dir ? walk(entries[node.path]) : false
        if (selfMatch || childMatch) {
          visible.add(node.path)
          any = true
        }
      }
      return any
    }

    for (const key of Object.keys(entries)) {
      walk(entries[key])
    }

    return visible
  }, [entries, matcher])
}

interface TreeNodeProps {
  node: FileNode
  depth: number
  isFilterActive: boolean
  matchedPaths: Set<string> | null
  visiblePaths: Set<string> | null
  expandedByFilter: Set<string> | null
  entriesSource: Record<string, FileNode[]>
}

function TreeNode({
  node,
  depth,
  isFilterActive,
  matchedPaths,
  visiblePaths,
  expandedByFilter,
  entriesSource,
}: TreeNodeProps) {
  const expandedDirs = useFileTreeStore((s) => s.expandedDirs)
  const loadingDirs = useFileTreeStore((s) => s.loadingDirs)
  const entries = useFileTreeStore((s) => s.entries)
  const toggleDir = useFileTreeStore((s) => s.toggleDir)

  const isMatch = isFilterActive && matchedPaths !== null && matchedPaths.has(node.path)
  const forceExpanded = isFilterActive && expandedByFilter !== null && expandedByFilter.has(node.path)
  const isExpanded = node.is_dir && (forceExpanded || expandedDirs.has(node.path))
  const isLoading = node.is_dir && loadingDirs.has(node.path)
  // Use the appropriate entries source for children
  const children = isFilterActive ? entriesSource[node.path] : entries[node.path]
  const isHidden = node.name.startsWith('.')

  const handleClick = useCallback(() => {
    if (node.is_dir) {
      toggleDir(node.path)
    }
  }, [node.is_dir, node.path, toggleDir])

  // If filter is active and this path isn't visible, hide it
  if (visiblePaths && !visiblePaths.has(node.path)) return null

  // Determine text color: yellow for matched nodes, default otherwise
  let textColorClass = isHidden ? 'text-zinc-500' : 'text-zinc-300'
  if (isMatch) {
    textColorClass = 'text-yellow-400'
  }

  return (
    <>
      <div
        className={`flex items-center gap-1 px-2 py-0.5 text-sm hover:bg-zinc-800/50 cursor-default select-none ${textColorClass}`}
        style={{ paddingLeft: depth * 16 + 8 }}
        onClick={handleClick}
        role={node.is_dir ? 'treeitem' : undefined}
        aria-expanded={node.is_dir ? isExpanded : undefined}
      >
        {/* Chevron for directories */}
        {node.is_dir ? (
          isLoading ? (
            <Loader2 className="h-3.5 w-3.5 flex-shrink-0 animate-spin text-zinc-500" />
          ) : (
            <ChevronRight
              className={`h-3.5 w-3.5 flex-shrink-0 text-zinc-500 transition-transform duration-150 ${
                isExpanded ? 'rotate-90' : ''
              }`}
            />
          )
        ) : (
          <span className="w-3.5 flex-shrink-0" />
        )}

        {!node.is_dir && (
          <span className={isHidden ? 'opacity-60' : undefined}>
            <FileIcon name={node.name} isDir={node.is_dir} isOpen={isExpanded} />
          </span>
        )}

        <span className="truncate">{node.name}</span>
      </div>

      {/* Render children if expanded */}
      {isExpanded && children && (
        <>
          {children.map((child) => (
            <TreeNode
              key={child.path}
              node={child}
              depth={depth + 1}
              isFilterActive={isFilterActive}
              matchedPaths={matchedPaths}
              visiblePaths={visiblePaths}
              expandedByFilter={expandedByFilter}
              entriesSource={entriesSource}
            />
          ))}
        </>
      )}
    </>
  )
}

export function FileTreePanel() {
  const rootPath = useFileTreeStore((s) => s.rootPath)
  const entries = useFileTreeStore((s) => s.entries)
  const recursiveEntries = useFileTreeStore((s) => s.recursiveEntries)
  const recursiveLoading = useFileTreeStore((s) => s.recursiveLoading)
  const initForProject = useFileTreeStore((s) => s.initForProject)
  const clearTree = useFileTreeStore((s) => s.clearTree)
  const refreshVisibleDirs = useFileTreeStore((s) => s.refreshVisibleDirs)
  const fetchRecursiveTree = useFileTreeStore((s) => s.fetchRecursiveTree)
  const clearRecursiveEntries = useFileTreeStore((s) => s.clearRecursiveEntries)

  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const projects = useProjectStore((s) => s.projects)
  const activeProject = projects?.find((p) => p.id === activeProjectId)

  const [filterText, setFilterText] = useState('')
  const [debouncedFilter, setDebouncedFilter] = useState('')
  const [filterMode, setFilterMode] = useState<FilterMode>('glob')
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Debounce filter text (300ms)
  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      setDebouncedFilter(filterText)
    }, 300)
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [filterText])

  const matcher = useMemo(
    () => buildMatcher(debouncedFilter, filterMode),
    [debouncedFilter, filterMode],
  )

  const isFilterActive = matcher !== null
  const hasRecursiveData = Object.keys(recursiveEntries).length > 0

  // Fetch recursive tree when filter becomes active
  useEffect(() => {
    if (isFilterActive && rootPath && !hasRecursiveData && !recursiveLoading) {
      fetchRecursiveTree(rootPath)
    }
    if (!isFilterActive && hasRecursiveData) {
      clearRecursiveEntries()
    }
  }, [isFilterActive, rootPath, hasRecursiveData, recursiveLoading, fetchRecursiveTree, clearRecursiveEntries])

  // Recursive filter visibility (used when filter is active and recursive data loaded)
  const recursiveFilterState = useRecursiveFilterState(recursiveEntries, rootPath, matcher)

  // Fallback visibility for lazy-loaded entries (used before recursive data loads)
  const lazyVisiblePaths = useNodeVisibility(entries, matcher)

  const handleToggleMode = useCallback(() => {
    setFilterMode((prev) => (prev === 'glob' ? 'regex' : 'glob'))
    setFilterText('')
    setDebouncedFilter('')
  }, [])

  const handleClearFilter = useCallback(() => {
    setFilterText('')
    setDebouncedFilter('')
  }, [])

  // React to project changes
  useEffect(() => {
    if (activeProject?.workspace_path) {
      initForProject(activeProject.workspace_path)
    } else {
      clearTree()
    }
  }, [activeProject?.workspace_path, initForProject, clearTree])

  // Listen for workspace:tree_changed events
  useEffect(() => {
    const rt = window?.runtime
    if (!rt) return () => {}
    const cleanup = rt.EventsOn('workspace:tree_changed', () => {
      refreshVisibleDirs()
      // If filter is active, also re-fetch recursive tree
      if (isFilterActive && rootPath) {
        fetchRecursiveTree(rootPath)
      }
    })
    return cleanup
  }, [refreshVisibleDirs, isFilterActive, rootPath, fetchRecursiveTree])

  // No project selected
  if (!activeProjectId) {
    return (
      <div className="h-full bg-card flex flex-col">
        <div className="px-3 py-2 text-xs font-semibold uppercase tracking-wider text-zinc-500 border-b border-border flex-shrink-0">
          Workspace
        </div>
        <div className="flex-1 flex items-center justify-center">
          <span className="text-xs text-zinc-600">No project selected</span>
        </div>
      </div>
    )
  }

  // Choose data source and visibility based on filter state
  const useRecursive = isFilterActive && hasRecursiveData
  const entriesSource = useRecursive ? recursiveEntries : entries
  const rootEntries = rootPath ? entriesSource[rootPath] : undefined

  const currentMatchedPaths = useRecursive ? (recursiveFilterState?.matchedPaths ?? null) : null
  const currentVisiblePaths = useRecursive ? (recursiveFilterState?.visiblePaths ?? null) : lazyVisiblePaths
  const currentExpandedByFilter = useRecursive ? (recursiveFilterState?.expandedByFilter ?? null) : null

  return (
    <div className="h-full bg-card flex flex-col">
      {/* Filter bar */}
      <div className="px-2 py-1.5 flex items-center gap-1 border-b border-zinc-800 flex-shrink-0">
        <input
          type="text"
          value={filterText}
          onChange={(e) => setFilterText(e.target.value)}
          placeholder={`Filter (${filterMode})…`}
          className="flex-1 min-w-0 bg-zinc-900 border border-zinc-700 rounded px-1.5 py-0.5 text-xs text-zinc-300 placeholder:text-zinc-600 outline-none focus:border-zinc-500 transition-colors"
        />
        {filterText && (
          <button
            onClick={handleClearFilter}
            className="p-0.5 rounded hover:bg-zinc-700 text-zinc-500 hover:text-zinc-300 transition-colors"
            title="Clear filter"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
        <button
          onClick={handleToggleMode}
          className={`p-0.5 rounded hover:bg-zinc-700 transition-colors ${
            filterMode === 'regex'
              ? 'text-blue-400 hover:text-blue-300'
              : 'text-zinc-500 hover:text-zinc-300'
          }`}
          title={`Mode: ${filterMode} (click to toggle)`}
        >
          {filterMode === 'regex' ? (
            <Regex className="h-3.5 w-3.5" />
          ) : (
            <Asterisk className="h-3.5 w-3.5" />
          )}
        </button>
      </div>

      <ScrollArea className="flex-1">
        <div className="py-1" role="tree">
          {rootEntries === undefined ? (
            <div className="px-3 py-2 text-xs text-zinc-500">
              {isFilterActive && recursiveLoading ? 'Searching…' : 'Loading…'}
            </div>
          ) : rootEntries.length === 0 ? (
            <div className="px-3 py-4 text-xs text-zinc-600 text-center">
              {isFilterActive ? 'No matches found' : 'No files created yet'}
            </div>
          ) : (
            rootEntries.map((node) => (
              <TreeNode
                key={node.path}
                node={node}
                depth={0}
                isFilterActive={isFilterActive}
                matchedPaths={currentMatchedPaths}
                visiblePaths={currentVisiblePaths}
                expandedByFilter={currentExpandedByFilter}
                entriesSource={entriesSource}
              />
            ))
          )}
        </div>
      </ScrollArea>
    </div>
  )
}
