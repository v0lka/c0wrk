// @vitest-environment node
//
// Button-title invariant — scoped to `src/components/chat/**`.
//
// Every button-like element in the chat area exposes its purpose to a user
// who cannot read its visible label, via one of three channels:
//
// 1. a native `title=` attribute — chat buttons are frequently icon-only or
//    truncate their label (panel section headers, combobox triggers,
//    tool-card bodies), so the native tooltip is the only discoverable
//    affordance for hover users — and the accessible name for several;
// 2. a `<TooltipTrigger>` ancestor — the Radix tooltip carries the text
//    (same channel as the project-wide guard);
// 3. a statically visible label — JSX text inside the element (including
//    ternaries whose both branches are string literals). The label IS the
//    explanation, so an echo tooltip over always-labeled text adds nothing;
// 4. a `{...spread}` (skipped — none exist today; trusted to forward a
//    title from the caller).
//
// The channels are MUTUALLY EXCLUSIVE (rule 4 of the title policy in
// specs/domains/frontend/button-tooltips.md): a button under a
// `<TooltipTrigger>` must NOT also carry a `title` — hovering would raise
// the Radix popup AND the native tooltip on top of each other. The scan
// flags that combination as a violation in its own right.
//
// The guard cannot see CSS: markup that may hide its label (responsive
// collapse, overflow) must keep a title regardless — that obligation is
// documented in specs/domains/frontend/button-tooltips.md and enforced at
// review time.
//
// Deliberately stricter than the project-wide guard (buttonTitleInvariant
// .test.ts), which exempts `role="menuitem"` elements: chat menus are built
// exclusively through Radix primitives — a DropdownMenuItem renders a
// `div[role="menuitem"]`, which this `<button>`/`<Button>` scan never sees.
// A `role="menuitem"` on a native button in chat would therefore be a
// hand-rolled one-off that must still carry a title or a static visible
// label. If a future chat surface legitimately needs the menuitem
// exemption, add it here deliberately with its rationale instead of
// assuming the project guard's.
//
// This guard scans the component tree with the real TypeScript parser so a
// re-introduced unlabeled icon-only button fails fast in CI no matter which
// component it lands in. Unlike a regex, the parser knows a `<button` in a
// string literal or a comment from a real JSX element, so prose and fixtures
// cannot produce false positives.

import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join, relative } from 'node:path'
import ts from 'typescript'

// This file lives directly under <src>/test/, so '../components/chat'
// resolves to <src>/components/chat.
const CHAT_DIR = fileURLToPath(new URL('../components/chat', import.meta.url))

/** Recursively collect `.tsx` sources (the only files that can carry JSX),
 * excluding test files. */
function collectSources(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...collectSources(full))
    } else if (/\.tsx$/.test(entry.name) && !/\.test\.tsx$/.test(entry.name)) {
      out.push(full)
    }
  }
  return out
}

/** Native `<button>` elements, lowercase and capitalized (shadcn/ui). */
const BUTTON_TAGS = new Set(['button', 'Button'])

/**
 * Non-native components that render a `<button>` at their root. Radix
 * `CollapsibleTrigger` renders a native `<button>` (without `asChild`), and
 * chat panel headers collapse through it — so it is held to the same rule.
 */
const BUTTON_RENDERING_COMPONENTS = new Set(['CollapsibleTrigger'])

function isButtonTag(tagName: string): boolean {
  return BUTTON_TAGS.has(tagName) || BUTTON_RENDERING_COMPONENTS.has(tagName)
}

/**
 * True when the expression provably renders a non-empty visible string: a
 * string literal (direct or parenthesized) or a ternary whose BOTH branches
 * are provable — including nested ternaries. Deliberately conservative:
 * identifiers (even a lookup in a const table) are not provable and keep
 * the title requirement; whitespace-only text does not count, so an
 * icon-plus-spacing layout still needs its title.
 */
function isStaticString(expr: ts.Expression): boolean {
  if (ts.isParenthesizedExpression(expr)) return isStaticString(expr.expression)
  if (ts.isStringLiteral(expr)) return expr.text.trim() !== ''
  if (ts.isConditionalExpression(expr)) {
    return isStaticString(expr.whenTrue) && isStaticString(expr.whenFalse)
  }
  return false
}

/**
 * True when the element's own children guarantee a visible text label: JSX
 * text with non-whitespace content, or an expression proven by
 * `isStaticString`.
 */
function hasStaticVisibleText(
  node: ts.JsxElement | ts.JsxSelfClosingElement,
  sf: ts.SourceFile,
): boolean {
  if (ts.isJsxSelfClosingElement(node)) return false
  for (const child of node.children) {
    if (ts.isJsxText(child) && child.getText(sf).trim() !== '') return true
    if (!ts.isJsxExpression(child)) continue
    const expr = child.expression
    if (expr === undefined) continue
    if (isStaticString(expr)) return true
  }
  return false
}

/**
 * True when some JSX ancestor opens a `<TooltipTrigger>` tag — the Radix
 * wrapper whose whole purpose is to attach the tooltip text (channel 2).
 * A tooltip from a differently-named wrapper is out of scope, mirroring the
 * project-wide guard.
 */
function hasTooltipTriggerAncestor(node: ts.Node): boolean {
  let cur: ts.Node | undefined = node.parent
  while (cur) {
    if (
      ts.isJsxElement(cur) &&
      cur.openingElement.tagName.getText().split('.').pop() === 'TooltipTrigger'
    ) {
      return true
    }
    cur = cur.parent
  }
  return false
}

/**
 * Inspect one JSX element's attributes.
 *
 * Returns `'untitled'` when no `title` attribute is present, `'dynamic'` when
 * a `{...spread}` makes the attribute set statically unknowable (skipped —
 * none exist today), and `'ok'` otherwise.
 */
function classifyElement(
  attributes: ts.JsxAttributes,
): 'untitled' | 'dynamic' | 'ok' {
  for (const attr of attributes.properties) {
    if (ts.isJsxSpreadAttribute(attr)) return 'dynamic'
    if (ts.isJsxAttribute(attr) && ts.isIdentifier(attr.name) && attr.name.escapedText === 'title') {
      return 'ok'
    }
  }
  return 'untitled'
}

/** All button-like JSX elements violating the title policy in `source` —
 * untitled (no title, no static visible text, no TooltipTrigger) or carrying
 * a title under a TooltipTrigger (the channels never combine) — as
 * `line: tag`. */
function findUntitledButtons(
  source: string,
  fileName: string,
): Array<{ line: number; tag: string }> {
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const found: Array<{ line: number; tag: string }> = []
  const flag = (node: ts.JsxSelfClosingElement | ts.JsxOpeningElement, tag: string): void => {
    found.push({ line: sf.getLineAndCharacterOfPosition(node.getStart()).line + 1, tag })
  }
  const visit = (node: ts.Node): void => {
    if (ts.isJsxSelfClosingElement(node) || ts.isJsxOpeningElement(node)) {
      const tag = node.tagName.getText(sf)
      if (isButtonTag(tag)) {
        const classified = classifyElement(node.attributes)
        const underTrigger = hasTooltipTriggerAncestor(node)
        if (classified === 'ok' && underTrigger) {
          // Rule 4: the tooltip channels never combine — the Radix popup and
          // the native title would both fire on hover.
          flag(node, tag)
        } else if (
          classified === 'untitled' &&
          !underTrigger &&
          // Static-label channel: only a PAIRED element's own JsxElement is
          // examined (its direct text children). A self-closing element has no
          // children — and its parent is the CONTAINER, whose other children's
          // text must never count as this element's label.
          !(ts.isJsxOpeningElement(node) && hasStaticVisibleText(node.parent as ts.JsxElement, sf))
        ) {
          flag(node, tag)
        }
      }
    }
    node.forEachChild(visit)
  }
  visit(sf)
  return found
}

/** Parse a JSX snippet and report whether a button-like element is flagged. */
function flagsSnippet(snippet: string): boolean {
  return findUntitledButtons(snippet, 'sample.tsx').length > 0
}

describe('button-title guard (chat components)', () => {
  const sources = collectSources(CHAT_DIR)

  it('scans a non-trivial number of component files', () => {
    // Sanity: the walk must actually reach the component tree, otherwise a
    // path regression would make the assertion below vacuously pass.
    expect(sources.length).toBeGreaterThan(30)
  })

  it('gives every button-like element a title or a static visible label', () => {
    const offenders: string[] = []
    for (const file of sources) {
      for (const { line, tag } of findUntitledButtons(readFileSync(file, 'utf8'), file)) {
        offenders.push(`${relative(CHAT_DIR, file)}:${line}: <${tag}>`)
      }
    }
    expect(offenders).toEqual([])
  })

  it('flags a native button without a title or label', () => {
    expect(flagsSnippet('<button onClick={f}><X /></button>')).toBe(true)
    expect(flagsSnippet('<button\n  type="button"\n  onClick={f}\n/>')).toBe(true)
    expect(flagsSnippet('<button onClick={f}>{label}</button>')).toBe(true)
  })

  it('passes a button that carries a title (static or bound)', () => {
    expect(flagsSnippet('<button title="Go" onClick={f}>Go</button>')).toBe(false)
    expect(flagsSnippet('<button title={open ? "Collapse" : "Expand"}>x</button>')).toBe(false)
  })

  it('honors the TooltipTrigger channel — no title needed under it', () => {
    expect(flagsSnippet('<TooltipTrigger asChild><button><X /></button></TooltipTrigger>')).toBe(
      false,
    )
    // A dynamic (unprovable) label stops being a problem under the trigger.
    expect(flagsSnippet('<TooltipTrigger asChild><button>{label}</button></TooltipTrigger>')).toBe(
      false,
    )
  })

  it('flags a title on a button under a TooltipTrigger (the channels never combine)', () => {
    expect(flagsSnippet('<TooltipTrigger asChild><button title="x" /></TooltipTrigger>')).toBe(true)
    expect(
      flagsSnippet('<TooltipTrigger><button title={label}>Go</button></TooltipTrigger>'),
    ).toBe(true)
  })

  it('passes a button with a static visible label — no echo title needed', () => {
    expect(flagsSnippet('<button onClick={f}>Go</button>')).toBe(false)
    expect(flagsSnippet('<button>{saving ? "Saving…" : "Save"}</button>')).toBe(false)
    expect(flagsSnippet('<CollapsibleTrigger className="x">Reasoning</CollapsibleTrigger>')).toBe(
      false,
    )
  })

  it('holds capitalized Button and CollapsibleTrigger to the same rule', () => {
    expect(flagsSnippet('<Button size="sm" onClick={f}><X /></Button>')).toBe(true)
    expect(flagsSnippet('<Button size="sm" title="Allow" onClick={f}>Allow</Button>')).toBe(false)
    expect(flagsSnippet('<Button size="sm" onClick={f}>Allow</Button>')).toBe(false)
    expect(flagsSnippet('<CollapsibleTrigger title="Reasoning">y</CollapsibleTrigger>')).toBe(false)
    expect(flagsSnippet('<CollapsibleTrigger className="x" />')).toBe(true)
    // Unrelated components stay out of scope — they may not render a button.
    expect(flagsSnippet('<DialogTrigger>Open</DialogTrigger>')).toBe(false)
  })

  it("does not honor the project guard's menuitem exemption", () => {
    // A hand-rolled menuitem button in chat must still be named: Radix menus
    // render div[role="menuitem"] (invisible to this scan), so a menuitem
    // BUTTON here is a deliberate one-off carrying its own title/label duty.
    expect(flagsSnippet('<button role="menuitem" onClick={f}><X /></button>')).toBe(true)
    expect(flagsSnippet('<button role="menuitem" title="Copy" onClick={f}><X /></button>')).toBe(
      false,
    )
  })

  it('skips spread-attribute elements instead of guessing', () => {
    expect(flagsSnippet('<button {...rest}>Go</button>')).toBe(false)
  })

  it('does not flag buttons inside strings or comments', () => {
    expect(flagsSnippet('const s = "<button>not jsx</button>"')).toBe(false)
    expect(flagsSnippet('// <button onClick={f}>comment</button>')).toBe(false)
  })

  it('reports accurate line numbers', () => {
    const src = 'const a = 1\nconst b = 2\nconst c = <button><X /></button>\n'
    expect(findUntitledButtons(src, 'sample.tsx')).toEqual([{ line: 3, tag: 'button' }])
  })
})
