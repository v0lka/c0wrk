// ──────────────────────────────── Shared Types ───────────────────────────────

/** Internal directory node of the branch tree view. */
export interface BranchTreeDirNode {
  name: string
  /** Slash-joined path from the tree root to this directory (e.g. `feature/auth`). */
  fullPath: string
  isDir: true
  children: BranchTreeNode[]
  /**
   * Full name of a branch that ends exactly at this directory level.
   * Set when a branch name collides as both leaf and directory
   * (`feature` + `feature/login` → the `feature` node is a directory that
   * additionally carries the `feature` branch as its leaf payload).
   */
  leaf?: string
}

/** Leaf node carrying the full branch name. */
export interface BranchTreeLeafNode {
  name: string
  /** Full branch name (e.g. `feature/login`). */
  branch: string
  isDir: false
}

export type BranchTreeNode = BranchTreeDirNode | BranchTreeLeafNode

// ──────────────────────────────── Tree Builder ───────────────────────────────

/**
 * Build a sorted tree from a list of branch names.
 * Directories are created implicitly from `/`-separated branch segments.
 * Nodes are sorted: directories first, then leaves — alphabetically within
 * each group (mirrors `ChangesList/buildTree.ts`).
 *
 * When one branch name is both a leaf and a directory prefix of another
 * (`feature` and `feature/login`), a single directory node is produced and it
 * carries the colliding branch as its optional `leaf` payload — no duplicate
 * sibling node is created.
 *
 * @param branches  Branch names; git uses `/` as a namespace separator
 *                  (e.g. `feature/login`). Duplicate names are ignored.
 */
export function buildBranchTree(branches: string[]): BranchTreeNode[] {
  const root: BranchTreeNode[] = []
  const dirMap = new Map<string, BranchTreeDirNode>()

  // Dedupe and sort for deterministic node creation order.
  const names = Array.from(new Set(branches)).sort((a, b) => a.localeCompare(b))

  // Pass 1 — ensure every intermediate directory node exists. Doing this in a
  // separate pass keeps the leaf/directory collision (`feature` +
  // `feature/login`) order-independent: by the time leaves are attached, every
  // directory they could collide with already exists.
  for (const branch of names) {
    const parts = branch.split('/')

    let currentPath = ''
    for (let i = 0; i < parts.length - 1; i++) {
      const parent = currentPath
      currentPath = currentPath ? `${currentPath}/${parts[i]}` : parts[i]!

      if (!dirMap.has(currentPath)) {
        const node: BranchTreeDirNode = {
          name: parts[i]!,
          fullPath: currentPath,
          isDir: true,
          children: [],
        }
        dirMap.set(currentPath, node)

        // Link to parent.
        if (i === 0) {
          root.push(node)
        } else {
          dirMap.get(parent)!.children.push(node)
        }
      }
    }
  }

  // Pass 2 — attach each branch as a leaf. When a deeper branch already turned
  // the final segment into a directory, that directory node carries the branch
  // as its leaf payload; otherwise a leaf node is appended.
  for (const branch of names) {
    const existingDir = dirMap.get(branch)
    if (existingDir) {
      existingDir.leaf = branch
      continue
    }

    const parts = branch.split('/')
    const leafNode: BranchTreeLeafNode = {
      name: parts[parts.length - 1]!,
      branch,
      isDir: false,
    }
    const parentDir = parts.slice(0, -1).join('/')
    if (parentDir) {
      dirMap.get(parentDir)!.children.push(leafNode)
    } else {
      root.push(leafNode)
    }
  }

  // Sort each directory's children: directories first (alphabetical), then
  // leaves (alphabetical).
  const sortChildren = (nodes: BranchTreeNode[]): void => {
    nodes.sort((a, b) => {
      // Directories always precede leaves.
      if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
      // Alphabetical by display name within each group.
      return a.name.localeCompare(b.name)
    })
    for (const node of nodes) {
      if (node.isDir) sortChildren(node.children)
    }
  }
  sortChildren(root)

  return root
}
