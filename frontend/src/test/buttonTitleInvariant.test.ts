// @vitest-environment node
//
// Button title invariant — project-wide guard.
//
// Every native `<button>` and `<Button>` component in non-test sources must
// expose its purpose to a user who cannot read its visible label. One of
// three channels satisfies the invariant:
//
// 1. a native `title=` attribute — the hover tooltip the desktop app shows
//    on any element;
// 2. a `<TooltipTrigger>` ancestor — the Radix tooltip carries the text;
// 3. a statically visible label — JSX text inside the element (including
//    ternaries whose both branches are string literals). The label IS the
//    explanation, so an echo tooltip over always-labeled text adds nothing.
//
// The channels are MUTUALLY EXCLUSIVE (rule 4 of the title policy in
// specs/domains/frontend/button-tooltips.md): a button under a
// `<TooltipTrigger>` must NOT also carry a `title` — hovering would raise
// the Radix popup AND the native tooltip on top of each other. The scan
// flags that combination as a violation in its own right.
//
// A bare ICON-ONLY button — or one whose markup hides its label (responsive
// `hidden @min-[…]:inline` chrome, collapsed layouts) — leaves a user with
// no way to learn what it does, which is what the guard exists to prevent.
// The guard cannot see CSS, so "this label can disappear" is a review-time
// obligation (see specs/domains/frontend/button-tooltips.md): markup that
// may hide its label MUST keep a title even when the label is present in
// the source. The guard accepts the static text; the spec mandates the
// title for hide-capable buttons.
//
// Context-menu entries are exempt: a button carrying `role="menuitem"` is a
// menu entry whose visible label IS its explanation — the pointer already
// sits on the rendered text while the menu is open, so a tooltip would only
// repeat it verbatim. The exemption keys off the ARIA role, not the file or
// component, so a plain button can never smuggle itself out of the scan.
//
// The scan is AST-based: `ts.createSourceFile` knows which `<button`
// sequences are JSX tags and which are comment prose, string literals or JSX
// text, so a comment mentioning `<button` can never flag — unlike a regex
// sweep, which needs a separate comment-stripping pass (see
// `zoomViewportInvariant.test.ts` for that variant). The AST is also what the
// ancestor check needs: whether a button sits inside `<TooltipTrigger>` is a
// parent-chain question a line scanner cannot answer.
//
// Documented limitations (both deliberate, fail-open in the direction of
// trusting existing code):
// - a `{...spread}` on a button is trusted to carry `title` from the caller —
//   props-forwarding wrappers (`<button {...props}>`) are exactly how a title
//   set at the call site reaches the DOM;
// - a button rendered by an unrelated component (`<FooButton />` whose
//   internals forgot the title) is invisible here — only intrinsic `button`
//   tags and components whose name ends in `.Button`/`Button` are matched;
// - static-label acceptance trusts that the text is actually visible — a
//   label hidden by CSS (`<span className="hidden">`) fools the scan, which
//   is why the review-time rule above requires titles on hide-capable
//   markup regardless of what the guard accepts.

import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join, relative } from 'node:path'
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

/** Dotted source text of a JSX tag name: `button`, `Button`, `Foo.Bar`. */
function jsxTagLabel(tagName: ts.JsxTagNameExpression): string {
  if (ts.isIdentifier(tagName)) return tagName.text
  if (ts.isPropertyAccessExpression(tagName)) {
    const base = jsxTagLabel(tagName.expression)
    return base ? `${base}.${tagName.name.text}` : tagName.name.text
  }
  if (ts.isJsxNamespacedName(tagName)) return `${tagName.namespace.text}:${tagName.name.text}`
  return ''
}

/**
 * The tag shapes this invariant guards. `button` is the intrinsic element;
 * `Button` (also as the last segment of a dotted name, `Tooltip.Button`) is
 * the shadcn/ui primitive every button in the app wraps. Other components
 * (`IconButton`, `DropdownMenuTrigger`, …) are out of scope: the invariant
 * stops where a component's own contract begins.
 */
function isButtonTag(label: string): boolean {
  if (label === 'button') return true
  const last = label.split('.').pop() ?? label
  return last === 'Button'
}

function attrNameIsTitle(name: ts.JsxAttributeName): boolean {
  if (ts.isIdentifier(name)) return name.text === 'title'
  if (ts.isJsxNamespacedName(name)) return name.namespace.text === 'title'
  return false
}

/**
 * True when the element's own attribute list declares `role="menuitem"` —
 * the context-menu exemption (see the header note): a menu entry's visible
 * label is its explanation, so it needs no `title` tooltip.
 */
function hasMenuitemRole(attributes: ts.JsxAttributes): boolean {
  for (const prop of attributes.properties) {
    if (ts.isJsxAttribute(prop) && ts.isIdentifier(prop.name) && prop.name.text === 'role') {
      const init = prop.initializer
      return init !== undefined && ts.isStringLiteral(init) && init.text === 'menuitem'
    }
  }
  return false
}

/**
 * True when the element's own attribute list satisfies the invariant: an
 * explicit `title=`/`title:`/`title:<expr>` attribute — any value counts, the
 * attribute's mere presence is the contract — or a `{...spread}` (trusted, see
 * the header note).
 */
function hasOwnTitle(attributes: ts.JsxAttributes): boolean {
  for (const prop of attributes.properties) {
    if (ts.isJsxAttribute(prop)) {
      if (attrNameIsTitle(prop.name)) return true
    } else {
      // JsxSpreadAttribute: `{...rest}` — the spread may carry a title.
      return true
    }
  }
  return false
}

/**
 * True when some JSX ancestor opens a `<TooltipTrigger>` tag — the Radix
 * wrapper whose whole purpose is to attach the tooltip text. Wrapping via a
 * conditional child (`<TooltipTrigger>{ok && <button/>}</TooltipTrigger>`)
 * still counts: the intermediate nodes all descend from the TooltipTrigger
 * element. A tooltip provided by a differently-named wrapper is out of scope
 * (see header).
 */
function hasTooltipTriggerAncestor(node: ts.Node): boolean {
  let cur: ts.Node | undefined = node.parent
  while (cur) {
    if (
      ts.isJsxElement(cur) &&
      (jsxTagLabel(cur.openingElement.tagName) ?? '').split('.').pop() === 'TooltipTrigger'
    ) {
      return true
    }
    cur = cur.parent
  }
  return false
}

/**
 * True when the expression provably renders a non-empty visible string:
 * a string literal (direct or parenthesized) or a ternary whose BOTH
 * branches are provable — `{busy ? 'Saving…' : 'Save'}` and the nested
 * `{busy ? '…' : kind === 'x' ? 'A' : 'B'}` forms. Deliberately
 * conservative: identifiers (even a lookup in a const table) are not
 * provable and keep the title requirement.
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
 * `isStaticString`. This is the third invariant channel: the label IS the
 * explanation, so a tooltip over always-labeled text can only echo it.
 *
 * Whitespace-only text does not count, so an icon-plus-spacing layout still
 * needs its title.
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

export interface TitleViolation {
  file: string
  line: number
  snippet: string
}

/**
 * Parse `source` and report every `<button>`/`<Button>` start tag that has
 * neither a `title` attribute nor a `<TooltipTrigger>` ancestor — or that
 * illegally combines a `title` with a `<TooltipTrigger>` ancestor (the
 * double-tooltip combination, see the header note). Comments and strings
 * are ignored by construction: only real JSX tags are visited.
 */
export function scanButtonTitles(source: string, fileName = 'sample.tsx'): TitleViolation[] {
  // The script kind must match the file extension: a `.ts` file parsed as TSX
  // mis-reads TS-only syntax (an unparenthesised generic arrow looks like a
  // JSX tag) and could invent tags that do not exist.
  const kind = fileName.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  const sf = ts.createSourceFile(
    fileName,
    source,
    ts.ScriptTarget.Latest,
    /* setParentNodes */ true,
    kind,
  )
  const violations: TitleViolation[] = []
  const report = (node: ts.Node): void => {
    const line = sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1
    violations.push({
      file: fileName,
      line,
      snippet: (source.split('\n')[line - 1] ?? '').trim(),
    })
  }
  const visit = (node: ts.Node): void => {
    if (
      (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) &&
      isButtonTag(jsxTagLabel(node.tagName))
    ) {
      const hasTitle = hasOwnTitle(node.attributes)
      const underTrigger = hasTooltipTriggerAncestor(node)
      if (hasTitle && underTrigger) {
        // Rule 4: the tooltip channels never combine — the Radix popup and
        // the native title would both fire on hover.
        report(node)
      } else if (
        !hasTitle &&
        !underTrigger &&
        !hasMenuitemRole(node.attributes) &&
        // Static-label channel: only a PAIRED element's own JsxElement is
        // examined (its direct text children). A self-closing button has no
        // children — and its parent is the CONTAINER, whose other children's
        // text must never count as this button's label.
        !(ts.isJsxOpeningElement(node) && hasStaticVisibleText(node.parent as ts.JsxElement, sf))
      ) {
        report(node)
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(sf)
  return violations
}

describe('button title invariant (title=, a TooltipTrigger ancestor, or a static visible label)', () => {
  const sources = collectSources(SRC_DIR)

  it('scans a non-trivial number of source files', () => {
    // Sanity: the walk must actually reach the component tree, otherwise the
    // assertion below would pass vacuously.
    expect(sources.length).toBeGreaterThan(50)
  })

  it('gives every <button>/<Button> a title= attribute or a TooltipTrigger ancestor', () => {
    const offenders: string[] = []
    for (const file of sources) {
      for (const v of scanButtonTitles(readFileSync(file, 'utf8'), file)) {
        offenders.push(`${relative(SRC_DIR, v.file)}:${v.line}: ${v.snippet}`)
      }
    }
    expect(offenders).toEqual([])
  })

  it('flags a button without title', () => {
    for (const src of [
      '<Button variant="ghost"><XIcon /></Button>', // icon-only
      '<button\n  className="m-1"\n/>', // self-closing: no children to label it
      '<button onClick={f}>{label}</button>', // dynamic expression: not provable
      '<button>{cond ? a : "Save"}</button>', // a non-literal ternary branch
      '<button>{items.map((i) => i.name)}</button>', // mapped children
      '<button><span className="hidden">Go</span></button>', // only an element child
      'export const A = () => <Tooltip><button><X /></button></Tooltip>', // Tooltip ≠ TooltipTrigger
    ]) {
      expect(scanButtonTitles(src)).toHaveLength(1)
    }
  })

  it('does not flag a button with role="menuitem" even without title', () => {
    for (const src of [
      '<button role="menuitem" onClick={f}>Open</button>',
      '<button\n  role="menuitem"\n  disabled={busy}\n  className={cn("a", "b")}\n>\n  <Icon />\n  Open\n</button>',
      '<Button role="menuitem">x</Button>', // exemption keys off the role, not the tag
    ]) {
      expect(scanButtonTitles(src)).toEqual([])
    }
  })

  it('only the exact role="menuitem" literal is exempt', () => {
    for (const src of [
      '<button role="menu"><X /></button>', // container role, not a menu entry
      '<button role={menuItemRole}><X /></button>', // non-literal role is not provably exempt
      '<button data-role="menuitem"><X /></button>', // not the role attribute
    ]) {
      expect(scanButtonTitles(src)).toHaveLength(1)
    }
  })

  it('accepts the documented compliant shapes', () => {
    for (const src of [
      '<button title="Do it">ok</button>',
      '<Button title={dynamicTitle}>ok</Button>',
      '<button {...rest}>ok</button>', // spread is trusted to carry title
      '<button role="menuitem" title="still fine">x</button>', // title on menuitem is harmless
      '<TooltipTrigger asChild><button>x</button></TooltipTrigger>',
      '<TooltipTrigger>{cond && <Button>x</Button>}</TooltipTrigger>',
      '<IconButton label="x" />', // out of scope: not button/Button
      // Static-label channel — the label IS the explanation:
      '<button onClick={f}>Cancel</button>',
      '<button>{saving ? "Saving…" : "Save"}</button>', // literal ternary
      '<button>{busy ? "Deleting…" : "Delete"}</button>',
      '<button>\n  <Icon />\n  Retry\n</button>', // icon + static text
      '<button>{("Save")}</button>', // parenthesized literal
    ]) {
      expect(scanButtonTitles(src)).toEqual([])
    }
  })

  it('flags a title on a button under a TooltipTrigger (the channels never combine)', () => {
    for (const src of [
      '<TooltipTrigger asChild><button title="x" /></TooltipTrigger>',
      '<TooltipTrigger><button title="both">x</button></TooltipTrigger>', // was compliant before rule 4
      '<TooltipTrigger asChild><Button title={label} disabled={disabled} onClick={onClick}><Icon /></Button></TooltipTrigger>',
    ]) {
      expect(scanButtonTitles(src)).toHaveLength(1)
    }
  })

  it('static-label acceptance never leaks to a sibling element', () => {
    // The label text sits inside a SIBLING <button>, not the scanned one —
    // the second (self-closing) button must still flag.
    const src = '<button>Save</button><button onClick={f} />'
    expect(scanButtonTitles(src)).toHaveLength(1)
  })

  it('does not flag <button mentioned in comments', () => {
    for (const src of [
      '// <button> in prose\nconst x = 1\n',
      '/* <Button> also in prose */\nconst y = 2\n',
      '// TODO: add <button title="…"> here\nconst z = 3\n',
    ]) {
      expect(scanButtonTitles(src)).toEqual([])
    }
  })

  it('does not flag <button inside strings or JSX text', () => {
    for (const src of [
      'const html = "<button>not a tag</button>"\n',
      'const el = <p>press the {"<button>"} please</p>\n',
      'const tpl = `<button loading>`\n',
    ]) {
      expect(scanButtonTitles(src)).toEqual([])
    }
  })

  it('parses a .ts file as TS so a generic arrow cannot invent a JSX tag', () => {
    // In a `.ts` file the bare `<T>(x: T)` is an unparenthesised generic
    // arrow; parsing that file as TSX would read it as a JSX opening tag.
    const src = '// <button> in prose\nconst id = <T>(x: T): T => x\n'
    expect(scanButtonTitles(src, 'sample.ts')).toEqual([])
  })

  it('reports an accurate file, line and snippet for each offender', () => {
    const src = 'const a = 1\nconst b = 2\nexport const C = () => <button><X /></button>\n'
    const violations = scanButtonTitles(src)
    expect(violations).toHaveLength(1)
    const v = violations[0]
    expect(v?.line).toBe(3)
    expect(v?.snippet).toContain('<button>')
    expect(v?.file).toBe('sample.tsx')
  })
})
