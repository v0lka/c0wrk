/**
 * Check if file content is binary by looking for null bytes in the first 8KB.
 */
export function isBinaryContent(content: string): boolean {
  const check = content.slice(0, 8192)
  for (let i = 0; i < check.length; i++) {
    if (check.charCodeAt(i) === 0) return true
  }
  return false
}

/**
 * Extract file name from a full path.
 * Handles synthetic pseudo-paths (e.g. 'c0wrk:review') by returning the
 * portion after the colon, title-cased.
 */
export function fileNameFromPath(path: string): string {
  if (path.startsWith('c0wrk:') && !path.includes('/')) {
    const label = path.slice('c0wrk:'.length)
    return label.charAt(0).toUpperCase() + label.slice(1)
  }
  return path.split('/').pop() ?? path
}
