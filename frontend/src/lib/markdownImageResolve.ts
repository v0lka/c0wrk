/**
 * Pure helpers for resolving markdown image `src` values to local disk paths.
 *
 * Kept in a dedicated module (rather than alongside the React `Markdown`
 * component) so the component file only exports components — required for
 * React Fast Refresh, and lets these helpers be unit-tested in isolation.
 */

/** Matches external/data URLs that the webview can load directly. */
export const EXTERNAL_SRC_RE = /^(data:|[a-z][a-z0-9+.-]*:)/i

/** Collapses `.` and `..` segments and returns an absolute POSIX path. */
export function normalizeAbsolutePath(p: string): string {
  const parts = p.split('/')
  const segments: string[] = []
  for (const part of parts) {
    if (part === '' || part === '.') continue
    if (part === '..') segments.pop()
    else segments.push(part)
  }
  return '/' + segments.join('/')
}

/**
 * Builds the ordered list of absolute disk paths to try when resolving a
 * markdown image `src`. Absolute paths (`/...`) yield a single candidate;
 * relative paths yield the markdown-file directory first, then the workspace
 * root. Returns an empty array when no base is available to resolve against.
 */
export function candidateImagePaths(
  src: string,
  baseFilePath: string | null | undefined,
  workspaceRoot: string | null | undefined,
): string[] {
  if (src.startsWith('/')) return [src]
  const candidates: string[] = []
  if (baseFilePath) {
    const slash = baseFilePath.lastIndexOf('/')
    const dir = slash >= 0 ? baseFilePath.slice(0, slash) : ''
    if (dir) candidates.push(normalizeAbsolutePath(dir + '/' + src))
  }
  if (workspaceRoot) {
    candidates.push(normalizeAbsolutePath(workspaceRoot + '/' + src))
  }
  return candidates
}
