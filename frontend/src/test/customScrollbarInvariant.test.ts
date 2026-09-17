// @vitest-environment node
//
// Custom-scrollbar invariant — project-wide guard.
//
// The app-wide scroll look is the `.custom-scrollbar` utility defined in
// index.css (8px, semi-transparent rounded thumb). A scroll container written
// without it renders the browser's built-in scrollbar instead — a visual
// mismatch that has crept in repeatedly (papers list, research dashboard,
// git-config risk toast, paper block equations). This guard scans the source
// tree so a scroll-enabling `overflow-*` utility that appears without
// `custom-scrollbar` (or the deliberate `no-scrollbar` hide) fails fast in CI
// no matter which component it lands in.
//
// Third-party internal scrollers cannot carry the class on their own elements
// and are therefore styled with dedicated CSS instead — CodeMirror's
// `.cm-scroller` and xterm's `.xterm-viewport` both reuse the exact
// `.custom-scrollbar` look via rules in index.css. Those live outside the
// TS tree and are covered by the index.css assertions below.

import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join, relative } from 'node:path'
// The real TS parser is used to identify comments: unlike a regex it knows
// which `//`/`/*` sequences are comments and which are string, regex or
// JSX-text content (see zoomViewportInvariant.test.ts for the full rationale
// and the parser-configuration notes — the helper below is the same
// proven one, restated so each guard file stays self-contained).
import ts from 'typescript'

// This file lives directly under <src>/test/, so '..' resolves to <src>/.
const SRC_DIR = fileURLToPath(new URL('..', import.meta.url))

/** Recursively collect `.ts`/`.tsx` sources, excluding test files. */
function collectSources(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...collectSources(full))
    } else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) {
      out.push(full)
    }
  }
  return out
}

/**
 * Offsets of every REAL comment in `source`, as the TypeScript parser sees
 * them (comments inside strings, regexes or JSX text are not comments there).
 */
function commentRanges(
  source: string,
  fileName: string,
  kind: ts.ScriptKind,
): Array<readonly [number, number]> {
  const sf = ts.createSourceFile(
    fileName,
    source,
    ts.ScriptTarget.Latest,
    /* setParentNodes */ true,
    kind,
  )
  const ranges: Array<readonly [number, number]> = []
  const add = (rs: readonly ts.CommentRange[] | undefined): void => {
    if (rs) for (const r of rs) ranges.push([r.pos, r.end])
  }
  const visit = (node: ts.Node): void => {
    add(ts.getLeadingCommentRanges(source, node.pos))
    for (const child of node.getChildren(sf)) visit(child)
    add(ts.getTrailingCommentRanges(source, node.end))
  }
  visit(sf)
  return ranges
}

/**
 * Blank every comment span (newlines kept) so code — including string,
 * template and regex literals, and JSX text — is scanned, while comment prose
 * (which legitimately mentions `overflow-auto` to explain scroll behavior) is
 * not. Offsets and line structure are preserved exactly, so reported line
 * numbers stay accurate.
 */
function stripComments(source: string, fileName: string): string {
  const kind = fileName.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  const ranges = [...commentRanges(source, fileName, kind)].sort((a, b) => a[0] - b[0])
  let out = ''
  let cursor = 0
  for (const [start, end] of ranges) {
    if (start < cursor) continue
    out += source.slice(cursor, start)
    out += source.slice(start, end).replace(/[^\n]/g, ' ')
    cursor = end
  }
  return out + source.slice(cursor)
}

/**
 * A scroll-enabling Tailwind overflow utility: `overflow-auto`,
 * `overflow-y-auto`, `overflow-x-auto` or `overflow-scroll`. The trailing
 * `(?![\w-])` excludes longer identifiers (`overflowAutoVar` etc.). Hiding
 * utilities (`overflow-hidden`, `overflow-x-hidden`) never render a scrollbar
 * and are deliberately not matched.
 */
const SCROLL_UTILITY = /\boverflow-(?:auto|y-auto|x-auto|scroll)(?![\w-])/

/**
 * The accepted scrollbar treatments: the app-wide `.custom-scrollbar` look, or
 * the deliberate `no-scrollbar` full hide (tab strips keep wheel-scrolling
 * without a gutter).
 */
const SCROLLBAR_TOKEN = /\b(?:custom|no)-scrollbar\b/

describe('custom-scrollbar guard (scroll-look invariant)', () => {
  const sources = collectSources(SRC_DIR)

  it('scans a non-trivial number of source files', () => {
    // Sanity: the walk must actually reach the component tree, otherwise a
    // path regression would make the assertion below vacuously pass.
    expect(sources.length).toBeGreaterThan(50)
  })

  it('every scroll-enabling overflow utility carries custom-scrollbar or no-scrollbar', () => {
    const offenders: string[] = []
    for (const file of sources) {
      const lines = stripComments(readFileSync(file, 'utf8'), file).split('\n')
      lines.forEach((line, i) => {
        if (SCROLL_UTILITY.test(line) && !SCROLLBAR_TOKEN.test(line)) {
          offenders.push(`${relative(SRC_DIR, file)}:${i + 1}: ${line.trim()}`)
        }
      })
    }
    expect(offenders).toEqual([])
  })

  it('flags scroll utilities without a scrollbar token (and passes styled ones)', () => {
    // The historical offenders.
    expect(SCROLL_UTILITY.test('className="flex-1 overflow-auto"')).toBe(true)
    expect(SCROLL_UTILITY.test('className="max-h-56 overflow-y-auto"')).toBe(true)
    expect(SCROLL_UTILITY.test("cn('block overflow-x-auto', className)")).toBe(true)
    // Styled/hided scroll containers must pass the compound rule.
    const styled = 'className="flex-1 overflow-auto custom-scrollbar"'
    expect(SCROLL_UTILITY.test(styled) && !SCROLLBAR_TOKEN.test(styled)).toBe(false)
    const hidden = 'className="flex overflow-x-auto no-scrollbar"'
    expect(SCROLL_UTILITY.test(hidden) && !SCROLLBAR_TOKEN.test(hidden)).toBe(false)
    // Non-scrolling overflow utilities never render a scrollbar.
    expect(SCROLL_UTILITY.test('className="overflow-hidden"')).toBe(false)
    expect(SCROLL_UTILITY.test('className="overflow-x-hidden overflow-y-auto custom-scrollbar"')).toBe(
      // overflow-y-auto DOES match (scroll-enabling), but the token is present.
      true,
    )
    expect(
      SCROLL_UTILITY.test('className="overflow-x-hidden"') &&
        !SCROLLBAR_TOKEN.test('className="overflow-x-hidden"'),
    ).toBe(false)
    // Longer identifiers are not the utility.
    expect(SCROLL_UTILITY.test('const overflowAutoFlag = true')).toBe(false)
    expect(SCROLL_UTILITY.test('overflow-autosave')).toBe(false)
  })

  it('ignores overflow utilities mentioned in comments', () => {
    // Explanatory prose legitimately mentions the utility; the parser-based
    // stripper must keep the code line scannable while blanking the comment.
    const src = '// pane uses overflow-auto for drag-to-pan\nconst cls = "overflow-auto custom-scrollbar"\n'
    const stripped = stripComments(src, 'sample.tsx')
    expect(stripped).not.toContain('for drag-to-pan')
    expect(stripped).toContain('overflow-auto custom-scrollbar')
    // A violation hiding inside a comment must not be flagged either — the
    // guard's subject is rendered class strings, not documentation.
    const src2 = '/* old code: overflow-auto */\nconst t = 1\n'
    expect(SCROLL_UTILITY.test(stripComments(src2, 'sample.tsx'))).toBe(false)
  })

  it('strips comments without swallowing code after a comment-opener inside a string', () => {
    // A `src/**`-style string contains a comment-opener sequence; a naive
    // block regex would delete everything up to the next closer — hiding real
    // violations and shifting every later line number.
    const src = 'const p = "src/**"\nconst h = "overflow-auto"\nconst t = 1\n'
    const stripped = stripComments(src, 'sample.tsx')
    expect(stripped).toContain('overflow-auto')
    expect(stripped).toContain('const t = 1')
    expect(stripped).toHaveLength(src.length)
  })

  it('is not desynced by a lone backtick or apostrophe in JSX text', () => {
    const src = "<p>Press the ` key, it's fine</p>\n// used overflow-auto\nconst y = 1\n"
    const stripped = stripComments(src, 'sample.tsx')
    expect(stripped).not.toContain('used overflow-auto')
    expect(stripped).toContain('const y = 1')
    expect(stripped).toHaveLength(src.length)
  })

  it('defines the .custom-scrollbar look and the third-party scroller rules in index.css', () => {
    const css = readFileSync(join(SRC_DIR, 'index.css'), 'utf8')
    // The class every scroll container relies on…
    expect(css).toMatch(/\.custom-scrollbar::-webkit-scrollbar\b/)
    // …plus the dedicated look-alike rules for third-party internal scrollers
    // that cannot carry the class (CodeMirror editor scroller, xterm
    // scrollback viewport).
    expect(css).toMatch(/\.cm-viewer-container \.cm-scroller::-webkit-scrollbar\b/)
    expect(css).toMatch(/\.xterm \.xterm-viewport::-webkit-scrollbar\b/)
  })
})
