import { listDirectory } from '@/api/workspace'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useUIStore } from '@/stores/uiStore'
import { logger } from '@/lib/logger'

/**
 * Split `filePath` into the ordered list of ancestor directory paths that sit
 * strictly between `rootPath` and the file's immediate parent directory.
 *
 * For `filePath = '/ws/src/components/Foo.ts'`, `rootPath = '/ws'` this yields
 * `['/ws/src', '/ws/src/components']` — every directory that must be expanded
 * (and have its children loaded) for the file node to render in the tree.
 *
 * Returns an empty array when the file is a direct child of the root or when
 * the file is not under the root.
 */
export function ancestorDirsBetween(filePath: string, rootPath: string): string[] {
  const file = filePath.replace(/\\/g, '/')
  const root = rootPath.replace(/\\/g, '/').replace(/\/$/, '')
  const prefix = root + '/'

  if (!file.startsWith(prefix)) return []

  // Slice off the filename segment — we only want ancestor *directories*.
  const lastSlash = file.lastIndexOf('/')
  // `relDirs` = path segments between root and the file's parent.
  const relDirs = file.slice(prefix.length, lastSlash)
  if (!relDirs) return []

  const dirs: string[] = []
  let acc = root
  for (const segment of relDirs.split('/')) {
    if (!segment) continue
    acc += '/' + segment
    dirs.push(acc)
  }
  return dirs
}

/**
 * Reveal a file in the sidebar workspace (Explorer) tree: switches to the
 * Explorer tab, expands the sidebar if collapsed, lazily loads every ancestor
 * directory that isn't already cached, expands the whole ancestor chain, and
 * marks the file as the transient "current" selection so it is highlighted
 * and scrolled into view.
 *
 * Safe to call when no workspace root is loaded — the function no-ops in that
 * case.
 */
export async function revealInWorkspace(filePath: string): Promise<void> {
  const treeState = useFileTreeStore.getState()
  const rootPath = treeState.rootPath
  if (!rootPath) {
    logger.warn('revealInWorkspace: no workspace root loaded — skipping')
    return
  }

  // Ensure the sidebar is visible and on the Explorer tab before touching the
  // tree, otherwise the expansion/selection has nothing to render into.
  useUIStore.getState().setWorkspaceTab('explorer')
  if (useUIStore.getState().sidebarCollapsed) {
    useUIStore.getState().setSidebarCollapsed(false)
  }

  const ancestorDirs = ancestorDirsBetween(filePath, rootPath)

  // Lazily load any ancestor directory whose children aren't cached yet. The
  // tree renders a node's children only when the directory is both expanded
  // AND has cached children, so every ancestor on the chain must be loaded.
  for (const dir of ancestorDirs) {
    if (!useFileTreeStore.getState().tree[dir]) {
      try {
        const entries = await listDirectory(dir)
        useFileTreeStore.getState().setEntries(dir, entries)
      } catch (err) {
        logger.error(`revealInWorkspace: failed to load directory ${dir}:`, err)
      }
    }
  }

  // Expand the whole ancestor chain (idempotent — never collapses anything).
  treeState.expandDirs(ancestorDirs)
  // Mark the file as the current selection (highlight + scroll target).
  treeState.setSelectedPath(filePath)
}
