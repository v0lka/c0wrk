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
 * Image file extensions the file viewer renders as pictures instead of text.
 * Covers every format the webview can paint in an <img>: the raster formats
 * (png, jpg/jpeg, gif, webp, bmp, ico, avif) plus the vector SVG. Kept in sync
 * with the backend's `imageMimeByExt` map (backend/frontend_api_workspace.go),
 * which guarantees an `image/*` MIME on the data URL the renderer receives.
 */
export const IMAGE_FILE_EXTENSIONS = new Set([
  'png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico', 'svg', 'avif',
])

/**
 * Report whether a path points at a viewable image file, judged by its
 * extension alone (no I/O). The file viewer uses this to route the tab to the
 * image renderer instead of the code/markdown editor, and to decide whether to
 * fetch bytes as a data URL rather than read them as text.
 *
 * Synthetic pseudo-paths (e.g. `c0wrk:review`, `c0wrk:paper:<slug>`) carry no
 * extension and always return false.
 */
export function isImageFilePath(path: string): boolean {
  const dot = path.lastIndexOf('.')
  if (dot < 0) return false
  return IMAGE_FILE_EXTENSIONS.has(path.slice(dot + 1).toLowerCase())
}

/**
 * Extract file name from a full path.
 * Handles synthetic pseudo-paths (e.g. 'c0wrk:review') by returning the
 * portion after the colon, title-cased.
 */
export function fileNameFromPath(path: string): string {
  // Commit review tab: "c0wrk:commit:<sha>" → "Commit <short-sha>"
  if (path.startsWith('c0wrk:commit:')) {
    const sha = path.slice('c0wrk:commit:'.length)
    return `Commit ${sha.slice(0, 7)}`
  }
  // Paper workspace tab: "c0wrk:paper:<slug>" → the slug (the workspace header
  // carries the full title once the record resolves).
  if (path.startsWith('c0wrk:paper:')) {
    const slug = path.slice('c0wrk:paper:'.length)
    return slug || 'Paper'
  }
  if (path.startsWith('c0wrk:') && !path.includes('/')) {
    const label = path.slice('c0wrk:'.length)
    return label.charAt(0).toUpperCase() + label.slice(1)
  }
  return path.split('/').pop() ?? path
}
