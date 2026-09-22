import type { GitPanelEntry } from '@/stores/gitPanelStore'
import type { StageSide } from '@/lib/gitStatus'

// ──────────────────────────────── Shared Types ───────────────────────────────

export interface SectionData {
  key: string
  title: string
  /**
   * The porcelain axis this section's rows act on: 'index' for Staged Changes,
   * 'worktree' for Changes and Untracked Files. Drives each row's checkbox
   * state, batch action, and status badge.
   */
  side: StageSide
  entries: GitPanelEntry[]
}

/** Internal node type for tree view */
export interface TreeDirNode {
  name: string
  fullPath: string
  isDir: true
  children: TreeNode[]
}

export interface TreeFileNode {
  name: string
  entry: GitPanelEntry
  isDir: false
}

export type TreeNode = TreeDirNode | TreeFileNode
