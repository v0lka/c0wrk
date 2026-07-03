import type { GitPanelEntry } from '@/stores/gitPanelStore'

// ──────────────────────────────── Shared Types ───────────────────────────────

export interface SectionData {
  key: string
  title: string
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
