import hljs from 'highlight.js/lib/core'
import { detectLanguage } from '@/lib/hljsLanguages'

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
 * Detect language from a file path (uses the file name).
 */
export function detectLanguageFromPath(filePath: string): string {
  const name = filePath.split('/').pop() ?? filePath
  return detectLanguage(name)
}

/**
 * Extract file name from a full path.
 */
export function fileNameFromPath(path: string): string {
  return path.split('/').pop() ?? path
}

/**
 * Highlight source code using highlight.js.
 * Highlights the full content first, then splits into per-line segments
 * preserving tag balance across lines.
 * Returns an array of HTML strings, one per line.
 */
export function highlightLines(content: string, language: string): string[] {
  if (!content) return ['']

  let highlighted: string
  try {
    highlighted = hljs.highlight(content, { language, ignoreIllegals: true }).value
  } catch {
    try {
      highlighted = hljs.highlightAuto(content).value
    } catch {
      return content.split('\n').map(escapeHtml)
    }
  }

  return splitHighlightedHtml(highlighted)
}

/**
 * Split highlighted HTML into per-line strings with balanced `<span>` tags.
 *
 * highlight.js output is well-formed HTML that only contains `<span>` elements
 * with `class` attributes (e.g. `<span class="hljs-keyword">`). This function
 * exploits that constraint: it tracks open `<span>` tags across line boundaries,
 * re-opening them at the start of each new line and closing them at the end.
 *
 * This is NOT a general HTML parser — it is purpose-built for hljs output.
 */
function splitHighlightedHtml(html: string): string[] {
  // Fast path: no tags at all
  if (!html.includes('<')) return html.split('\n')

  const rawLines = html.split('\n')
  const result: string[] = []
  // Stack of currently-open span tags (full opening tag strings like `<span class="hljs-keyword">`)
  const openSpans: string[] = []

  for (const rawLine of rawLines) {
    // Re-open spans that remain open from previous lines
    let line = openSpans.join('') + rawLine

    // Track span opens/closes in this line
    spanTagRegex.lastIndex = 0
    let match: RegExpExecArray | null
    while ((match = spanTagRegex.exec(rawLine)) !== null) {
      if (match[1] === '/') {
        // Closing </span> — pop the most recent open span
        openSpans.pop()
      } else {
        // Opening <span ...> — push onto stack
        openSpans.push(match[0])
      }
    }

    // Close any spans that remain open at end of this line
    for (let i = openSpans.length - 1; i >= 0; i--) {
      line += '</span>'
    }

    result.push(line)
  }

  return result
}

// Matches <span ...> or </span> — the only tags hljs produces.
const spanTagRegex = /<(\/?)(span)(?:\s[^>]*)?>/g

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}
