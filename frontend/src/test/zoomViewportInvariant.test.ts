// @vitest-environment node
//
// UI-scale zoom invariant — project-wide guard.
//
// The app-wide UI scale is applied as CSS `zoom` on <html>. `zoom`
// pre-multiplies the used value of every <length> — including viewport units
// (`100vh`/`100vw`) — while leaving percentages untouched. A viewport-unit
// size written in component code therefore renders magnified at any scale
// ≠ 100%: a dialog capped with `max-h-[80vh]` can grow taller than the window
// (its controls become unreachable), and a full-bleed shell forces horizontal
// + vertical document scrollbars around the whole app.
//
// Viewport-derived sizes must go through `--ui-vh` — the zoom-corrected `100vh`
// defined in index.css (`calc(100vh / var(--ui-zoom, 1))`) — or percentages;
// never a raw viewport unit or a `*-screen` full-bleed utility. This guard
// scans the source tree so a re-introduced raw unit fails fast in CI no matter
// which component it lands in.
//
// The second half of the same invariant covers POSITION. `zoom` also magnifies
// the used value of a `left`/`top` length, while `MouseEvent.clientX/clientY`
// and `getBoundingClientRect()` report VISUAL px (layout px × zoom). Writing a
// pointer coordinate straight into a fixed panel's `left`/`top` therefore
// displaces it from the cursor by `coordinate × (zoom − 1)`. Pointer-anchored
// panels must route through `lib/cursorMenuPosition` (or, for a
// trigger-anchored dropdown, `lib/layoutSpace`), which converts to layout px
// and fits the panel inside the visible window. See `lib/layoutSpace.ts` for
// the measured coordinate model.

import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join, relative } from 'node:path'
// The real TS parser is used to identify comments: unlike a regex or a
// hand-rolled scanner it knows which `//`/`/*` sequences are comments and which
// are string, regex or JSX-text content.
import ts from 'typescript'

// This file lives directly under <src>/test/, so '..' resolves to <src>/.
const SRC_DIR = fileURLToPath(new URL('..', import.meta.url))

// The HTML entry point (one level above <src>/) is viewport-sensitive too — an
// inline `style="height: 100vh"` there would be invisible to the src-only walk
// — so it is scanned alongside the sources. index.css is deliberately NOT
// scanned: it is where the zoom-corrected `--ui-vh` primitive is defined.
const HTML_ENTRY = fileURLToPath(new URL('../../index.html', import.meta.url))

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
 * them.
 *
 * A hand-rolled character scanner cannot tell a comment from the same
 * characters in another context: a `slash-slash` or `slash-star` inside a JS
 * string literal, a regex literal, or JSX TEXT (a bare URL, a rendered
 * `src/**` glob) is not a comment there, while a JSX comment
 * (`brace-slash-star … star-slash-brace`) is. The parser resolves string,
 * regex, template and JSX-comment contexts correctly by construction, so the
 * spans it reports are exactly the ones that must not be scanned. (The only
 * theoretical gap is a JSX text node whose own content begins with a comment
 * marker, which `getLeadingCommentRanges` still reads as trivia — that can only
 * ever hide a violation inside that text, never invent one.)
 */
function commentRanges(
  source: string,
  fileName: string,
  kind: ts.ScriptKind,
): Array<readonly [number, number]> {
  // `setParentNodes` is required for `getChildren`, which walks down to token
  // nodes — that is what makes the leading-trivia sweep reach comments an
  // AST-node-only walk would miss (e.g. the sole statement of a block).
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
 * (which legitimately mentions viewport units to explain this rule) is not.
 * Offsets and line structure are preserved exactly, so reported line numbers
 * stay accurate.
 *
 * The parser's script kind must match the file: a `.ts` file parsed as TSX
 * mis-reads TS-only syntax — an unparenthesised generic arrow `generic<T>(…)`
 * looks like a JSX opening tag — and stops attributing comments after it, which
 * would make the scan fail on that file's own explanatory comments. The HTML
 * entry is not TypeScript at all and is scanned verbatim (it carries no comment
 * prose that mentions a viewport unit).
 */
function stripComments(source: string, fileName: string): string {
  if (fileName.endsWith('.html')) return source
  const kind = fileName.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  // Sorted, and the `start < cursor` guard skips a comment reported twice
  // (once as trailing trivia of one node, once as leading trivia of the next).
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

/** A raw viewport-unit length: a number (optionally signed, or with a dot-led
 * mantissa, so `-50vh` and `.5vh` are caught too) immediately followed by a
 * unit. The unit set is every CSS viewport-percentage unit Tailwind's length
 * validator accepts in an arbitrary value (`h-[50vmin]`): `vh`/`vw`, the
 * dynamic/small/large variants (`dvh`/`dvw`/`svh`/`svw`/`lvh`/`lvw`), and the
 * logical/min-max forms (`vmin`/`vmax`/`vi`/`vb`). `zoom` magnifies all of them
 * identically, so all must be caught. The leading class is `[^\w]` —
 * deliberately not the narrower `[^\w.-]`, which would skip a signed or
 * dot-led length; `--ui-vh` and `var(--ui-zoom)` contain no digits and still
 * do not match. */
const VIEWPORT_UNIT =
  /(?:^|[^\w])[-+]?(?:\d+\.?\d*|\.\d+)(?:vmin|vmax|vh|vw|vi|vb|dvh|dvw|svh|svw|lvh|lvw)\b/

/**
 * Tailwind full-bleed utilities, which expand to `100vh`/`100vw` and are
 * magnified by `zoom` exactly like a literal unit. The trailing `(?![\w-])`
 * excludes breakpoint forms (`max-w-screen-sm` = 640px, not 100vw).
 */
const SCREEN_UTILITY = /\b(?:h|w|min-h|max-h|min-w|max-w)-screen(?![\w-])/

/**
 * Tailwind's viewport-unit size utilities (`h-dvh`, `min-h-svh`, `w-dvw`, …),
 * which expand to `100dvh`/`100svw`/… — the same units, magnified by `zoom`
 * identically. `VIEWPORT_UNIT` matches only the bracketed literal form
 * (`h-[100dvh]`), so the bare utility spellings need their own pattern. The
 * trailing `(?![\w-])` excludes longer class names.
 */
const VIEWPORT_UNIT_UTILITY =
  /\b(?:h|w|min-h|max-h|min-w|max-w|size)-(?:dvh|dvw|svh|svw|lvh|lvw)(?![\w-])/

/**
 * A `left`/`top` style entry fed from a raw anchor coordinate — a pointer point
 * (`e.clientX/clientY`) or a bare `.x`/`.y` field. Such a value is in VISUAL px
 * while the property it lands in is LAYOUT px, so the panel opens away from the
 * cursor by `coordinate × (zoom − 1)`. Route it through
 * `lib/cursorMenuPosition` instead. (`left: position.left` — already layout px,
 * e.g. from `lib/dropdownPosition` — deliberately does not match.)
 */
const RAW_ANCHOR_POSITION = /(?:^|[^-\w])(?:left|top)\s*:[^,;{}\n]*\.(?:clientX|clientY|x|y)\b/

describe('viewport-unit guard (UI-scale zoom invariant)', () => {
  const sources = collectSources(SRC_DIR)

  it('scans a non-trivial number of source files', () => {
    // Sanity: the walk must actually reach the component tree, otherwise a
    // path regression would make the assertion below vacuously pass.
    expect(sources.length).toBeGreaterThan(50)
  }, 30_000)

  it('never sizes with a raw viewport unit or a viewport-size utility', () => { // whole-tree scan: generous bound under parallel suite load
    const offenders: string[] = []
    for (const file of [...sources, HTML_ENTRY]) {
      const lines = stripComments(readFileSync(file, 'utf8'), file).split('\n')
      lines.forEach((line, i) => {
        if (
          VIEWPORT_UNIT.test(line) ||
          SCREEN_UTILITY.test(line) ||
          VIEWPORT_UNIT_UTILITY.test(line)
        ) {
          offenders.push(`${relative(SRC_DIR, file)}:${i + 1}: ${line.trim()}`)
        }
      })
    }
    expect(offenders).toEqual([])
  }, 30_000)

  it('never positions a floating panel from a raw anchor coordinate', () => { // whole-tree scan
    const offenders: string[] = []
    for (const file of sources) {
      const lines = stripComments(readFileSync(file, 'utf8'), file).split('\n')
      lines.forEach((line, i) => {
        if (RAW_ANCHOR_POSITION.test(line)) {
          offenders.push(`${relative(SRC_DIR, file)}:${i + 1}: ${line.trim()}`)
        }
      })
    }
    expect(offenders).toEqual([])
  }, 30_000)

  it('the raw-anchor pattern catches the shape it guards against (and not layout-px ones)', () => {
    // The historical bug: a pointer coordinate written into a fixed panel.
    expect(
      RAW_ANCHOR_POSITION.test("style={{ position: 'fixed', left: position.x, top: position.y }}"),
    ).toBe(true)
    expect(RAW_ANCHOR_POSITION.test('left: e.clientX')).toBe(true)
    expect(RAW_ANCHOR_POSITION.test('top: cursor.y')).toBe(true)
    // Values that are already layout px must stay unflagged.
    expect(RAW_ANCHOR_POSITION.test('left: menuPosition?.left ?? 0')).toBe(false)
    expect(RAW_ANCHOR_POSITION.test('top: position?.top ?? 0')).toBe(false)
    expect(RAW_ANCHOR_POSITION.test('const delta = startY - e.clientY')).toBe(false)
  }, 30_000)

  it('strips comments without swallowing code after a comment-opener inside a string', () => {
    // The `src/**` glob contains a comment-opener sequence; a naive block
    // regex would delete everything up to the next closer — hiding real
    // violations and shifting every later line number. The scanner must leave
    // the string and the code that follows it untouched.
    const src = 'const p = "src/**"\nconst h = "max-h-[80vh]"\nconst t = 1\n'
    const stripped = stripComments(src, 'sample.tsx')
    expect(stripped).toContain('80vh')
    expect(stripped).toContain('const t = 1')
    // Length and line count are preserved, so reported line numbers are exact.
    expect(stripped).toHaveLength(src.length)
    expect(stripped.split('\n')).toHaveLength(src.split('\n').length)
  }, 30_000)

  it('blanks real comments while still scanning string literals', () => {
    const src = "const a = '100vh' // 80vw\n/* 60dvh */\nconst b = 2\n"
    const stripped = stripComments(src, 'sample.tsx')
    // Real comments are removed…
    expect(stripped).not.toContain('80vw')
    expect(stripped).not.toContain('60dvh')
    // …but a unit inside a string literal is still scanned.
    expect(stripped).toContain("'100vh'")
    expect(stripped).toContain('const b = 2')
    expect(stripped).toHaveLength(src.length)
  }, 30_000)

  it('does not treat `//` or `/*` inside JSX text as a comment', () => {
    // A bare URL or a rendered `src/**` glob in JSX text is literal content,
    // not a comment; mistaking it for one would blank — and so skip — real code.
    const src =
      'const el = <p>See https://example.com and src/** ok</p>\n// cap 80vh\nconst x = 1\n'
    const stripped = stripComments(src, 'sample.tsx')
    expect(stripped).toContain('https://example.com')
    expect(stripped).toContain('src/**')
    expect(stripped).not.toContain('80vh')
    expect(stripped).toContain('const x = 1')
    expect(stripped).toHaveLength(src.length)
  }, 30_000)

  it('is not desynced by a lone backtick or apostrophe in JSX text', () => {
    const src = "<p>Press the ` key, it's fine</p>\n// cap of 80vh\nconst y = 1\n"
    const stripped = stripComments(src, 'sample.tsx')
    expect(stripped).not.toContain('80vh')
    expect(stripped).toContain('const y = 1')
    expect(stripped).toHaveLength(src.length)
  }, 30_000)

  it('parses a .ts file as TS so a generic arrow does not hide later comments', () => {
    // In a `.ts` file the bare `<T>(x: T)` is an unparenthesised generic arrow;
    // parsing that file as TSX would read it as a JSX opening tag and stop
    // attributing comments after it — so the file's own explanatory comment
    // would be scanned and flagged.
    const src = '// a cap of 80vh\nconst id = <T>(x: T): T => x\n// after 90vh\nconst y = 1\n'
    const stripped = stripComments(src, 'sample.ts')
    expect(stripped).not.toContain('80vh')
    expect(stripped).not.toContain('90vh')
    expect(stripped).toContain('const y = 1')
    expect(stripped).toHaveLength(src.length)
  }, 30_000)

  it('flags signed and dot-led viewport lengths too', () => {
    for (const snippet of [
      "transform: 'translateY(-50vh)'",
      'className="translate-y-[-100vh]"',
      'top: -100vh',
      'marginLeft: .5vw',
    ]) {
      expect(VIEWPORT_UNIT.test(snippet)).toBe(true)
    }
    // Positive forms still match; the zoom primitives never do.
    expect(VIEWPORT_UNIT.test('className="h-[80vh]"')).toBe(true)
    expect(VIEWPORT_UNIT.test('calc(var(--ui-vh) * 0.8)')).toBe(false)
    expect(VIEWPORT_UNIT.test('var(--ui-zoom, 1)')).toBe(false)
  }, 30_000)

  it('flags every viewport-percentage unit, not just vh/vw', () => {
    // Tailwind accepts all of these in an arbitrary value; `zoom` magnifies
    // each one, so the guard must catch the whole set.
    for (const unit of ['vmin', 'vmax', 'vi', 'vb', 'dvh', 'svh', 'lvh', 'dvw']) {
      expect(VIEWPORT_UNIT.test(`className="h-[50${unit}]"`)).toBe(true)
    }
    // Prefixes that merely start with a unit token are not lengths.
    expect(VIEWPORT_UNIT.test('const vipers = 1')).toBe(false)
    expect(VIEWPORT_UNIT.test('className="h-full"')).toBe(false)
  }, 30_000)

  it('flags Tailwind viewport-size utilities as well as bracketed literals', () => {
    for (const cls of ['h-dvh', 'min-h-svh', 'w-dvw', 'max-h-lvh', 'min-w-svw', 'size-dvh']) {
      expect(VIEWPORT_UNIT_UTILITY.test(`<div className="${cls}" />`)).toBe(true)
    }
    // Bracketed literals are VIEWPORT_UNIT's job; unrelated classes and plain
    // identifiers must not be flagged.
    expect(VIEWPORT_UNIT_UTILITY.test('className="h-[100dvh]"')).toBe(false)
    expect(VIEWPORT_UNIT_UTILITY.test('className="max-w-screen-sm"')).toBe(false)
    expect(VIEWPORT_UNIT_UTILITY.test('className="h-full w-full"')).toBe(false)
    expect(VIEWPORT_UNIT_UTILITY.test('const pathDvh = 1')).toBe(false)
  }, 30_000)

  it('defines the zoom-corrected --ui-vh primitive in index.css', () => {
    const css = readFileSync(join(SRC_DIR, 'index.css'), 'utf8')
    expect(css).toMatch(/--ui-vh:\s*calc\(100vh\s*\/\s*var\(--ui-zoom/)
  }, 30_000)
})
