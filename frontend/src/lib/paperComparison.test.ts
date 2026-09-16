// Pure tests for the comparison-matrix parser (no React / DOM).
import { describe, expect, it } from 'vitest'

import {
  parseComparison,
  comparisonMentionsPaper,
  type ComparisonMentionPaper,
} from './paperComparison'

// A comparison artifact as the study-paper skill writes it, filled from
// assets/comparison-matrix.md.
const COMPARISON = `# Paper Comparison Matrix — Sparse vs dense attention

> **Usage.** Use when comparing two or more papers on the same problem.

## 1. Papers under comparison

| ID | Short name | Citation / identifier | One-line summary [Paper] |
| --- | --- | --- | --- |
| P-001 | Vanilla | arxiv:1706.03762 | Standard dense attention [Paper] |
| P-002 | Longformer | arxiv:2004.05150 | Sparse sliding-window attention [Paper] |
| P-003 | BigBird | arxiv:2007.14062 | Block-sparse attention [Paper] |

## 2. Comparison matrix

Rows are dimensions; columns are papers. Keep one anchor per cell.

| Dimension | P-001 | P-002 | P-003 |
| --- | --- | --- | --- |
| Problem framing | dense attention | long sequences | long sequences |
| Core idea / approach | full attention | sliding window | block sparse |
| Evidence strength (present / weak / absent) | present | weak | present |
| [Analyst] red flags | O(n^2) cost | [Unknown] | |

## 3. Fairness & comparability notes

- Are the comparisons apples-to-apples? Same datasets, splits, and metrics?
- The three papers use different sequence lengths, which blocks a head-to-head claim.

## 4. Where the papers agree / disagree

| Point | Agree / disagree | Papers & anchors |
| --- | --- | --- |
| Long context matters | agree | P-001 §1, P-002 §2 |
| Best sparsity pattern | disagree | P-002 §4 vs P-003 §4 |

## 5. Gaps none of the papers address [Unknown]

- No unified benchmark across all three.

## 6. Synthesis verdict

| Field | Value |
| --- | --- |
| Strongest for which purpose — [Analyst], with reason | P-001 for short sequences |
| Confidence in this synthesis (low / medium / high) | medium |
`

describe('parseComparison — the full template artifact', () => {
  const parsed = parseComparison(COMPARISON)

  it('reads the topic from the H1 heading', () => {
    expect(parsed.topic).toBe('Sparse vs dense attention')
  })

  it('reads the papers-under-comparison table', () => {
    expect(parsed.papers).toHaveLength(3)
    expect(parsed.papers[0]).toEqual({
      id: 'P-001',
      shortName: 'Vanilla',
      citation: 'arxiv:1706.03762',
      summary: 'Standard dense attention [Paper]',
    })
    expect(parsed.papers[2]!.id).toBe('P-003')
  })

  it('reads the dimension × paper matrix (columns + rows)', () => {
    expect(parsed.columns).toEqual(['P-001', 'P-002', 'P-003'])
    expect(parsed.hasMatrix).toBe(true)
    expect(parsed.matrix).toHaveLength(4)
    expect(parsed.matrix[0]).toEqual({
      dimension: 'Problem framing',
      cells: ['dense attention', 'long sequences', 'long sequences'],
    })
    // The strength row and a bracketed [Analyst] label survive.
    const strength = parsed.matrix.find((r) => r.dimension.includes('Evidence strength'))
    expect(strength?.cells).toEqual(['present', 'weak', 'present'])
    expect(parsed.matrix[3]!.dimension).toBe('[Analyst] red flags')
    expect(parsed.matrix[3]!.cells[1]).toBe('[Unknown]')
  })

  it('reads fairness bullets, the agreement table, gaps and the verdict', () => {
    expect(parsed.fairness).toHaveLength(2)
    expect(parsed.fairness[1]).toContain('different sequence lengths')

    expect(parsed.agreements).toHaveLength(2)
    expect(parsed.agreements[0]).toEqual({
      point: 'Long context matters',
      verdict: 'agree',
      papers: 'P-001 §1, P-002 §2',
    })
    expect(parsed.agreements[1]!.verdict).toBe('disagree')

    expect(parsed.gaps).toEqual(['No unified benchmark across all three.'])

    expect(parsed.verdict).toHaveLength(2)
    expect(parsed.verdict[1]).toEqual({
      field: 'Confidence in this synthesis (low / medium / high)',
      value: 'medium',
    })
  })
})

describe('parseComparison — degradation', () => {
  it('returns empty collections for empty content', () => {
    const parsed = parseComparison('')
    expect(parsed.topic).toBe('')
    expect(parsed.papers).toEqual([])
    expect(parsed.matrix).toEqual([])
    expect(parsed.columns).toEqual([])
    expect(parsed.fairness).toEqual([])
    expect(parsed.agreements).toEqual([])
    expect(parsed.gaps).toEqual([])
    expect(parsed.verdict).toEqual([])
    expect(parsed.hasMatrix).toBe(false)
  })

  it('keeps the papers table when there is no matrix section', () => {
    const partial = `# Paper Comparison Matrix — partial

## 1. Papers under comparison

| ID | Short name | Citation / identifier | One-line summary [Paper] |
| --- | --- | --- | --- |
| P-001 | A | arxiv:1 | one |
| P-002 | B | arxiv:2 | two |
`
    const parsed = parseComparison(partial)
    expect(parsed.papers).toHaveLength(2)
    expect(parsed.hasMatrix).toBe(false)
    expect(parsed.matrix).toEqual([])
  })

  it('does not mistake the H1 title for the matrix section', () => {
    // The H1 "Paper Comparison Matrix — …" must not be read as §2.
    const parsed = parseComparison(COMPARISON)
    expect(parsed.matrix[0]!.dimension).toBe('Problem framing')
  })
})

describe('comparisonMentionsPaper', () => {
  const paper: ComparisonMentionPaper = {
    id: 'P-002',
    slug: 'longformer-2020',
    title: 'Longformer',
    card_path: 'papers/longformer-2020/paper.md',
    identifiers: [{ scheme: 'arxiv', value: '2004.05150' }],
  }

  it('matches on the internal id present in the artifact', () => {
    expect(comparisonMentionsPaper(COMPARISON, paper)).toBe(true)
  })

  it('matches on an identifier (scheme:value)', () => {
    const content = 'a comparison that only cites arxiv:2004.05150'
    expect(comparisonMentionsPaper(content, paper)).toBe(true)
  })

  it('matches on the slug or the title', () => {
    expect(comparisonMentionsPaper('see papers/longformer-2020/paper.md', paper)).toBe(true)
    expect(comparisonMentionsPaper('the Longformer approach', paper)).toBe(true)
  })

  it('does not match an unrelated artifact', () => {
    const other: ComparisonMentionPaper = {
      id: 'P-009',
      slug: 'unrelated-2021',
      title: 'Unrelated Work',
      card_path: 'papers/unrelated-2021/paper.md',
      identifiers: [{ scheme: 'doi', value: '10.1/unrelated' }],
    }
    expect(comparisonMentionsPaper(COMPARISON, other)).toBe(false)
  })
})
