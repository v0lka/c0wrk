import { describe, it, expect } from 'vitest'
import { branchLeafLabel, buildBranchTree, type BranchTreeNode } from './branchTree'

describe('buildBranchTree', () => {
  it('returns an empty array for empty input', () => {
    expect(buildBranchTree([])).toEqual([])
  })

  it('builds flat leaves for single-segment branch names', () => {
    expect(buildBranchTree(['main', 'develop'])).toEqual([
      { name: 'develop', branch: 'develop', isDir: false },
      { name: 'main', branch: 'main', isDir: false },
    ])
  })

  it('nests branches two levels deep with directory fullPath values', () => {
    expect(buildBranchTree(['feature/login', 'feature/signup'])).toEqual([
      {
        name: 'feature',
        fullPath: 'feature',
        isDir: true,
        leaf: undefined,
        children: [
          { name: 'login', branch: 'feature/login', isDir: false },
          { name: 'signup', branch: 'feature/signup', isDir: false },
        ],
      },
    ])
  })

  it('nests branches three levels deep', () => {
    expect(buildBranchTree(['release/2.0/fix-crash'])).toEqual([
      {
        name: 'release',
        fullPath: 'release',
        isDir: true,
        leaf: undefined,
        children: [
          {
            name: '2.0',
            fullPath: 'release/2.0',
            isDir: true,
            leaf: undefined,
            children: [
              { name: 'fix-crash', branch: 'release/2.0/fix-crash', isDir: false },
            ],
          },
        ],
      },
    ])
  })

  it('shares intermediate directory nodes across sibling branches', () => {
    expect(buildBranchTree(['feature/login', 'feature/logout', 'hotfix/x'])).toEqual([
      {
        name: 'feature',
        fullPath: 'feature',
        isDir: true,
        leaf: undefined,
        children: [
          { name: 'login', branch: 'feature/login', isDir: false },
          { name: 'logout', branch: 'feature/logout', isDir: false },
        ],
      },
      {
        name: 'hotfix',
        fullPath: 'hotfix',
        isDir: true,
        leaf: undefined,
        children: [{ name: 'x', branch: 'hotfix/x', isDir: false }],
      },
    ])
  })

  it('mixes flat and nested branches with shared parents', () => {
    expect(buildBranchTree(['main', 'feature/login', 'develop'])).toEqual([
      {
        name: 'feature',
        fullPath: 'feature',
        isDir: true,
        leaf: undefined,
        children: [{ name: 'login', branch: 'feature/login', isDir: false }],
      },
      { name: 'develop', branch: 'develop', isDir: false },
      { name: 'main', branch: 'main', isDir: false },
    ])
  })

  it('sorts directories before leaves, each alphabetically', () => {
    // 'bbb' sorts before 'zzz' alphabetically, but the 'zzz' directory
    // must still precede the 'bbb' leaf at the root level.
    expect(buildBranchTree(['bbb', 'zzz/one'])).toEqual([
      {
        name: 'zzz',
        fullPath: 'zzz',
        isDir: true,
        leaf: undefined,
        children: [{ name: 'one', branch: 'zzz/one', isDir: false }],
      },
      { name: 'bbb', branch: 'bbb', isDir: false },
    ])
  })

  it('sorts recursively inside every directory', () => {
    const tree = buildBranchTree([
      'feature/zeta',
      'feature/aaa/x',
      'feature/mid',
    ])
    expect(tree).toEqual([
      {
        name: 'feature',
        fullPath: 'feature',
        isDir: true,
        leaf: undefined,
        children: [
          {
            name: 'aaa',
            fullPath: 'feature/aaa',
            isDir: true,
            leaf: undefined,
            children: [{ name: 'x', branch: 'feature/aaa/x', isDir: false }],
          },
          { name: 'mid', branch: 'feature/mid', isDir: false },
          { name: 'zeta', branch: 'feature/zeta', isDir: false },
        ],
      },
    ])
  })

  it('merges a leaf-and-directory collision into one directory node with a leaf payload', () => {
    expect(buildBranchTree(['feature', 'feature/foo'])).toEqual([
      {
        name: 'feature',
        fullPath: 'feature',
        isDir: true,
        leaf: 'feature',
        children: [{ name: 'foo', branch: 'feature/foo', isDir: false }],
      },
    ])
  })

  it('merges a leaf-and-directory collision at depth', () => {
    expect(buildBranchTree(['feature/sub/x', 'feature/sub'])).toEqual([
      {
        name: 'feature',
        fullPath: 'feature',
        isDir: true,
        leaf: undefined,
        children: [
          {
            name: 'sub',
            fullPath: 'feature/sub',
            isDir: true,
            leaf: 'feature/sub',
            children: [{ name: 'x', branch: 'feature/sub/x', isDir: false }],
          },
        ],
      },
    ])
  })

  it('merges collisions regardless of input order', () => {
    const leafFirst = buildBranchTree(['feature', 'feature/foo'])
    const dirFirst = buildBranchTree(['feature/foo', 'feature'])
    expect(leafFirst).toEqual(dirFirst)
    expect(leafFirst).toHaveLength(1)
  })

  it('ignores duplicate branch names', () => {
    const tree = buildBranchTree(['main', 'main', 'feature/one', 'feature/one'])
    expect(tree).toEqual([
      {
        name: 'feature',
        fullPath: 'feature',
        isDir: true,
        leaf: undefined,
        children: [{ name: 'one', branch: 'feature/one', isDir: false }],
      },
      { name: 'main', branch: 'main', isDir: false },
    ])
  })
})

describe('branchLeafLabel', () => {
  it('returns the last slash segment — the tree leaf label', () => {
    expect(branchLeafLabel('origin/ui/branch-grouping')).toBe('branch-grouping')
    expect(branchLeafLabel('feature/login')).toBe('login')
  })

  it('returns the full name when there is no slash (top-level leaf)', () => {
    expect(branchLeafLabel('main')).toBe('main')
  })

  it('always agrees with the tree leaf node name field', () => {
    const name = 'origin/feat/remote'
    const tree = buildBranchTree([name])
    // A single input name builds a linear chain of dirs ending in one leaf.
    let node: BranchTreeNode = tree[0]!
    while (node.isDir) node = node.children[0]!
    expect(node.branch).toBe(name)
    expect(branchLeafLabel(name)).toBe(node.name)
  })
})
