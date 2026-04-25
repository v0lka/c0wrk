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
 */
export function fileNameFromPath(path: string): string {
  return path.split('/').pop() ?? path
}
