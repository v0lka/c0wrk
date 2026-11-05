// @vitest-environment node
//
// Wails window-access invariant — project-wide guard.
//
// `window.go` (the generated RPC bindings) and `window.runtime` (the Wails
// event/OS surface) are the desktop transport's raw capabilities: RPC dispatch,
// event delivery, clipboard, window title, native browser open, Go-process
// logging. The web transport (web-access sidecar, ADR-101) must be able to
// substitute ALL of them — which is only possible if every access funnels
// through the single shim in `src/api/runtime.ts`. A second access point would
// silently keep desktop-only behavior in the browser (a thrown RPC, a lost
// event, a broken link), so this scan fails fast no matter which file a raw
// access lands in.
//
// Allowed today: `api/runtime.ts` — the transport shim itself.
//
// The real TS parser identifies comments: unlike a regex scan it knows which
// `//`/`/*` sequences are comments and which are string, regex or JSX-text
// content. Comments are masked (many files *document* the window.runtime
// contract in prose); any occurrence in real code — including inside a string,
// which is how an evasion would be written — is a violation.

import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join, relative, sep } from 'node:path'
// The real TS parser is used to identify comments (see the zoom-scan
// invariant for the full rationale: a hand-rolled scanner cannot tell a
// comment from the same characters inside a string, regex or JSX text).
import ts from 'typescript'

// This file lives directly under <src>/test/, so '..' resolves to <src>/.
const SRC_DIR = fileURLToPath(new URL('..', import.meta.url))

/** The single file allowed to touch the raw Wails window surface. */
const ALLOWED = new Set(['api/runtime.ts'])

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

/** Offsets of every REAL comment in `source`, as the TypeScript parser sees them. */
function commentRanges(source: string, fileName: string): Array<readonly [number, number]> {
  // `setParentNodes` is required for `getChildren`, which walks down to token
  // nodes — that is what makes the leading-trivia sweep reach comments an
  // AST-node-only walk would miss (e.g. the sole comment inside a block).
  const kind = fileName.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, /* setParentNodes */ true, kind)
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

/** Blank out comment text while preserving line structure, so violations
 *  still report their true line number. Sorted ranges plus a `start < cursor`
 *  guard skip a comment reported twice (trailing trivia of one node and
 *  leading trivia of the next). */
function maskComments(source: string, ranges: Array<readonly [number, number]>): string {
  let out = ''
  let cursor = 0
  for (const [start, end] of [...ranges].sort((a, b) => a[0] - b[0])) {
    if (start < cursor) continue
    out += source.slice(cursor, start)
    out += source.slice(start, end).replace(/[^\n]/g, ' ')
    cursor = end
  }
  return out + source.slice(cursor)
}

// Dot access (window.go / window.runtime, tolerating whitespace around the
// dot) and the bracket-string evasion (window['go'] / window["runtime"]).
const VIOLATION_PATTERNS = [
  /\bwindow\s*\.\s*(?:go|runtime)\b/,
  /\bwindow\s*\[\s*['"](?:go|runtime)['"]\s*\]/,
]

describe('Wails window-access invariant', () => {
  it('window.go / window.runtime are touched only inside api/runtime.ts', () => { // whole-tree scan
    const violations: string[] = []
    for (const file of collectSources(SRC_DIR)) {
      // `relative()` emits platform separators (backslashes on Windows);
      // normalize to posix so the ALLOWED set matches on every OS.
      const rel = relative(SRC_DIR, file).split(sep).join('/')
      if (ALLOWED.has(rel)) continue
      const source = readFileSync(file, 'utf8')
      const masked = maskComments(source, commentRanges(source, file))
      const lines = masked.split('\n')
      lines.forEach((line, idx) => {
        if (VIOLATION_PATTERNS.some((re) => re.test(line))) {
          violations.push(`${rel}:${idx + 1}`)
        }
      })
    }
    expect(violations).toEqual([])
  }, 30_000)

  it('api/runtime.ts actually contains the accesses the invariant protects', () => {
    // A guard that passes because the allowed file was renamed away (or the
    // shim's accesses removed) would be vacuous — pin the allow-list to real
    // code, comment prose included in neither direction.
    const shim = readFileSync(join(SRC_DIR, 'api', 'runtime.ts'), 'utf8')
    const masked = maskComments(shim, commentRanges(shim, 'api/runtime.ts'))
    const hits = VIOLATION_PATTERNS.reduce(
      (n, re) => n + (masked.match(new RegExp(re.source, 'gm'))?.length ?? 0),
      0,
    )
    expect(hits).toBeGreaterThan(0)
  }, 30_000)
})
