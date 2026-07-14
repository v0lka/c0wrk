import { describe, it, expect, beforeAll } from 'vitest'
import { registerLanguages } from '@/lib/hljsLanguages'
import { detectHljsLanguage, highlightCodeLine } from './hunkCodeHighlight'

// Register hljs languages once before running the suite — mirrors what
// main.tsx does at app startup.
beforeAll(() => {
  registerLanguages()
})

describe('detectHljsLanguage', () => {
  it('detects common extensions', () => {
    expect(detectHljsLanguage('main.go')).toBe('go')
    expect(detectHljsLanguage('src/app.tsx')).toBe('typescript')
    expect(detectHljsLanguage('script.js')).toBe('javascript')
    expect(detectHljsLanguage('model.py')).toBe('python')
    expect(detectHljsLanguage('main.rs')).toBe('rust')
    expect(detectHljsLanguage('Gemfile.rb')).toBe('ruby')
    expect(detectHljsLanguage('deploy.sh')).toBe('bash')
    expect(detectHljsLanguage('style.css')).toBe('css')
    expect(detectHljsLanguage('config.yaml')).toBe('yaml')
    expect(detectHljsLanguage('data.json')).toBe('json')
    expect(detectHljsLanguage('query.sql')).toBe('sql')
    expect(detectHljsLanguage('README.md')).toBe('markdown')
  })

  it('detects special filenames without extensions', () => {
    expect(detectHljsLanguage('Dockerfile')).toBe('dockerfile')
    expect(detectHljsLanguage('dockerfile')).toBe('dockerfile')
    expect(detectHljsLanguage('Makefile')).toBe('bash')
    expect(detectHljsLanguage('makefile')).toBe('bash')
  })

  it('handles nested paths', () => {
    expect(detectHljsLanguage('backend/session/persistence.go')).toBe('go')
    expect(detectHljsLanguage('frontend/src/components/review/HunkReviewBlock.tsx')).toBe('typescript')
  })

  it('returns null for unknown extensions', () => {
    expect(detectHljsLanguage('data.csv')).toBeNull()
    expect(detectHljsLanguage('image.png')).toBeNull()
    expect(detectHljsLanguage('unknown.xyz')).toBeNull()
  })

  it('returns null for files without extension', () => {
    expect(detectHljsLanguage('LICENSE')).toBeNull()
    expect(detectHljsLanguage('CHANGELOG')).toBeNull()
  })

  it('is case-insensitive for extensions', () => {
    expect(detectHljsLanguage('Main.GO')).toBe('go')
    expect(detectHljsLanguage('App.TSX')).toBe('typescript')
  })
})

describe('highlightCodeLine', () => {
  it('highlights Go code with hljs span classes', () => {
    const html = highlightCodeLine('func main() {', 'go')
    expect(html).toContain('hljs-keyword')
    expect(html).toContain('func')
    expect(html).toContain('main')
  })

  it('highlights TypeScript code', () => {
    const html = highlightCodeLine('const x: number = 42', 'typescript')
    expect(html).toContain('hljs-keyword')
    expect(html).toContain('const')
  })

  it('highlights Python strings', () => {
    const html = highlightCodeLine("print('hello')", 'python')
    expect(html).toContain('hljs-string')
    expect(html).toContain('hello')
  })

  it('returns escaped plain text for null language', () => {
    const html = highlightCodeLine('some <code> here', null)
    expect(html).toBe('some &lt;code&gt; here')
    expect(html).not.toContain('hljs-')
  })

  it('returns escaped text for empty input', () => {
    expect(highlightCodeLine('', 'go')).toBe('')
  })

  it('escapes HTML special characters in code', () => {
    const html = highlightCodeLine('if (a < b && c > d)', 'go')
    // The < and > must be escaped even inside hljs spans.
    expect(html).not.toContain(' < ')
    expect(html).not.toContain(' > ')
    expect(html).toContain('&lt;')
    expect(html).toContain('&gt;')
  })

  it('falls back to escaped text for unregistered language', () => {
    const html = highlightCodeLine('some code', 'cobol')
    expect(html).toBe('some code')
    expect(html).not.toContain('hljs-')
  })
})
