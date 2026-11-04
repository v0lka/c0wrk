// @vitest-environment jsdom
// SpecsFindings — the tier-1 findings list's CASCADE CARD layout (ADR-106
// D5): row 1 carries the kind chip + the file link, row 2 the detail text
// on its own line at the full card width (a long detail must never squeeze
// the row-1 controls). Renders NOTHING when clean — the header badge is
// the clean signal.
import { describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsFindings } from './SpecsFindings'
import type { SpecFinding } from '@/types/models'

let container: HTMLElement | null = null
let root: Root | null = null

async function render(findings: SpecFinding[]): Promise<void> {
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(<SpecsFindings findings={findings} onOpen={vi.fn()} />)
  })
}

function query(testId: string): HTMLElement | null {
  return container?.querySelector(`[data-testid="${testId}"]`) ?? null
}

const FINDINGS: SpecFinding[] = [
  { kind: 'PATH_MISSING', file: 'domains/testing.md', detail: 'core/x.go does not exist' },
  { kind: 'INDEX_STALE', file: 'domains/orphan.md', detail: 'not listed in INDEX.md' },
]

describe('SpecsFindings — cascade card', () => {
  it('renders nothing when the tree is clean', async () => {
    await render([])
    expect(query('specs-findings')).toBeNull()
  })

  it('row 1 carries the kind chip and the file link, row 2 the full-width detail', async () => {
    await render(FINDINGS)

    const cards = document.querySelectorAll('[data-testid="specs-finding"]')
    expect(cards).toHaveLength(2)

    const card = cards[0] as HTMLElement
    // Two stacked rows: the controls row and the detail row.
    const rows = Array.from(card.children)
    expect(rows).toHaveLength(2)

    // Row 1: the kind label + the clickable file (basename).
    expect(rows[0]!.textContent).toContain('Missing path')
    const link = rows[0]!.querySelector('[data-testid="specs-finding-file"]')!
    expect(link.textContent).toBe('testing.md')
    expect(link.getAttribute('data-file')).toBe('domains/testing.md')

    // Row 2: the detail on its OWN line — a block sibling of row 1, full
    // card width (not squeezed into the controls row).
    const detail = rows[1]!.querySelector('[data-testid="specs-finding-detail"]') ?? rows[1]!
    expect(detail.getAttribute('data-testid')).toBe('specs-finding-detail')
    expect(detail.textContent).toBe('core/x.go does not exist')
  })

  it('the file link opens the finding file through onOpen', async () => {
    const onOpen = vi.fn()
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
    await act(async () => {
      root!.render(<SpecsFindings findings={FINDINGS} onOpen={onOpen} />)
    })

    const links = document.querySelectorAll<HTMLButtonElement>(
      '[data-testid="specs-finding-file"]',
    )
    act(() => {
      links[1]!.click()
    })
    expect(onOpen).toHaveBeenCalledWith('domains/orphan.md')
  })
})
