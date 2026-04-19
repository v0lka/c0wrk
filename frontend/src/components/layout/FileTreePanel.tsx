import { useState, useEffect, useCallback, useMemo } from 'react'
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

function useNodeVisibility(
  entries: Record<string, FileNode[]>,
  matcher: ((name: string) => boolean) | null,
) {
  return useMemo(() => {
    if (!matcher) return null // no filter active — everything visible

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

    // Walk from every known parent
    for (const key of Object.keys(entries)) {
      walk(entries[key])
    }

    return visible
  }, [entries, matcher])
}

interface TreeNodeProps {
  node: FileNode
  depth: number
  visiblePaths: Set<string> | null
}

function TreeNode({ node, depth, visiblePaths }: TreeNodeProps) {
  const expandedDirs = useFileTreeStore((s) => s.expandedDirs)
  const loadingDirs = useFileTreeStore((s) => s.loadingDirs)
  const entries = useFileTreeStore((s) => s.entries)
  const toggleDir = useFileTreeStore((s) => s.toggleDir)

  const isExpanded = node.is_dir && expandedDirs.has(node.path)
  const isLoading = node.is_dir && loadingDirs.has(node.path)
  const children = entries[node.path]
  const isHidden = node.name.startsWith('.')

  const handleClick = useCallback(() => {
    if (node.is_dir) {
      toggleDir(node.path)
    }
  }, [node.is_dir, node.path, toggleDir])

  // If filter is active and this path isn't visible, hide it
  if (visiblePaths && !visiblePaths.has(node.path)) return null

  return (
    <>
      <div
        className={`flex items-center gap-1 px-2 py-0.5 text-sm hover:bg-zinc-800/50 cursor-default select-none ${isHidden ? 'text-zinc-500' : 'text-zinc-300'}`}
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
            <TreeNode key={child.path} node={child} depth={depth + 1} visiblePaths={visiblePaths} />
          ))}
        </>
      )}
    </>
  )
}

export function FileTreePanel() {
  const rootPath = useFileTreeStore((s) => s.rootPath)
  const entries = useFileTreeStore((s) => s.entries)
  const initForProject = useFileTreeStore((s) => s.initForProject)
  const clearTree = useFileTreeStore((s) => s.clearTree)
  const refreshVisibleDirs = useFileTreeStore((s) => s.refreshVisibleDirs)

  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const projects = useProjectStore((s) => s.projects)
  const activeProject = projects?.find((p) => p.id === activeProjectId)

  const [filterText, setFilterText] = useState('')
  const [filterMode, setFilterMode] = useState<FilterMode>('glob')

  const matcher = useMemo(
    () => buildMatcher(filterText, filterMode),
    [filterText, filterMode],
  )
  const visiblePaths = useNodeVisibility(entries, matcher)

  const handleToggleMode = useCallback(() => {
    setFilterMode((prev) => (prev === 'glob' ? 'regex' : 'glob'))
    setFilterText('')
  }, [])

  const handleClearFilter = useCallback(() => {
    setFilterText('')
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
    })
    return cleanup
  }, [refreshVisibleDirs])

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

  const rootEntries = rootPath ? entries[rootPath] : undefined

  return (
    <div className="h-full bg-card flex flex-col">
      <div className="px-3 py-2 text-xs font-semibold uppercase tracking-wider text-zinc-500 border-b border-border flex-shrink-0">
        WORKSPACE
      </div>

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
            <div className="px-3 py-2 text-xs text-zinc-500">Loading…</div>
          ) : rootEntries.length === 0 ? (
            <div className="px-3 py-4 text-xs text-zinc-600 text-center">
              No files created yet
            </div>
          ) : (
            rootEntries.map((node) => (
              <TreeNode key={node.path} node={node} depth={0} visiblePaths={visiblePaths} />
            ))
          )}
        </div>
      </ScrollArea>
    </div>
  )
}
