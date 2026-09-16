// Paper source-anchor navigation (E1).
//
// A paper's identity card (paper.md front matter) carries `anchors` — labelled
// pointers into the source document (`{label: sec3, ref: "§3"}`). Clicking an
// anchor in the reader must find the matching heading/caption inside the
// paper's extracted source text (source.md) and scroll to it.
//
// The resolution is deliberately CONSERVATIVE. It first recognises the anchor's
// SHAPE (section, figure, table, equation, or a verbatim quote) and only then
// reports a hit on a confident structural match. Anything it cannot classify
// confidently — a bare number, a page pointer, a structural keyword without a
// number, or a too-short quote — resolves to `null`, so the UI degrades to an
// honest "not found" instead of jumping to a wrong line.
//
// Pure over strings: no React, no DOM, no I/O.

import type { PaperAnchor } from '@/api/papers'

export type AnchorKind = 'section' | 'figure' | 'table' | 'equation' | 'text'

export interface AnchorHit {
  /** 0-based line index into the source document. */
  line: number
  kind: AnchorKind
  /** The needle that produced the hit (for display/debugging). */
  needle: string
}

/** Shortest verbatim-quote needle we will search for; anything shorter is too
 *  generic to be a confident match and is refused (no false jumps). */
export const MIN_TEXT_NEEDLE = 4

interface Classified {
  kind: AnchorKind
  token: string
}

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/**
 * Classify an anchor reference. Returns null when the reference is too
 * ambiguous to resolve confidently (a bare number, a page pointer, or a
 * structural keyword without a number).
 */
export function classifyAnchor(ref: string): Classified | null {
  const s = ref.trim()
  if (s === '') return null
  // A bare number is ambiguous (page? figure? section?) — refuse rather than guess.
  if (/^\d+(?:\.\d+)*$/.test(s)) return null
  // A page pointer cannot be resolved against the extracted text.
  if (/^p(?:age|g)?\.?\s*\d+/i.test(s)) return null

  let m = /^(?:§+\s*|sec(?:tion)?\.?\s*)(\d+(?:\.\d+)*)/i.exec(s)
  if (m) return { kind: 'section', token: m[1]! }
  m = /^fig(?:ure)?\.?\s*(\d+)/i.exec(s)
  if (m) return { kind: 'figure', token: m[1]! }
  m = /^tab(?:le)?\.?\s*(\d+)/i.exec(s)
  if (m) return { kind: 'table', token: m[1]! }
  m = /^eq(?:uation)?\.?\s*\(?(\d+)\)?/i.exec(s)
  if (m) return { kind: 'equation', token: m[1]! }

  // A structural keyword on its own is too weak to anchor on.
  if (/^(?:fig(?:ure)?|tab(?:le)?|eq(?:uation)?|sec(?:tion)?|§)\b/i.test(s)) return null

  return { kind: 'text', token: s }
}

/** The heading text of an ATX heading line, or null when the line is not one. */
function headingText(line: string): string | null {
  const m = /^\s{0,3}(#{1,6})\s*(.*)$/.exec(line)
  if (!m) return null
  const text = m[2]!.replace(/[*_`]/g, '').trim()
  return text === '' ? null : text
}

/** Whether a heading ("3. Method", "3 Method") is the heading of section `n`. */
export function sectionHeadingMatches(text: string, token: string): boolean {
  const re = new RegExp(`^${escapeRe(token)}(?:\\.(?!\\d)|[\\s:)]|$)`)
  return re.test(text)
}

function numberedLineRe(kind: 'figure' | 'table' | 'equation', token: string): RegExp {
  const word = kind === 'figure' ? 'fig(?:ure)?' : kind === 'table' ? 'tab(?:le)?' : 'eq(?:uation)?'
  return new RegExp(`\\b${word}\\.?\\s*\\(?${escapeRe(token)}\\)?\\b`, 'i')
}

function resolveNeedle(lines: string[], needle: string): AnchorHit | null {
  const classified = classifyAnchor(needle)
  if (!classified) return null
  const { kind, token } = classified

  if (kind === 'section') {
    for (let i = 0; i < lines.length; i++) {
      const h = headingText(lines[i]!)
      if (h !== null && sectionHeadingMatches(h, token)) return { line: i, kind, needle }
    }
    return null
  }

  if (kind === 'figure' || kind === 'table' || kind === 'equation') {
    const re = numberedLineRe(kind, token)
    for (let i = 0; i < lines.length; i++) {
      if (re.test(lines[i]!)) return { line: i, kind, needle }
    }
    return null
  }

  // Verbatim quote: require a long-enough needle, then match the first line
  // containing it.
  if (token.length < MIN_TEXT_NEEDLE) return null
  const n = token.toLowerCase()
  for (let i = 0; i < lines.length; i++) {
    if (lines[i]!.toLowerCase().includes(n)) return { line: i, kind: 'text', needle }
  }
  return null
}

/**
 * Resolve an anchor against the paper's extracted source text. Returns the
 * 0-based line to scroll to, or null when the anchor cannot be located
 * confidently (the caller shows an honest "not found"). Both the anchor's
 * `ref` and its `label` are tried, in that order; the free-form `note` is NOT
 * used as a locator (it is context, not a pointer).
 */
export function resolveAnchor(source: string, anchor: PaperAnchor): AnchorHit | null {
  if (source === '') return null
  const lines = source.split('\n')
  const needles = [anchor.ref, anchor.label]
    .map((s) => (s ?? '').trim())
    .filter((s) => s !== '')
  for (const needle of needles) {
    const hit = resolveNeedle(lines, needle)
    if (hit) return hit
  }
  return null
}
