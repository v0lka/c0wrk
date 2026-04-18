import { useEffect, useCallback } from 'react'
import { ChevronRight, Loader2 } from 'lucide-react'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useFileTreeStore, type FileNode } from '@/stores/fileTreeStore'
import { useProjectStore } from '@/stores/projectStore'
import { FileIcon } from './FileIcon'

interface TreeNodeProps {
  node: FileNode
  depth: number
}

function TreeNode({ node, depth }: TreeNodeProps) {
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
            <TreeNode key={child.path} node={child} depth={depth + 1} />
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
              <TreeNode key={node.path} node={node} depth={0} />
            ))
          )}
        </div>
      </ScrollArea>
    </div>
  )
}
