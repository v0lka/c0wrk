import { LanguageDescription } from '@codemirror/language'
import { languages } from '@codemirror/language-data'
import type { LanguageSupport } from '@codemirror/language'

/**
 * Fallback map for filenames that @codemirror/language-data doesn't cover.
 * Maps exact filenames to a CodeMirror language name (used for matchLanguageName).
 */
const FILENAME_FALLBACK: Record<string, string> = {
  'Makefile': 'Shell',
  'makefile': 'Shell',
  'Dockerfile': 'Dockerfile',
  'dockerfile': 'Dockerfile',
  '.gitignore': 'Shell',
  '.gitmodules': 'Shell',
  '.editorconfig': 'Shell',
  'go.mod': 'Go',
  'go.sum': 'text/plain',
  '.env': 'Shell',
}

/**
 * Extension fallback for types that language-data might map differently
 * from what we want (or not at all).
 */
const EXT_FALLBACK: Record<string, string> = {
  '.toml': 'TOML',
  '.ini': 'text/plain',
  '.cfg': 'text/plain',
  '.conf': 'text/plain',
  '.sum': 'text/plain',
}

/**
 * Detect language name from a file path.
 *
 * Uses LanguageDescription.matchFilename from @codemirror/language-data
 * as the primary lookup, with a fallback map for special filenames.
 * Returns a language name string suitable for passing to loadLanguageByName().
 */
export function detectLanguageFromPath(filePath: string): string {
  const name = filePath.split('/').pop() ?? filePath

  // Try exact filename fallback first
  const fallback = FILENAME_FALLBACK[name]
  if (fallback) return fallback

  // Try extension fallback
  const dotIdx = name.indexOf('.')
  if (dotIdx >= 0) {
    const ext = name.slice(dotIdx).toLowerCase()
    const extFallback = EXT_FALLBACK[ext]
    if (extFallback) return extFallback
  }

  // Use @codemirror/language-data matching
  const desc = LanguageDescription.matchFilename(languages, name)
  if (desc) return desc.name

  return 'text/plain'
}

/**
 * Load a CodeMirror LanguageSupport by name.
 *
 * Finds the matching LanguageDescription in the language-data array,
 * calls .load() (triggers dynamic import), and returns the LanguageSupport
 * extension. Returns null for unknown or plaintext languages.
 */
export async function loadLanguageByName(name: string): Promise<LanguageSupport | null> {
  if (name === 'text/plain' || name === 'plaintext') return null

  const desc = LanguageDescription.matchLanguageName(languages, name, true)
  if (!desc) return null

  // If already loaded, return immediately
  if (desc.support) return desc.support

  return desc.load()
}
