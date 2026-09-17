// Paper source-anchor navigation (E1).
//
// A paper's identity card (paper.md front matter) carries `anchors` — labelled
// pointers into the source document (`{label: sec3, ref: "§3"}`). Clicking an
// anchor in the reader must find the matching heading/caption inside the
// paper's extracted source text (source.md) and scroll to it.
//
// The resolution is deliberately CONSERVATIVE. It first recognises the anchor's
// SHAPE (section, figure, table, equation, algorithm, or a verbatim quote) and
// only then reports a hit on a confident structural match. Anything it cannot
// classify confidently — a bare number, a page pointer, a structural keyword
// without a number, or a too-short quote — resolves to `null`, so the UI
// degrades to an honest "not found" instead of jumping to a wrong line.
//
// Real PDF-extracted sources add wrinkles the matcher must survive without
// giving up that conservatism:
//   * headings lose their ATX marks and survive as plain numbered lines
//     (`3 ATTACK SURFACE…`, `5.2 Limitations`);
//   * references carry parenthetical annotations (`§4.1 (synthetic apps…)`)
//     and may join several pointers with `/` or `+` (`Prompt 7 / Output 1 (…)`)
//     — each segment is tried, in order, after the verbatim needle;
//   * the target of a named-pointer reference is the CAPTION line that starts
//     with it (`Prompt 7: …`), never the earlier body line that merely mentions
//     it (`…as demonstrated in Prompt 7 and Output 1.`);
//   * the `label` fallback may only hit STRUCTURAL targets (a heading or
//     caption that starts with the label) — a free-form slug must never
//     free-text-match bare body prose.
//
// Pure over strings: no React, no DOM, no I/O.

import type { PaperAnchor } from '@/api/papers'

export type AnchorKind = 'section' | 'figure' | 'table' | 'equation' | 'algorithm' | 'text'

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
  // Figures/tables carry an optional sub-label suffix (`Fig. 2a`, `Table 3b`),
  // which is part of the reference: `2a` targets a DIFFERENT float than `2`.
  m = /^fig(?:ure)?\.?\s*(\d+[a-z]?)/i.exec(s)
  if (m) return { kind: 'figure', token: m[1]! }
  m = /^tab(?:le)?\.?\s*(\d+[a-z]?)/i.exec(s)
  if (m) return { kind: 'table', token: m[1]! }
  m = /^eq(?:uation)?\.?\s*\(?(\d+)\)?/i.exec(s)
  if (m) return { kind: 'equation', token: m[1]! }
  m = /^alg(?:orithm)?\.?\s*(\d+)/i.exec(s)
  if (m) return { kind: 'algorithm', token: m[1]! }

  // A structural keyword on its own is too weak to anchor on.
  if (/^(?:fig(?:ure)?|tab(?:le)?|eq(?:uation)?|sec(?:tion)?|alg(?:orithm)?|§)\b/i.test(s)) return null

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

/**
 * A plain (non-ATX) numbered heading line, as PDF-extracted sources produce
 * them (`3 ATTACK SURFACE OF LLM-INTEGRATED`, `5.2 Limitations`). Conservative
 * by construction: the line must start with the section number followed by a
 * letter-initial title — so wrapped body lines (`3 , and the LLM…`), bare
 * number lines (`4.1.1`) and hyphenated line wraps (`…not aim-`) are refused.
 */
function isPlainSectionHeading(line: string, token: string): boolean {
  const t = line.trim()
  if (t === '') return false
  // Hyphen/comma/semicolon-ending lines are mid-sentence wraps, not headings.
  if (/[-,;]$/.test(t)) return false
  if (!sectionHeadingMatches(t, token)) return false
  // A heading carries a title after the number (optionally via `.`/`:`/`)`);
  // a line whose remainder starts with anything else is prose that merely
  // begins with the number.
  return new RegExp(`^${escapeRe(token)}[\\s.:)]*[A-Za-z]`).test(t)
}

/** Trailing boundary for a float reference: forbid a following word character
 *  or a sub-number, so a digits-only token (`Fig. 2`) never matches `Figure 2a`
 *  or `Figure 2.1`, and a sub-labelled token (`Fig. 2a`) never matches `2ab`. */
const NUMBER_END = '(?![\\w]|\\.\\d)'

export function numberedLineRe(
  kind: 'figure' | 'table' | 'equation' | 'algorithm',
  token: string,
  anchored = false,
): RegExp {
  const word =
    kind === 'figure'
      ? 'fig(?:ure)?'
      : kind === 'table'
        ? 'tab(?:le)?'
        : kind === 'algorithm'
          ? 'alg(?:orithm)?'
          : 'eq(?:uation)?'
  const core = `${word}\\.?\\s*\\(?${escapeRe(token)}\\)?${NUMBER_END}`
  // Anchored = caption priority: the text STARTS with the float reference,
  // i.e. it is the caption itself rather than a body mention of the float.
  // Shared by the source-text resolver (over lines) and the paper.html
  // resolver (over element text) so both stay in lockstep.
  return new RegExp(anchored ? `^\\s*${core}` : `\\b${core}`, 'i')
}

/** Whether the line starts with the (lower-cased) needle and the needle is not
 *  a mere prefix of a longer token — `Output 3` must not match `Output 30:` or
 *  `Output 3.1:`. Mirrors the NUMBER_END boundary used for float references. */
function startsWithNeedle(line: string, needleLower: string): boolean {
  const t = line.trim().toLowerCase()
  return t.startsWith(needleLower) && !/^(?:\w|\.\d)/.test(t.slice(needleLower.length))
}

/** Whether the line is a STRUCTURAL target starting with the needle: an ATX
 *  heading whose text starts with the needle, or a caption-like line whose
 *  remainder after the needle begins with a separator (`:`/`.`/dash) or ends
 *  the line. Used ONLY for the label fallback — a free-form slug that merely
 *  continues into body prose (`persistence across sessions…`) is not a
 *  confident jump target. */
function structuralStartMatch(line: string, needleLower: string): boolean {
  const h = headingText(line)
  if (h !== null) {
    const t = h.toLowerCase()
    return t.startsWith(needleLower) && !/^\w/.test(t.slice(needleLower.length))
  }
  if (!startsWithNeedle(line, needleLower)) return false
  const rest = line.trim().toLowerCase().slice(needleLower.length)
  return rest === '' || /^[:.\u2014-]/.test(rest)
}

/**
 * Strip parenthetical/bracketed annotations from a reference
 * (`§4.1 (synthetic apps…)` → `§4.1`). Annotations inside the parentheses of
 * the reference syntax itself (`Equation (5)`) are recovered by the verbatim
 * needle, which is always tried before the stripped one.
 */
function stripAnnotations(s: string): string {
  return s
    .replace(/\([^()]*\)/g, ' ')
    .replace(/\[[^\][]*\]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
}

/**
 * Expand a raw needle into the ordered list of candidates to try: the verbatim
 * needle, the needle with annotations stripped, and — for compound references
 * — each `/`- or `+`-joined segment, in order. Only whitespace-delimited
 * separators split, so URLs (`github.com/greshake/llm-security`) stay intact.
 */
export function needleCandidates(needle: string): string[] {
  const out: string[] = []
  const push = (s: string): void => {
    const t = s.trim()
    if (t !== '' && !out.includes(t)) out.push(t)
  }
  push(needle)
  const stripped = stripAnnotations(needle)
  push(stripped)
  if (/\s[/+]\s/.test(stripped)) {
    for (const seg of stripped.split(/\s[/+]\s/)) push(stripAnnotations(seg))
  }
  return out
}

function resolveNeedle(lines: string[], needle: string, labelFallback: boolean): AnchorHit | null {
  const classified = classifyAnchor(needle)
  if (!classified) return null
  const { kind, token } = classified

  if (kind === 'section') {
    for (let i = 0; i < lines.length; i++) {
      const h = headingText(lines[i]!)
      if (h !== null && sectionHeadingMatches(h, token)) return { line: i, kind, needle }
    }
    // PDF-extracted sources lose the ATX marks: fall back to plain numbered
    // heading lines (`3 ATTACK SURFACE…`, `5.2 Limitations`).
    for (let i = 0; i < lines.length; i++) {
      if (isPlainSectionHeading(lines[i]!, token)) return { line: i, kind, needle }
    }
    return null
  }

  if (kind === 'figure' || kind === 'table' || kind === 'equation' || kind === 'algorithm') {
    // Caption priority: the line that STARTS with the float reference is the
    // caption itself; a body mention ("as shown in Figure 2") is only a
    // fallback when no caption line exists.
    const startRe = numberedLineRe(kind, token, true)
    for (let i = 0; i < lines.length; i++) {
      if (startRe.test(lines[i]!)) return { line: i, kind, needle }
    }
    const re = numberedLineRe(kind, token)
    for (let i = 0; i < lines.length; i++) {
      if (re.test(lines[i]!)) return { line: i, kind, needle }
    }
    return null
  }

  // Verbatim quote or named caption (`Prompt 7`, `Output 3`, a URL): require a
  // long-enough needle, prefer the line that STARTS with it (its caption), and
  // for the ref also accept a line that merely contains it (a quoted phrase
  // lives mid-line). The label fallback never accepts a bare contains-match.
  if (token.length < MIN_TEXT_NEEDLE) return null
  const n = token.toLowerCase()
  for (let i = 0; i < lines.length; i++) {
    const matched = labelFallback
      ? structuralStartMatch(lines[i]!, n)
      : startsWithNeedle(lines[i]!, n)
    if (matched) return { line: i, kind: 'text', needle }
  }
  if (labelFallback) return null
  for (let i = 0; i < lines.length; i++) {
    if (lines[i]!.toLowerCase().includes(n)) return { line: i, kind: 'text', needle }
  }
  return null
}

/**
 * Resolve an anchor against the paper's extracted source text. Returns the
 * 0-based line to scroll to, or null when the anchor cannot be located
 * confidently (the caller shows an honest "not found"). The anchor's `ref` is
 * the pointer and may hit a caption start or a containing line; the `label` is
 * a fallback that may only hit structural targets (headings/captions), never
 * bare body text. The free-form `note` is NOT used as a locator (it is
 * context, not a pointer).
 */
export function resolveAnchor(source: string, anchor: PaperAnchor): AnchorHit | null {
  if (source === '') return null
  const lines = source.split('\n')
  const ref = (anchor.ref ?? '').trim()
  const label = (anchor.label ?? '').trim()
  if (ref !== '') {
    for (const candidate of needleCandidates(ref)) {
      const hit = resolveNeedle(lines, candidate, false)
      if (hit) return hit
    }
  }
  if (label !== '') {
    for (const candidate of needleCandidates(label)) {
      const hit = resolveNeedle(lines, candidate, true)
      if (hit) return hit
    }
  }
  return null
}
