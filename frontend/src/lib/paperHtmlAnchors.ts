// Paper HTML anchor navigation (E1, paper.html side).
//
// The DOM mirror of `paperAnchors` (the source-text resolver): the same
// conservative shape-first strategy — classify the anchor, then resolve only
// on a confident structural match — applied to the paper's SANITIZED
// `paper.html` tree instead of its extracted lines.
//
// Resolution order per anchor kind:
//   1. Element ids, following LaTeXML conventions (verified against real
//      ar5iv exports): sections `S3` / subsections `S3.SS2` / subsubsections
//      `S3.SS2.SSS1`, figures `S3.F2` / `fig2`, tables `S5.T1` / `tbl1`,
//      equations `S4.E3` / `eq3`, plus a few generic spellings for
//      non-LaTeXML documents.
//   2. Conservative caption/heading TEXT, with caption-start priority — the
//      caption element that STARTS with the float reference wins over a body
//      paragraph that merely mentions it, exactly like the line resolver.
//   3. Verbatim quotes (≥ MIN_TEXT_NEEDLE) search the document text and
//      resolve to the most specific (shortest) containing element.
//
// Bare numbers and page pointers stay unresolvable (classifyAnchor refuses
// them), so the caller keeps its honest "not found". A hit is returned as an
// ELEMENT DESCRIPTOR — the target's `id` when it carries one (the most robust
// DOM handle) plus an element-child index path (the fallback handle) — never
// a DOM node: this module is pure over strings and hast trees, no DOM access.
//
// The paper.html pipeline's own rules apply up front: an empty or over-cap
// document is unresolvable (the view cannot render it, so the anchor must
// fall through to the extracted source text instead).

import type { Element, Root, RootContent } from 'hast'
import type { PaperAnchor } from '@/api/papers'
import type { AnchorKind } from './paperAnchors'
import {
  classifyAnchor,
  MIN_TEXT_NEEDLE,
  needleCandidates,
  numberedLineRe,
  sectionHeadingMatches,
} from './paperAnchors'
import { PAPER_HTML_MAX_BYTES, paperHtmlByteLength, sanitizePaperHtml } from './paperHtmlSanitize'

/** A resolved paper.html anchor: a stable handle on the target element, never
 *  a live node. `id` is the element's id when it carries one (LaTeXML ids
 *  survive sanitization); `path` is its element-child index path from the
 *  sanitized root, indexing ELEMENT children only (text nodes are skipped on
 *  both the hast and the DOM side, so the path survives the React render). */
export interface HtmlAnchorHit {
  id: string | null
  path: number[]
  kind: AnchorKind
  /** The needle that produced the hit (for display/debugging). */
  needle: string
}

/** Longest element text the float/label text fallbacks will consider: bigger
 *  blocks (sections, whole tables) aggregate too much prose for a
 *  contains-match to be a confident target. */
const FLOAT_TEXT_MAX = 400

/** Longest element text the verbatim-quote startsWith pass will consider. */
const QUOTE_START_MAX = 1200

interface ElementEntry {
  el: Element
  tag: string
  id: string
  /** Lowercased subtree text. */
  text: string
  /** Element-child index path from the sanitized root. */
  path: number[]
}

interface DocumentIndex {
  /** First element per id, in document order. */
  byId: Map<string, ElementEntry>
  /** First element per lowercased id (case-forgiving fallback pass). */
  byIdLower: Map<string, ElementEntry>
  /** Every element, in document order. */
  entries: ElementEntry[]
}

function elementText(el: Element): string {
  let out = ''
  const walk = (nodes: readonly RootContent[]): void => {
    for (const node of nodes) {
      if (node.type === 'text') out += node.value
      else if (node.type === 'element') walk(node.children)
    }
  }
  walk(el.children)
  return out.toLowerCase()
}

function idOf(el: Element): string {
  const value = el.properties['id']
  return typeof value === 'string' ? value : ''
}

function collectElements(root: Root): DocumentIndex {
  const index: DocumentIndex = {
    byId: new Map(),
    byIdLower: new Map(),
    entries: [],
  }
  const walk = (nodes: readonly RootContent[], path: number[]): void => {
    let elementIndex = -1
    for (const node of nodes) {
      if (node.type !== 'element') continue
      elementIndex++
      const childPath = [...path, elementIndex]
      const entry: ElementEntry = {
        el: node,
        tag: node.tagName,
        id: idOf(node),
        text: elementText(node),
        path: childPath,
      }
      index.entries.push(entry)
      if (entry.id !== '' && !index.byId.has(entry.id)) index.byId.set(entry.id, entry)
      const lower = entry.id.toLowerCase()
      if (lower !== '' && !index.byIdLower.has(lower)) index.byIdLower.set(lower, entry)
      walk(node.children, childPath)
    }
  }
  walk(root.children, [])
  return index
}

// --- Id candidates -------------------------------------------------------------

/** Escape for embedding a token in a regex source. */
function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** LaTeXML section ids for a dotted section token (`3` → `S3`; `3.2` →
 *  `S3.SS2`, the ar5iv form, plus the plain-dotted and SS-prefixed
 *  spellings other generators produce). */
function sectionIdCandidates(token: string): string[] {
  const parts = token.split('.')
  const out = [`S${token}`]
  if (parts.length === 2) {
    out.push(`S${parts[0]}.SS${parts[1]}`, `SS${parts[0]}.SSS${parts[1]}`)
  } else if (parts.length === 3) {
    out.push(`S${parts[0]}.SS${parts[1]}.SSS${parts[2]}`, `SS${parts[0]}.SSS${parts[1]}.SSSS${parts[2]}`)
  }
  return out
}

type FloatKind = 'figure' | 'table' | 'equation' | 'algorithm'

interface FloatIdCandidates {
  /** Exact ids to try, in order (case-sensitive first, then forgiving). */
  exact: string[]
  /** LaTeXML section-scoped id pattern (`S3.F2`, `S4.E3`, `S5.T1`) — matched
   *  against the document's ids in document order. */
  scoped?: RegExp
}

function floatIdCandidates(kind: FloatKind, token: string): FloatIdCandidates {
  switch (kind) {
    case 'figure':
      return { exact: [`fig${token}`, `figure${token}`], scoped: new RegExp(`^S\\d+\\.F${escapeRe(token)}$`) }
    case 'table':
      return { exact: [`tbl${token}`, `table${token}`], scoped: new RegExp(`^S\\d+\\.T${escapeRe(token)}$`) }
    case 'equation':
      return {
        exact: [`eq${token}`, `equation${token}`, `E${token}`],
        scoped: new RegExp(`^S\\d+\\.E${escapeRe(token)}$`),
      }
    case 'algorithm':
      return {
        exact: [`alg${token}`, `algorithm${token}`],
        scoped: new RegExp(`^S\\d+\\.Alg${escapeRe(token)}$`, 'i'),
      }
  }
}

// --- Text fallback helpers -------------------------------------------------------

/** Whether a needle is the start of the text and not a mere prefix of a
 *  longer token (`figure 2` must not match `figure 20:`). Mirrors
 *  paperAnchors' startsWithNeedle boundary. */
function startsWithBoundary(text: string, needleLower: string): boolean {
  return text.startsWith(needleLower) && !/^(?:\w|\.\d)/.test(text.slice(needleLower.length))
}

function isHeading(tag: string): boolean {
  return /^h[1-6]$/.test(tag)
}

function isCaption(tag: string): boolean {
  return tag === 'figcaption' || tag === 'caption'
}

function toHit(entry: ElementEntry, kind: AnchorKind, needle: string): HtmlAnchorHit {
  return { id: entry.id !== '' ? entry.id : null, path: entry.path, kind, needle }
}

function resolveByIds(index: DocumentIndex, ids: readonly string[]): ElementEntry | null {
  for (const id of ids) {
    const exact = index.byId.get(id)
    if (exact !== undefined) return exact
  }
  for (const id of ids) {
    const lower = index.byIdLower.get(id.toLowerCase())
    if (lower !== undefined) return lower
  }
  return null
}

function resolveByScopedId(index: DocumentIndex, pattern: RegExp): ElementEntry | null {
  for (const id of index.byId.keys()) {
    if (pattern.test(id)) return index.byId.get(id) ?? null
  }
  return null
}

/** Float text fallback, caption-start priority: the caption element that
 *  starts with the reference, then the float wrapper itself, then a short
 *  block that starts with it, then a short block that merely mentions it. */
function resolveFloatByText(
  index: DocumentIndex,
  kind: FloatKind,
  token: string,
  needle: string,
): HtmlAnchorHit | null {
  const anchored = numberedLineRe(kind, token, true)
  const anywhere = numberedLineRe(kind, token)
  const startsWith = (entry: ElementEntry): boolean => anchored.test(entry.text.trim())
  const mentions = (entry: ElementEntry): boolean => anywhere.test(entry.text)

  // Caption priority: figcaption/caption elements are THE caption targets.
  const caption = index.entries.find((e) => isCaption(e.tag) && startsWith(e))
  if (caption !== undefined) return toHit(caption, kind, needle)
  // Then the float wrapper (`figure`, `table`) whose own text starts with the
  // caption — the LaTeXML shape puts the caption inside the wrapper.
  const wrapper = index.entries.find(
    (e) => (e.tag === 'figure' || e.tag === 'table') && startsWith(e),
  )
  if (wrapper !== undefined) return toHit(wrapper, kind, needle)
  // Then a short block that starts with the reference, then one that mentions
  // it (a body paragraph is the honest last resort, as in the line resolver).
  const startBlock = index.entries.find((e) => e.text.length <= FLOAT_TEXT_MAX && startsWith(e))
  if (startBlock !== undefined) return toHit(startBlock, kind, needle)
  const mention = index.entries.find((e) => e.text.length <= FLOAT_TEXT_MAX && mentions(e))
  if (mention !== undefined) return toHit(mention, kind, needle)
  return null
}

/** Section text fallback: a heading whose text is the section's heading
 *  (`3 Method`, `3.2 Attention`) — the DOM mirror of the heading-line logic. */
function resolveSectionByText(
  index: DocumentIndex,
  token: string,
  needle: string,
): HtmlAnchorHit | null {
  const heading = index.entries.find((e) => isHeading(e.tag) && sectionHeadingMatches(e.text, token))
  return heading === undefined ? null : toHit(heading, 'section', needle)
}

/** Verbatim-quote / named-pointer resolution. `labelFallback` restricts to
 *  STRUCTURAL targets (a heading or a short block that starts with the needle
 *  and separates it), never a bare contains-match — mirroring
 *  paperAnchors' structuralStartMatch. */
function resolveText(
  index: DocumentIndex,
  token: string,
  needle: string,
  labelFallback: boolean,
): HtmlAnchorHit | null {
  if (token.length < MIN_TEXT_NEEDLE) return null
  const n = token.toLowerCase()

  if (labelFallback) {
    const structural = index.entries.find((e) => {
      if (!startsWithBoundary(e.text, n)) return false
      if (isHeading(e.tag) || isCaption(e.tag)) return true
      if (e.text.length > FLOAT_TEXT_MAX) return false
      const rest = e.text.slice(n.length)
      return rest === '' || /^[:.\u2014-]/.test(rest)
    })
    return structural === undefined ? null : toHit(structural, 'text', needle)
  }

  // Starts-with pass: prefer the deepest matching element (the caption inside
  // the figure, the paragraph inside the blockquote) — the most specific
  // scroll target. Falls back to nested ties by document order.
  let best: ElementEntry | null = null
  for (const entry of index.entries) {
    if (entry.text.length > QUOTE_START_MAX) continue
    if (!startsWithBoundary(entry.text, n)) continue
    if (best === null || entry.path.length > best.path.length) best = entry
  }
  if (best !== null) return toHit(best, 'text', needle)

  // Contains pass: the SHORTEST containing element is the most specific (a
  // paragraph beats its section); document order breaks ties.
  let smallest: ElementEntry | null = null
  for (const entry of index.entries) {
    if (!entry.text.includes(n)) continue
    if (smallest === null || entry.text.length < smallest.text.length) smallest = entry
  }
  return smallest === null ? null : toHit(smallest, 'text', needle)
}

function resolveNeedle(index: DocumentIndex, needle: string, labelFallback: boolean): HtmlAnchorHit | null {
  const classified = classifyAnchor(needle)
  if (classified === null) return null
  const { kind, token } = classified

  if (kind === 'section') {
    const byId = resolveByIds(index, sectionIdCandidates(token))
    if (byId !== null) return toHit(byId, kind, needle)
    return resolveSectionByText(index, token, needle)
  }

  if (kind === 'figure' || kind === 'table' || kind === 'equation' || kind === 'algorithm') {
    const candidates = floatIdCandidates(kind, token)
    const byId = resolveByIds(index, candidates.exact)
    if (byId !== null) return toHit(byId, kind, needle)
    if (candidates.scoped !== undefined) {
      const scoped = resolveByScopedId(index, candidates.scoped)
      if (scoped !== null) return toHit(scoped, kind, needle)
    }
    return resolveFloatByText(index, kind, token, needle)
  }

  return resolveText(index, token, needle, labelFallback)
}

/**
 * Resolve an anchor against the paper's rendered `paper.html` document. The
 * raw HTML is run through the SAME sanitized pipeline the renderer uses
 * (ids survive; hostile markup is already neutralized), then the anchor's
 * `ref` (verbatim needle first, then its stripped/segmented candidates) and,
 * as a structural-only fallback, its `label` are matched — mirroring
 * `resolveAnchor`'s source-text strategy. Returns an element descriptor, or
 * null when the anchor cannot be located confidently (the caller then tries
 * the extracted source text before admitting "not found").
 */
export function resolveHtmlAnchor(html: string, anchor: PaperAnchor): HtmlAnchorHit | null {
  if (html.trim() === '') return null
  // Over-cap documents are not renderable (the view refuses them), so
  // resolving into them would be a silent dead end — refuse and let the
  // extracted source text answer instead.
  if (paperHtmlByteLength(html) > PAPER_HTML_MAX_BYTES) return null
  const index = collectElements(sanitizePaperHtml(html))
  if (index.entries.length === 0) return null

  const ref = (anchor.ref ?? '').trim()
  if (ref !== '') {
    for (const candidate of needleCandidates(ref)) {
      const hit = resolveNeedle(index, candidate, false)
      if (hit !== null) return hit
    }
  }
  const label = (anchor.label ?? '').trim()
  if (label !== '') {
    for (const candidate of needleCandidates(label)) {
      const hit = resolveNeedle(index, candidate, true)
      if (hit !== null) return hit
    }
  }
  return null
}
