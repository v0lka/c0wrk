/**
 * File-tree name and path helpers shared by the tree panel, the context menu
 * and the inline creation/rename input (issue #60). Paths inside the tree use
 * the OS separator the backend listing produces, so joining a user-typed name
 * onto a directory must use that same separator.
 */

/** Platform path separator — Windows uses `\`, POSIX uses `/`. Derived
 *  lazily per call, never at module scope: a module-scope `navigator` read
 *  crashes test collection (the vitest environment has no `navigator`),
 *  taking down every importing test file with it. */
export function treePathSep(): string {
  const platform = typeof navigator === 'undefined' ? '' : navigator.platform
  return platform.includes('Win') ? '\\' : '/'
}

/** Joins a directory path from the tree with a user-typed entry name. */
export function joinTreePath(dir: string, name: string): string {
  const sep = treePathSep()
  if (dir.endsWith(sep)) return dir + name
  return dir + sep + name
}

/** The directory containing a tree entry path (its parent in tree terms). */
export function parentTreeDir(path: string): string {
  const sep = treePathSep()
  const idx = path.lastIndexOf(sep)
  return idx > 0 ? path.slice(0, idx) : path
}

/**
 * Client-side pre-flight validation for a user-typed entry name. Returns a
 * user-facing error message, or null when the name is acceptable. The
 * backend stays authoritative (containment + collision re-checked on the
 * RPC path) — this only produces a clear message before the round-trip.
 */
export function validateEntryName(name: string, siblings: readonly string[]): string | null {
  const trimmed = name.trim()
  if (trimmed.length === 0) {
    return 'Name cannot be empty'
  }
  if (trimmed !== name) {
    return 'Name cannot have leading or trailing spaces'
  }
  if (name.includes('/') || name.includes('\\')) {
    return 'Name cannot contain path separators'
  }
  if (name === '.' || name === '..') {
    return 'Name cannot be "." or ".."'
  }
  const clash = siblings.find((s) => s.toLowerCase() === name.toLowerCase())
  if (clash !== undefined) {
    return `An entry named "${clash}" already exists`
  }
  return null
}
