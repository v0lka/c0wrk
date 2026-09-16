// @vitest-environment jsdom
// CompareMatrix — the paper Compare section's renderer for a comparison artifact.
import { afterEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { CompareMatrix } from './CompareMatrix'

const FULL = `# Paper Comparison Matrix — sparse vs dense attention

## 1. Papers under comparison

| ID | Short name | Citation / identifier | One-line summary [Paper] |
| --- | --- | --- | --- |
| P-001 | Demo | arxiv:1706.03762 | dense baseline [Paper] |
| P-002 | Sparse | arxiv:2004.05150 | sparse variant [Paper] |

## 2. Comparison matrix

| Dimension | P-001 | P-002 |
| --- | --- | --- |
| Problem framing | dense attention | sparse attention |
| Evidence strength (present / weak / absent) | present | weak |

## 3. Fairness & comparability notes

- Different sequence lengths block a head-to-head claim.

## 5. Gaps none the papers address [Unknown]

- No unified benchmark.

## 6. Synthesis verdict

| Field | Value |
| --- | --- |
| Confidence in this synthesis (low / medium / high) | medium |
`

let root: Root | null = null
let container: HTMLDivElement | null = null

function render(slug: string, content: string): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(<CompareMatrix slug={slug} content={content} />)
  })
}

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  container?.remove()
  root = null
  container = null
})

describe('CompareMatrix', () => {
  it('renders the topic, papers table, matrix, and the template sections', () => {
    render('demo-vs-sparse', FULL)

    expect(document.querySelector('[data-testid="comparison-matrix"]')?.getAttribute('data-slug')).toBe(
      'demo-vs-sparse',
    )
    expect(document.querySelector('[data-testid="comparison-topic"]')?.textContent).toContain(
      'sparse vs dense attention',
    )
    expect(document.querySelectorAll('[data-testid="comparison-paper-row"]')).toHaveLength(2)

    // The dimension × paper grid: two dimension rows, each with two paper cells.
    expect(document.querySelector('[data-testid="comparison-grid"]')).not.toBeNull()
    expect(document.querySelectorAll('[data-testid="comparison-grid-row"]')).toHaveLength(2)
    expect(document.querySelectorAll('[data-testid="comparison-grid-cell"]')).toHaveLength(4)

    expect(document.querySelectorAll('[data-testid="comparison-fairness-item"]')).toHaveLength(1)
    expect(document.querySelectorAll('[data-testid="comparison-gaps-item"]')).toHaveLength(1)
    expect(document.querySelectorAll('[data-testid="comparison-verdict-row"]')).toHaveLength(1)
  })

  it('shows an honest empty state for an artifact with nothing recognizable', () => {
    render('empty', 'just some prose with no tables or headings')
    expect(document.querySelector('[data-testid="comparison-matrix"]')).toBeNull()
    expect(document.querySelector('[data-testid="comparison-empty"]')).not.toBeNull()
  })
})
