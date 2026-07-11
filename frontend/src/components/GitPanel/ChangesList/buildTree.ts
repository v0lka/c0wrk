import type { GitPanelEntry, SortBy } from '@/stores/gitPanelStore'
import { compareEntries } from '@/lib/gitSortGroup'
import type { TreeDirNode, TreeFileNode, TreeNode } from './types'

// ──────────────────────────────── Helpers ────────────────────────────────────

/**
 * Compute the display-relative path for an entry (stripped of the workspace
 * root prefix). Shared by `buildTree` and `collectAllDirPaths` so both use
 * identical path semantics for the `expandedDirs` set.
 */
function displayPath(entry: GitPanelEntry, workspaceRoot: string): string {
  // Match workspaceRoot only at a path-separator boundary to avoid a
  // sibling directory sharing the same prefix (e.g. "/repo" vs "/repo-extra").
  const isUnderRoot =
    workspaceRoot !== '' &&
    (entry.path === workspaceRoot || entry.path.startsWith(workspaceRoot + '/'))
  return isUnderRoot
    ? entry.path.slice(workspaceRoot.length).replace(/^\//, '')
    : entry.path
}

// ──────────────────────────────── Tree Builder ───────────────────────────────

/**
 * Build a sorted tree from a flat list of file entries.
 * Directories are created implicitly from file path segments.
 * Nodes are sorted: directories first, then files — alphabetically within each group.
 *
 * @param entries  Flat list of git file entries (absolute paths preserved for callbacks).
 * @param workspaceRoot  Workspace root path (to compute display-relative paths for tree structure).
 * @param sortBy  Sort criterion applied to leaf (file) nodes within each directory.
 *                Defaults to `'path'` (alphabetical). Directories always sort
 *                alphabetically and precede files. (D8)
 */
export function buildTree(
  entries: GitPanelEntry[],
  workspaceRoot: string,
  sortBy: SortBy = 'path',
): TreeNode[] {
  const root: TreeNode[] = []
  const dirMap = new Map<string, TreeDirNode>()

  // Preprocess: compute display path relative to workspace root
  interface IndexedEntry {
    entry: GitPanelEntry
    displayPath: string
  }
  const indexed: IndexedEntry[] = entries.map((entry) => ({
    entry,
    displayPath: displayPath(entry, workspaceRoot),
  }))

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

  // Sort each directory's children: directories first (alphabetical), then
  // files ordered by the selected sort criterion (D8).
  const sortChildren = (nodes: TreeNode[]) => {
    nodes.sort((a, b) => {
      // Directories always precede files.
      if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
      // Both are directories — alphabetical by name.
      if (a.isDir && b.isDir) return a.name.localeCompare(b.name)
      // Both are files — compare by the selected criterion.
      return compareEntries(
        (a as TreeFileNode).entry,
        (b as TreeFileNode).entry,
        sortBy,
      )
    })
    for (const node of nodes) {
      if (node.isDir) sortChildren(node.children)
    }
  }
  sortChildren(root)

  return root
}

// ──────────────────────── Directory Path Collector ───────────────────────────

/**
 * Collect every directory path that would appear in the tree built from the
 * given entries. The returned paths use the same display-relative format as
 * `TreeDirNode.fullPath` produced by `buildTree`, so they can be fed directly
 * into `setExpandedDirs` to expand all nodes.
 *
 * Unlike `buildTree` this does not sort or allocate tree nodes — it only
 * extracts the intermediate directory segments from each entry's display path.
 */
export function collectAllDirPaths(
  entries: GitPanelEntry[],
  workspaceRoot: string,
): string[] {
  const dirs = new Set<string>()
  for (const entry of entries) {
    const parts = displayPath(entry, workspaceRoot).split('/')
    // Root-level files (single segment) contribute no directory paths.
    let currentPath = ''
    for (let i = 0; i < parts.length - 1; i++) {
      currentPath = currentPath ? `${currentPath}/${parts[i]}` : parts[i]!
      dirs.add(currentPath)
    }
  }
  // Sort for deterministic output (Set iteration order follows insertion,
  // which depends on backend status output order).
  return Array.from(dirs).sort()
}
