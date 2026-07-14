import hljs from 'highlight.js/lib/core'

/**
 * highlightHunkDiff syntax-highlights a single hunk's unified-diff block
 * using the registered 'diff' language. Returns safe HTML with hljs span
 * classes. Falls back to HTML-escaped plain text when highlighting is
 * unavailable (e.g. the diff language is not registered).
 *
 * The input is a hunk block (header + body) as produced by the backend's
 * GetFileDiffHunks RPC — never a full multi-file diff.
 */
export function highlightHunkDiff(diffBlock: string): string {
  if (!diffBlock) return ''
  try {
    const result = hljs.highlight(diffBlock, { language: 'diff' })
    return result.value
  } catch {
    // Language not registered or parse error — return escaped text.
    return escapeHtml(diffBlock)
  }
}

/** Escape HTML special characters for safe rendering in dangerouslySetInnerHTML. */
function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
