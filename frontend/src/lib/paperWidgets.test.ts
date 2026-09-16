// Tests for lib/paperWidgets.ts — the critical-layer widget parsers, exercised
// on fixtures shaped like the study-paper skill's note.md / appraisal.md.

import { describe, it, expect } from 'vitest'
import {
  MAX_RED_FLAG_CHIPS,
  normalizeStrength,
  parseCriticalLayer,
  parseEvidenceMatrix,
  parseMarkdownTables,
  parseRedFlags,
  parseUncertainties,
} from './paperWidgets'

// A note.md excerpt with the canonical note-template §5 evidence matrix.
const NOTE_MATRIX = `# Reading Note — Attention

## 5. Contribution → evidence matrix

| Contribution | Claim the evidence is meant to support | Evidence offered (experiment / result / proof) | **Anchor** (section / figure / table / equation / page) | Evidence strength (present / weak / absent) | [Analyst] verdict — does the evidence support the claim? |
| --- | --- | --- | --- | --- | --- |
| C1 | Sparse attention matches dense quality | WMT14 benchmark | §4, Table 2 | present | yes |
| C2 | Trains 2x faster | training curves | Fig 3 | weak | partially |
| C3 | Generalizes to long sequences | — | — | absent | no |

## 7. Critical layer
`

// The appraisal-template §4 "Claim → evidence" shape (no anchor column).
const APPRAISAL_MATRIX = `# Critical Appraisal

## 4. Claim → evidence

| Claim (anchored) | Experiment / result offered | Evidence present / weak / absent | [Analyst] verdict — does it support the claim? |
| --- | --- | --- | --- |
| Self-attention suffices [§3] | WMT14 BLEU 28.4 [Table 2] | present | supports |
| Beats every baseline | a single run | weak | partially |
`

// A note whose critical layer is written as bullets (the template's §7 shape).
const NOTE_LISTS = `## 7. Critical layer (required in every mode)

- **Red flags (≤3, ranked by severity):**
  1. No variance reported — results look cherry-picked.
  2. Baseline never tuned — comparison is unfair.
- **Uncertainty flags** — [Unknown] items:
  - Number of seeds not stated.
  - Compute budget unverifiable.

## 8. TL;DR

Fine.
`

describe('parseMarkdownTables', () => {
  it('extracts header + rows, skipping the separator', () => {
    const tables = parseMarkdownTables(NOTE_MATRIX)
    expect(tables).toHaveLength(1)
    expect(tables[0]!.header[0]).toBe('Contribution')
    expect(tables[0]!.rows).toHaveLength(3)
  })

  it('unescapes \\| inside a cell and folds <br> to a newline', () => {
    const tables = parseMarkdownTables('| a | b |\n| - | - |\n| x\\|y | p<br>q |')
    expect(tables[0]!.rows[0]).toEqual(['x|y', 'p\nq'])
  })

  it('ignores a pipe-run without a separator row', () => {
    expect(parseMarkdownTables('| a | b |\n| c | d |')).toEqual([])
  })
})

describe('normalizeStrength', () => {
  it('maps the three verdicts', () => {
    expect(normalizeStrength('present')).toBe('present')
    expect(normalizeStrength('Weak')).toBe('weak')
    expect(normalizeStrength('ABSENT')).toBe('absent')
  })

  it('folds an ambiguous placeholder and empty to unknown', () => {
    expect(normalizeStrength('present / weak / absent')).toBe('unknown')
    expect(normalizeStrength('')).toBe('unknown')
    expect(normalizeStrength('n/a')).toBe('unknown')
  })
})

describe('parseEvidenceMatrix', () => {
  it('reads the note matrix: claim, evidence, anchor and strength', () => {
    const rows = parseEvidenceMatrix(NOTE_MATRIX)
    expect(rows).toHaveLength(3)
    expect(rows[0]).toEqual({
      claim: 'Sparse attention matches dense quality',
      evidence: 'WMT14 benchmark',
      anchor: '§4, Table 2',
      strength: 'present',
    })
    expect(rows[1]!.strength).toBe('weak')
    expect(rows[2]!.strength).toBe('absent')
  })

  it('reads the appraisal matrix (no anchor column)', () => {
    const rows = parseEvidenceMatrix(APPRAISAL_MATRIX)
    expect(rows).toHaveLength(2)
    expect(rows[0]).toEqual({
      claim: 'Self-attention suffices [§3]',
      evidence: 'WMT14 BLEU 28.4 [Table 2]',
      anchor: '',
      strength: 'present',
    })
  })

  it('returns [] when no table carries an evidence-strength column', () => {
    const md = '| Claim | Evidence |\n| --- | --- |\n| x | y |'
    expect(parseEvidenceMatrix(md)).toEqual([])
  })

  it('skips a template matrix whose data rows are all empty', () => {
    const md = `| Contribution | Claim | Evidence offered | Anchor | Evidence strength (present / weak / absent) |
| - | - | - | - | - |
| | | | | |
| | | | | |`
    expect(parseEvidenceMatrix(md)).toEqual([])
  })
})

describe('parseRedFlags', () => {
  const table = `| Flag | Detail | Severity |
| --- | --- | --- |
| No error bars | single seed | high |
| Weak baseline | old model | medium |
| Tiny dev set | 500 ex | low |
| Extra nit | cosmetic | low |`

  it('reads the red-flags table in full', () => {
    const flags = parseRedFlags(table)
    expect(flags).toHaveLength(4)
    expect(flags[0]).toEqual({ flag: 'No error bars', detail: 'single seed', severity: 'high' })
  })

  it('falls back to the bulleted critical layer when no table exists', () => {
    const flags = parseRedFlags(NOTE_LISTS)
    expect(flags).toHaveLength(2)
    expect(flags[0]).toEqual({
      flag: 'No variance reported',
      detail: 'results look cherry-picked.',
      severity: '',
    })
  })

  it('returns [] when neither shape is present', () => {
    expect(parseRedFlags('# Nothing here')).toEqual([])
  })
})

describe('parseUncertainties', () => {
  it('reads the uncertainty table', () => {
    const md = `| Item | Detail |
| --- | --- |
| Number of seeds | not reported |
| Compute budget | unclear |`
    const items = parseUncertainties(md)
    expect(items).toHaveLength(2)
    expect(items[0]).toEqual({ item: 'Number of seeds', detail: 'not reported' })
  })

  it('falls back to the bulleted uncertainty list', () => {
    const items = parseUncertainties(NOTE_LISTS)
    expect(items.map((i) => i.item)).toEqual([
      'Number of seeds not stated.',
      'Compute budget unverifiable.',
    ])
  })
})

describe('parseCriticalLayer', () => {
  it('merges note + appraisal and caps red flags at three', () => {
    const note = `| Flag | Detail | Severity |
| - | - | - |
| a | x | high |
| b | x | high |
| c | x | med |
| d | x | low |`
    const layer = parseCriticalLayer(note, '')
    expect(layer.redFlags).toHaveLength(MAX_RED_FLAG_CHIPS)
  })

  it('falls back to the appraisal when the note lacks a part', () => {
    const layer = parseCriticalLayer('# empty note', APPRAISAL_MATRIX)
    expect(layer.evidence).toHaveLength(2)
    expect(layer.evidence[0]!.strength).toBe('present')
  })

  it('prefers the note matrix over the appraisal matrix', () => {
    const layer = parseCriticalLayer(NOTE_MATRIX, APPRAISAL_MATRIX)
    expect(layer.evidence).toHaveLength(3)
    expect(layer.evidence[0]!.claim).toBe('Sparse attention matches dense quality')
  })

  it('returns an empty layer for empty inputs', () => {
    expect(parseCriticalLayer('', '')).toEqual({ evidence: [], redFlags: [], uncertainties: [] })
  })
})
