import type { GitPanelEntry } from '@/stores/gitPanelStore'
import type { TreeDirNode, TreeFileNode, TreeNode } from './types'

// ──────────────────────────────── Tree Builder ───────────────────────────────

/**
 * Build a sorted tree from a flat list of file entries.
 * Directories are created implicitly from file path segments.
 * Nodes are sorted: directories first, then files — alphabetically within each group.
 *
 * @param entries  Flat list of git file entries (absolute paths preserved for callbacks).
 * @param workspaceRoot  Workspace root path (to compute display-relative paths for tree structure).
 */
export function buildTree(entries: GitPanelEntry[], workspaceRoot: string): TreeNode[] {
  const root: TreeNode[] = []
  const dirMap = new Map<string, TreeDirNode>()

  // Preprocess: compute display path relative to workspace root
  interface IndexedEntry {
    entry: GitPanelEntry
    displayPath: string
  }
  const indexed: IndexedEntry[] = entries.map((entry) => {
    const displayPath =
      workspaceRoot && entry.path.startsWith(workspaceRoot)
        ? entry.path.slice(workspaceRoot.length).replace(/^\//, '')
        : entry.path
    return { entry, displayPath }
  })

  // Sort entries by display path for deterministic ordering
  indexed.sort((a, b) => a.displayPath.localeCompare(b.displayPath))

  for (const { entry, displayPath } of indexed) {
    const parts = displayPath.split('/')

    // Root-level file (no directory)
    if (parts.length === 1) {
      root.push({ name: parts[0]!, entry, isDir: false })
      continue
    }

    // Ensure all intermediate directory nodes exist
    let currentPath = ''
    for (let i = 0; i < parts.length - 1; i++) {
      const parent = currentPath
      currentPath = currentPath ? `${currentPath}/${parts[i]}` : parts[i]!

      if (!dirMap.has(currentPath)) {
        const node: TreeDirNode = {
          name: parts[i]!,
          fullPath: currentPath,
          isDir: true,
          children: [],
        }
        dirMap.set(currentPath, node)

        // Link to parent
        if (i === 0) {
          root.push(node)
        } else if (dirMap.has(parent)) {
          dirMap.get(parent)!.children.push(node)
        }
      }
    }

    // Add file to its parent directory
    const parentDir = parts.slice(0, -1).join('/')
    const fileNode: TreeFileNode = {
      name: parts[parts.length - 1]!,
      entry,
      isDir: false,
    }
    if (dirMap.has(parentDir)) {
      dirMap.get(parentDir)!.children.push(fileNode)
    }
  }

  // Sort each directory's children: dirs first, then files, alphabetical
  const sortChildren = (nodes: TreeNode[]) => {
    nodes.sort((a, b) => {
      if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
      return a.name.localeCompare(b.name)
    })
    for (const node of nodes) {
      if (node.isDir) sortChildren(node.children)
    }
  }
  sortChildren(root)

  return root
}
