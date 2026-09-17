// Sanitizing parser for a paper's `paper.html` — the static HTML export the
// study-paper skill writes next to the markdown artifacts (LaTeXML-shaped:
// sections with ids, MathML equations carrying `alttext`/`display`, figures
// with locally bundled images, tables).
//
// Threat model: paper.html is untrusted, file-shaped input rendered inside the
// app webview. The parse → sanitize pipeline must guarantee that NO script,
// iframe, style, or event-handler attribute survives, and that `href`/`src`
// values are constrained to an explicit protocol allowlist — everything else
// (the LaTeXML `ltx-*` class hooks, remote stylesheets, forms) is dropped
// before the tree ever reaches React.
//
// Pipeline: unified + rehype-parse (full-document mode) → extract `<body>`
// children → hast-util-sanitize with an extended schema → bounded LRU cache.
// The component layer (PaperHtmlView) applies all typography via Tailwind
// classes over design tokens — no class attribute from the document survives,
// so the document's own (absent/remote) CSS is never needed and never loaded.

import { unified } from 'unified'
import rehypeParse from 'rehype-parse'
import { defaultSchema, sanitize, type Schema } from 'hast-util-sanitize'
import type { Element, Root, RootContent } from 'hast'

/** Hard cap on rendered paper HTML size (bytes). Documents over this cap get
 *  an explicit message instead of a render — a pathological paper.html must
 *  not freeze the UI in a multi-second parse + sanitize + React render. */
export const PAPER_HTML_MAX_BYTES = 8 * 1024 * 1024

/** Maximum number of sanitized trees retained (LRU). Sanitized paper trees are
 *  large; the papers tab shows one paper at a time, so the cache only needs to
 *  absorb tab/section switches within a paper and back-navigation. */
export const PAPER_HTML_TREE_CACHE_MAX = 8

// --- Schema ------------------------------------------------------------------
//
// Built on hast-util-sanitize's GitHub-style defaultSchema with these paper-
// specific extensions:
//
// * MathML: the full presentation-markup tag set (LaTeXML emits `math` with
//   `alttext`/`display`, `mrow`/`mfrac`/`msub`/… trees) plus the presentation
//   attributes those tags carry. SVG is deliberately NOT allowed in v1 —
//   LaTeXML does not emit it and an SVG surface is a much larger attack space.
// * Structural HTML that the default (GitHub-flavored markdown) schema lacks:
//   figure/figcaption, caption/colgroup/col, abbr/cite/dfn/mark/time/wbr and
//   the sectioning elements — LaTeXML uses them heavily.
// * `id` is allowed on every element and is NOT clobber-prefixed: internal
//   `#fragment` anchors in the document must resolve against the rendered ids.
//   DOM-clobbering exposure is limited to `name`/ARIA references, which keep
//   the default `user-content-` prefix.
// * `class` attributes are dropped everywhere. The document's own CSS is never
//   loaded, so surviving class names would be inert noise at best; typography
//   is reapplied by the component layer from the semantic structure alone.
// * `img[src]` additionally allows `data:` so self-contained exports with
//   inline base64 images keep working (http/https stay allowed; relative srcs
//   are resolved to data URLs at render time by the img component).
// * `strip` (removed WITH their children, as opposed to merely unwrapped):
//   script/style/iframe and the other active/embedding tags whose leftover
//   fallback content would be noise at best — `<style>` bodies must never leak
//   as visible text.

const MATHML_TAG_NAMES = [
  'annotation',
  'annotation-xml',
  'maction',
  'maligngroup',
  'malignmark',
  'math',
  'menclose',
  'merror',
  'mfenced',
  'mfrac',
  'mglyph',
  'mi',
  'mlabeledtr',
  'mlongdiv',
  'mmultiscripts',
  'mn',
  'mo',
  'mover',
  'mpadded',
  'mphantom',
  'mroot',
  'mrow',
  'ms',
  'mscarries',
  'mscarry',
  'msgroup',
  'msline',
  'mspace',
  'msqrt',
  'msrow',
  'mstack',
  'mstyle',
  'msub',
  'msubsup',
  'msup',
  'mtable',
  'mtd',
  'mtext',
  'mtr',
  'munder',
  'munderover',
  'semantics',
] as const

/** Presentation attributes MathML elements may carry. Attribute names are
 *  hast property names (parse5 + property-information maps `rowspan` →
 *  `rowSpan`; the MathML-only names are not HTML properties and stay
 *  literal). */
const MATHML_ATTRIBUTES = [
  'accent',
  'accentunder',
  'alttext',
  'close',
  'columnalign',
  'columnlines',
  'columnspan',
  'depth',
  'display',
  'displaystyle',
  'encoding',
  'equalcolumns',
  'equalrows',
  'fence',
  'frame',
  'framespacing',
  'height',
  'largeop',
  'linethickness',
  'lspace',
  'mathbackground',
  'mathcolor',
  'mathsize',
  'mathvariant',
  'maxsize',
  'minsize',
  'mode',
  'movablelimits',
  'notation',
  'open',
  'overflow',
  'rspace',
  'rowalign',
  'rowlines',
  'rowSpan',
  'scriptlevel',
  'separators',
  'shift',
  'side',
  'stretchy',
  'symmetric',
  'voffset',
  'width',
] as const

const MATHML_ATTRIBUTES_LIST: readonly string[] = MATHML_ATTRIBUTES

function mathmlAttributes(): Record<string, readonly string[]> {
  const out: Record<string, readonly string[]> = {}
  for (const tag of MATHML_TAG_NAMES) out[tag] = MATHML_ATTRIBUTES_LIST
  return out
}

/** The paper-HTML sanitize schema (exported for tests). */
export const paperHtmlSanitizeSchema: Schema = {
  ...defaultSchema,
  ancestors: defaultSchema.ancestors,
  attributes: {
    ...defaultSchema.attributes,
    ...mathmlAttributes(),
    a: ['href'],
    img: ['src'],
  },
  clobber: ['ariaDescribedBy', 'ariaLabelledBy', 'name'],
  protocols: {
    ...defaultSchema.protocols,
    src: ['http', 'https', 'data'],
  },
  strip: [
    'script',
    'style',
    'iframe',
    'frame',
    'frameset',
    'object',
    'embed',
    'applet',
    'noscript',
    'canvas',
    'form',
    'button',
    'input',
    'select',
    'option',
    'textarea',
    'label',
    'fieldset',
    'legend',
    'dialog',
    'template',
    'link',
    'meta',
    'base',
    'title',
    'audio',
    'video',
    'track',
    'source',
    'picture',
  ],
  tagNames: [
    ...(defaultSchema.tagNames ?? []),
    'abbr',
    'article',
    'aside',
    'caption',
    'cite',
    'col',
    'colgroup',
    'dfn',
    'figcaption',
    'figure',
    'footer',
    'header',
    'main',
    'mark',
    'nav',
    'section',
    'small',
    'time',
    'u',
    'wbr',
    ...MATHML_TAG_NAMES,
  ],
}

// --- Parse + sanitize --------------------------------------------------------

const parser = unified().use(rehypeParse, { fragment: false })

/** Depth-first search for the `<body>` element — a full-document parse nests
 *  it under `<html>`, while fragment-ish input may omit both wrappers. */
function findBody(nodes: readonly RootContent[]): Element | undefined {
  for (const node of nodes) {
    if (node.type !== 'element') continue
    if (node.tagName === 'body') return node
    const nested = findBody(node.children)
    if (nested !== undefined) return nested
  }
  return undefined
}

/** Extract the `<body>` element's children from a parsed full document. A
 *  body-less input (a bare fragment) passes its root children through — the
 *  sanitize step is the security boundary either way, this only keeps `<head>`
 *  residue (`<title>` text, `<meta>`, `<link>`) out of the render. */
function extractBody(root: Root): Root {
  const body = findBody(root.children)
  return { type: 'root', children: body === undefined ? root.children : body.children }
}

const cache = new Map<string, Root>()
let hits = 0
let misses = 0

/**
 * Parse + sanitize a paper.html document and return the sanitized hast root
 * (body content only). The result is cached by the raw HTML string: switching
 * paper-workspace sections re-renders the same document without re-running
 * the pipeline.
 *
 * Throws nothing for hostile input — that is the point — but assumes the
 * caller has already enforced the size cap (see {@link PAPER_HTML_MAX_BYTES}).
 */
export function sanitizePaperHtml(html: string): Root {
  const cached = cache.get(html)
  if (cached !== undefined) {
    hits++
    // Refresh recency so the LRU evicts the least-recently-used tree.
    cache.delete(html)
    cache.set(html, cached)
    return cached
  }
  misses++
  const parsed = parser.parse(html) as Root
  // hast-util-sanitize types its input/output as the broad `Nodes` union; for
  // a rehype tree the result is always the sanitized `Root`.
  const sanitized = sanitize(extractBody(parsed), paperHtmlSanitizeSchema) as Root
  cache.set(html, sanitized)
  if (cache.size > PAPER_HTML_TREE_CACHE_MAX) {
    const oldest = cache.keys().next().value
    if (oldest !== undefined) cache.delete(oldest)
  }
  return sanitized
}

/** Sanitized-tree cache counters and current size (test observability). */
export function paperHtmlCacheStats(): { hits: number; misses: number; size: number } {
  return { hits, misses, size: cache.size }
}

/** Clear the sanitized-tree cache and its counters. */
export function resetPaperHtmlCache(): void {
  cache.clear()
  hits = 0
  misses = 0
}

const encoder = new TextEncoder()

/** UTF-8 byte length of a string (NOT `.length`, which counts UTF-16 units). */
export function paperHtmlByteLength(html: string): number {
  return encoder.encode(html).length
}
