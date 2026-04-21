import hljs from 'highlight.js/lib/core'

import markdown from 'highlight.js/lib/languages/markdown'
import go from 'highlight.js/lib/languages/go'
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import python from 'highlight.js/lib/languages/python'
import rust from 'highlight.js/lib/languages/rust'
import ruby from 'highlight.js/lib/languages/ruby'
import bash from 'highlight.js/lib/languages/bash'
import css from 'highlight.js/lib/languages/css'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'
import json from 'highlight.js/lib/languages/json'
import sql from 'highlight.js/lib/languages/sql'
import plaintext from 'highlight.js/lib/languages/plaintext'

let registered = false

/**
 * registerLanguages registers all highlight.js languages used by the app.
 * Must be called once at app startup before any highlighting is performed.
 */
export function registerLanguages(): void {
  if (registered) return
  registered = true

  hljs.registerLanguage('markdown', markdown)
  hljs.registerLanguage('go', go)
  hljs.registerLanguage('javascript', javascript)
  hljs.registerLanguage('typescript', typescript)
  hljs.registerLanguage('python', python)
  hljs.registerLanguage('rust', rust)
  hljs.registerLanguage('ruby', ruby)
  hljs.registerLanguage('bash', bash)
  hljs.registerLanguage('css', css)
  hljs.registerLanguage('xml', xml)
  hljs.registerLanguage('yaml', yaml)
  hljs.registerLanguage('json', json)
  hljs.registerLanguage('sql', sql)
  hljs.registerLanguage('plaintext', plaintext)
}

/**
 * detectLanguage maps a file extension to a highlight.js language name.
 * Falls back to 'plaintext' for unknown extensions.
 */
export function detectLanguage(fileName: string): string {
  // Check for exact filename matches first
  const exactMap: Record<string, string> = {
    'Makefile': 'plaintext',
    'makefile': 'plaintext',
    'Dockerfile': 'bash',
    'dockerfile': 'bash',
    '.gitignore': 'bash',
    '.gitmodules': 'bash',
    '.editorconfig': 'plaintext',
    'go.mod': 'go',
    'go.sum': 'plaintext',
  }
  if (exactMap[fileName]) return exactMap[fileName]

  // Extension-based detection
  const dotIdx = fileName.indexOf('.')
  if (dotIdx < 0) return 'plaintext'

  let ext = fileName.slice(dotIdx).toLowerCase()

  const extMap: Record<string, string> = {
    '.go': 'go',
    '.js': 'javascript',
    '.mjs': 'javascript',
    '.cjs': 'javascript',
    '.jsx': 'javascript',
    '.ts': 'typescript',
    '.tsx': 'typescript',
    '.py': 'python',
    '.pyx': 'python',
    '.rs': 'rust',
    '.rb': 'ruby',
    '.sh': 'bash',
    '.bash': 'bash',
    '.zsh': 'bash',
    '.css': 'css',
    '.scss': 'css',
    '.less': 'css',
    '.html': 'xml',
    '.htm': 'xml',
    '.svg': 'xml',
    '.xml': 'xml',
    '.yaml': 'yaml',
    '.yml': 'yaml',
    '.toml': 'yaml',
    '.json': 'json',
    '.md': 'markdown',
    '.mdx': 'markdown',
    '.sql': 'sql',
    '.env': 'bash',
    '.ini': 'plaintext',
    '.cfg': 'plaintext',
    '.conf': 'plaintext',
    '.mod': 'go',
    '.sum': 'plaintext',
  }

  // Try longest extension match
  while (ext) {
    const lang = extMap[ext]
    if (lang !== undefined) return lang
    const nextDot = ext.indexOf('.', 1)
    if (nextDot === -1) break
    ext = ext.slice(nextDot)
  }

  return 'plaintext'
}
