import hljs from 'highlight.js/lib/core'

/**
 * Map file extensions to registered highlight.js language names.
 *
 * Only languages registered in {@link ../lib/hljsLanguages.ts} are listed.
 * {@link detectHljsLanguage} double-checks registration via
 * `hljs.getLanguage` before returning, so unmapped or unregistered
 * extensions safely fall back to plain text.
 */
const EXT_TO_HLJS: Record<string, string> = {
  '.go': 'go',
  '.ts': 'typescript',
  '.tsx': 'typescript',
  '.mts': 'typescript',
  '.cts': 'typescript',
  '.js': 'javascript',
  '.jsx': 'javascript',
  '.mjs': 'javascript',
  '.cjs': 'javascript',
  '.py': 'python',
  '.pyw': 'python',
  '.rs': 'rust',
  '.rb': 'ruby',
  '.sh': 'bash',
  '.bash': 'bash',
  '.zsh': 'bash',
  '.css': 'css',
  '.xml': 'xml',
  '.html': 'xml',
  '.htm': 'xml',
  '.svg': 'xml',
  '.vue': 'xml',
  '.yaml': 'yaml',
  '.yml': 'yaml',
  '.json': 'json',
  '.jsonc': 'json',
  '.sql': 'sql',
  '.md': 'markdown',
  '.markdown': 'markdown',
}

/** Special filenames without a conventional extension. */
const NAME_TO_HLJS: Record<string, string> = {
  Dockerfile: 'dockerfile',
  dockerfile: 'dockerfile',
  Makefile: 'bash',
  makefile: 'bash',
}

/**
 * Detect a registered highlight.js language name from a file path.
 *
 * Returns `null` when no registered language matches, signalling the
 * caller to fall back to plain (escaped) text.
 */
export function detectHljsLanguage(filePath: string): string | null {
  const name = filePath.split('/').pop() ?? filePath

  // Try exact filename match first (Dockerfile, Makefile, …).
  const nameMatch = NAME_TO_HLJS[name]
  if (nameMatch && hljs.getLanguage(nameMatch)) return nameMatch

  // Try extension lookup.
  const dotIdx = name.lastIndexOf('.')
  if (dotIdx > 0) {
    const ext = name.slice(dotIdx).toLowerCase()
    const lang = EXT_TO_HLJS[ext]
    if (lang && hljs.getLanguage(lang)) return lang
  }

  return null
}

/**
 * Highlight a single line of code with the given language.
 *
 * Returns safe HTML with `hljs-*` span classes. Falls back to
 * HTML-escaped plain text when `language` is `null` or highlighting
 * fails (e.g. the language is not registered).
 *
 * Per-line highlighting is intentional for diff hunks: added and deleted
 * lines represent different states of the code, so highlighting them as a
 * single block would be semantically incorrect.
 */
export function highlightCodeLine(text: string, language: string | null): string {
  if (!language || !text) return escapeHtml(text)
  try {
    return hljs.highlight(text, { language }).value
  } catch {
    return escapeHtml(text)
  }
}

/** Escape HTML special characters for safe rendering via `dangerouslySetInnerHTML`. */
function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
